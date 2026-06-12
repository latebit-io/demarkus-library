package web

import (
	"bytes"
	"net/url"
	"path"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// rewriteLinks turns demarkus links in a rendered HTML fragment into in-app
// routes. Relative and absolute world paths stay in the current world
// (/w/<world>/d/<path>, resolved against basePath for relative links); a
// mark://<world-or-host>/<path> link crosses to its target world — this is
// what makes the distributed knowledge graph traversable from the reading
// room. External links (http/https/mailto/tel), in-page anchors, and
// unparseable hrefs are left untouched. This is a web-adapter concern: only
// this layer knows the URL scheme, so the core and markdown adapter stay
// free of it.
//
// On any parse failure it returns the fragment unchanged — link rewriting is an
// enhancement, never a reason to fail a render.
func rewriteLinks(fragment, world, basePath string) string {
	ctx := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(fragment), ctx)
	if err != nil {
		return fragment
	}

	var buf bytes.Buffer
	for _, n := range nodes {
		rewriteNode(n, world, basePath)
		if err := html.Render(&buf, n); err != nil {
			return fragment
		}
	}
	return buf.String()
}

// catalogPath matches a bare world path (no whitespace, leading slash) — the
// shape of the Path column the LOOKUP catalog table emits as plain text.
var catalogPath = regexp.MustCompile(`^/\S+$`)

// linkifyCatalogPaths makes the LOOKUP catalog table click-through: a table
// cell whose entire text is a world path becomes a /w/<world>/d/<path> link
// into the searched world. LOOKUP returns the path as plain text (not a
// markdown link), so this is the catalog's counterpart to rewriteLinks.
// Returns the fragment unchanged on any parse failure.
func linkifyCatalogPaths(fragment, world string) string {
	ctx := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(fragment), ctx)
	if err != nil {
		return fragment
	}

	var buf bytes.Buffer
	for _, n := range nodes {
		linkifyNode(n, world)
		if err := html.Render(&buf, n); err != nil {
			return fragment
		}
	}
	return buf.String()
}

func linkifyNode(n *html.Node, world string) {
	if n.DataAtom == atom.Td && n.FirstChild != nil && n.FirstChild == n.LastChild &&
		n.FirstChild.Type == html.TextNode {
		if p := strings.TrimSpace(n.FirstChild.Data); catalogPath.MatchString(p) {
			a := &html.Node{Type: html.ElementNode, DataAtom: atom.A, Data: "a",
				Attr: []html.Attribute{{Key: "href", Val: docRoute(world, p)}}}
			a.AppendChild(&html.Node{Type: html.TextNode, Data: p})
			n.RemoveChild(n.FirstChild)
			n.AppendChild(a)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		linkifyNode(c, world)
	}
}

func rewriteNode(n *html.Node, world, basePath string) {
	if n.Type == html.ElementNode && n.DataAtom == atom.A {
		for i, attr := range n.Attr {
			if attr.Key == "href" {
				n.Attr[i].Val = rewriteHref(attr.Val, world, basePath)
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		rewriteNode(c, world, basePath)
	}
}

// rewriteHref maps a single href to an in-app route, or returns it unchanged
// if it is external or not a demarkus link.
func rewriteHref(href, world, basePath string) string {
	if href == "" || strings.HasPrefix(href, "#") {
		return href
	}

	// VERSIONS emits percent-encoded paths (e.g. %2Fdoc.md/v2); decode first.
	if dec, err := url.PathUnescape(href); err == nil {
		href = dec
	}

	u, err := url.Parse(href)
	if err != nil {
		return href
	}

	// Leave anything with a non-demarkus scheme alone (http, https, mailto, tel).
	if u.Scheme != "" && u.Scheme != "mark" {
		return href
	}

	targetWorld := world
	worldPath := u.Path
	hadTrailingSlash := strings.HasSuffix(worldPath, "/")

	switch {
	case u.Scheme == "mark":
		// mark://<world-or-host>/<path> — cross-world: the authority IS
		// the target world (a knowledge-system name, or a host[:port] —
		// u.Host carries the port when present).
		if u.Host != "" {
			targetWorld = u.Host
		}
		if worldPath == "" {
			worldPath = "/"
			hadTrailingSlash = true
		}
	case strings.HasPrefix(worldPath, "/"):
		// already an absolute world path, current world
	default:
		// relative to the current document's directory, current world
		worldPath = path.Join(path.Dir(basePath), worldPath)
	}

	worldPath = path.Clean(worldPath)
	if worldPath == "." || worldPath == "/" {
		worldPath = "/"
	} else if hadTrailingSlash && !strings.HasSuffix(worldPath, "/") {
		worldPath += "/" // preserve dir-ness so the route lists rather than fetches
	}

	rewritten := docRoute(targetWorld, worldPath)
	if u.Fragment != "" {
		rewritten += "#" + u.Fragment
	}
	return rewritten
}

// docRoute builds the in-app document route for (world, path). The world
// segment is escaped (host:port worlds carry a colon; names are clean).
func docRoute(world, worldPath string) string {
	return "/w/" + url.PathEscape(world) + "/d" + worldPath
}
