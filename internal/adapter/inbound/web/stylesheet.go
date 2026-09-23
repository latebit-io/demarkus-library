package web

import (
	"errors"
	"fmt"
	"regexp"
)

// Stylesheet validation for CSS from a document (ADR 0008). CSS cannot run
// script, but an attribute selector plus a background URL leaks the value to
// another origin, so a document's stylesheet may not name one.

// cssActive matches what a stylesheet may not contain: anything that fetches
// from another origin, imports, legacy script hooks, a run of markup, or a
// backslash, since CSS escapes could spell any of those past the match.
var cssActive = regexp.MustCompile(`(?i)@import|expression\s*\(|-moz-binding|behavior\s*:|javascript:|[a-z][a-z0-9+.-]*://|(url|src|image|image-set)\(\s*["']?\s*//|["']//|<\s*/?\s*(script|style)|\\`)

// checkCSS refuses a stylesheet that could reach out of the page. Size is
// bounded by worldBrandMaxBytes so the regexp runs over a known maximum.
func checkCSS(sheet string) error {
	if len(sheet) > worldBrandMaxBytes {
		return errors.New("stylesheet is larger than 256 KB")
	}
	if loc := cssActive.FindStringIndex(sheet); loc != nil {
		return fmt.Errorf("stylesheet may not contain %q: only same-origin, relative, or data: references are allowed", sheet[loc[0]:loc[1]])
	}
	return nil
}
