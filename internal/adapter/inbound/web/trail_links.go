package web

import (
	"bytes"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// trailizeLinks is the per-request post-process the trail engine's cache
// design depends on (ADR 0005 decision 9): pane HTML is cached with plain
// /w/ document routes, and this pass rewrites each one into the link's
// post-click trail URL — truncation, dedup, and the depth cap are all baked
// into the href, so the link IS the state transition and the client stays
// logic-free (ADR 0003). The link whose target is the pane open to the
// right gets class="active-link": the highlighted links are the breadcrumb.
//
// Routes that are not pane addresses (versions, raw, external, anchors)
// pass through untouched — they escape the trail deliberately. Returns the
// fragment unchanged on any parse failure.
func trailizeLinks(fragment string, t trail, paneIdx int) string {
	ctx := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(fragment), ctx)
	if err != nil {
		return fragment
	}

	var buf bytes.Buffer
	for _, n := range nodes {
		trailizeNode(n, t, paneIdx)
		if err := html.Render(&buf, n); err != nil {
			return fragment
		}
	}
	return buf.String()
}

func trailizeNode(n *html.Node, t trail, paneIdx int) {
	if n.Type == html.ElementNode && n.DataAtom == atom.A {
		for i, attr := range n.Attr {
			if attr.Key != "href" {
				continue
			}
			addr, frag, ok := paneAddrFromRoute(attr.Val)
			if !ok {
				continue
			}
			n.Attr[i].Val = trailURL(trailAfterClick(t, paneIdx, addr))
			if frag != "" {
				n.Attr[i].Val += "#" + frag
			}
			if paneIdx+1 < len(t.Panes) && addr == t.Panes[paneIdx+1] {
				setClass(n, "active-link")
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		trailizeNode(c, t, paneIdx)
	}
}

// paneAddrFromRoute decodes an in-app /w/<world>/(d|tags)/<value> href into
// a pane address, returning any #fragment separately.
func paneAddrFromRoute(href string) (paneAddr, string, bool) {
	u, err := url.Parse(href)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return paneAddr{}, "", false
	}
	rest, ok := strings.CutPrefix(u.Path, "/w/")
	if !ok {
		return paneAddr{}, "", false
	}
	world, tail, ok := strings.Cut(rest, "/")
	if !ok || world == "" {
		return paneAddr{}, "", false
	}
	if dec, err := url.PathUnescape(world); err == nil {
		world = dec
	}
	kind, value, ok := strings.Cut(tail, "/")
	if !ok || value == "" {
		return paneAddr{}, "", false
	}
	switch kind {
	case paneDoc:
		return paneAddr{Kind: paneDoc, World: world, Value: "/" + value}, u.Fragment, true
	case paneTag:
		if strings.Contains(value, "/") {
			return paneAddr{}, "", false
		}
		if dec, err := url.PathUnescape(value); err == nil {
			value = dec
		}
		return paneAddr{Kind: paneTag, World: world, Value: value}, u.Fragment, true
	default:
		return paneAddr{}, "", false
	}
}

// setClass appends a class to the anchor (post-sanitizer injection — this
// pass adds only our own fixed token).
func setClass(n *html.Node, class string) {
	for i, attr := range n.Attr {
		if attr.Key == "class" {
			if !strings.Contains(" "+attr.Val+" ", " "+class+" ") {
				n.Attr[i].Val = attr.Val + " " + class
			}
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: "class", Val: class})
}
