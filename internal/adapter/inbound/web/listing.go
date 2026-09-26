package web

import (
	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// listingFragment shelves folders ahead of documents and wraps the listing so
// the room styles it as a table of contents, never touching lists inside
// documents. Runs on /w/ hrefs; an unparsable fragment is wrapped as it came.
func listingFragment(fragment string) string {
	if out, err := rewriteFragment(fragment, shelveLists); err == nil {
		fragment = out
	}
	return `<div class="listing">` + fragment + `</div>`
}

// shelveLists moves each list's folder rows ahead of its document rows,
// keeping the world's order within each group.
func shelveLists(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		shelveLists(c)
	}
	if n.Type != html.ElementNode || n.DataAtom != atom.Ul {
		return
	}
	var folders, rest []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if isFolderRow(c) {
			folders = append(folders, c)
		} else {
			rest = append(rest, c)
		}
	}
	if len(folders) == 0 {
		return
	}
	for _, c := range append(folders, rest...) {
		n.RemoveChild(c)
		n.AppendChild(c)
	}
}

// isFolderRow reports whether an <li> leads with a link to a listing.
func isFolderRow(n *html.Node) bool {
	if n.Type != html.ElementNode || n.DataAtom != atom.Li {
		return false
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && c.DataAtom == atom.A {
			addr, ok := anchorDocAddr(c)
			return ok && domain.IsListingPath(addr.Value)
		}
	}
	return false
}
