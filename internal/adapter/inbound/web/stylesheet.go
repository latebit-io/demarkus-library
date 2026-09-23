package web

import (
	"errors"
	"fmt"
	"regexp"
)

// Stylesheet validation for CSS that arrives in a document (ADR 0008). CSS
// cannot run script, but it can fetch: a selector on an attribute value plus
// a background URL leaks that value, character by character, to whoever
// hosts the URL. So a document's stylesheet may not name another origin.
// The page CSP blocks the same fetches; this check refuses them at the door
// so the desk can say why, and so the rule holds on a browser without CSP.

// cssActive matches what a stylesheet may not contain: anything that fetches
// from another origin, imports, legacy script hooks, or a run of markup.
var cssActive = regexp.MustCompile(`(?i)@import|expression\s*\(|-moz-binding|behavior\s*:|javascript:|[a-z][a-z0-9+.-]*://|(url|src|image|image-set)\(\s*["']?\s*//|["']//|<\s*/?\s*(script|style)`)

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
