package web

import (
	"bytes"
	"fmt"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// parseFragment parses rendered HTML as the children of a <body>, the shape
// every rewrite pass over document content works on.
func parseFragment(fragment string) ([]*html.Node, error) {
	nodes, err := html.ParseFragment(strings.NewReader(fragment),
		&html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body})
	if err != nil {
		return nil, fmt.Errorf("parse html fragment: %w", err)
	}
	return nodes, nil
}

// rewriteFragment parses a fragment, lets visit mutate each top-level node,
// and renders the result. Callers treat an error as "pass the fragment
// through unchanged": every rewrite pass is best-effort presentation.
func rewriteFragment(fragment string, visit func(*html.Node)) (string, error) {
	nodes, err := parseFragment(fragment)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	for _, n := range nodes {
		visit(n)
		if err := html.Render(&buf, n); err != nil {
			return "", fmt.Errorf("render html fragment: %w", err)
		}
	}
	return buf.String(), nil
}
