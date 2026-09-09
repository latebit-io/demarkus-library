package web

import (
	"fmt"
	"strings"
)

// Design tokens: the branding surface for operators who do not write CSS.
// Names match the :root custom properties in library.css, so a token map
// renders to a stylesheet the room already knows how to honor.

// brandTokens are the tokens an operator may set, in emission order. Anything
// outside this list is rejected: the map becomes CSS, so the key set is a
// trust boundary, not a convenience.
var brandTokens = []string{
	"paper", "ink", "muted", "faint",
	"font-prose", "font-ui", "font-mono",
	"ok", "warn", "danger", "info", "accent",
	"margin-w", "gutter",
}

// tokenHints are the placeholders the branding desk shows per token.
var tokenHints = map[string]string{
	"paper":      "light-dark(#f7f3ea, #101418)",
	"ink":        "light-dark(#1d2733, #d8e1ea)",
	"muted":      "light-dark(#1d273399, #d8e1ea99)",
	"faint":      "light-dark(#1d273322, #d8e1ea2a)",
	"font-prose": "Palatino, Georgia, serif",
	"font-ui":    "system-ui, sans-serif",
	"font-mono":  "ui-monospace, monospace",
	"ok":         "#1a7f37",
	"warn":       "#9a6700",
	"danger":     "#cf222e",
	"info":       "#0969da",
	"accent":     "#8250df",
	"margin-w":   "250px",
	"gutter":     "2.5rem",
}

// tokenBanned are the substrings a value may not contain: they would end the
// declaration, start a rule, or fetch something.
var tokenBanned = []string{"{", "}", ";", "<", ">", "\\", "url(", "@import", "javascript:", "expression("}

const tokenMaxLen = 200

// tokensCSS renders a token map as a :root rule, empty when nothing is set.
// An unknown name or an unsafe value is an error, so a manifest typo stops
// startup and an untrusted document simply loses its tokens.
func tokensCSS(tokens map[string]string) (string, error) {
	for name := range tokens {
		if tokenHints[name] == "" {
			return "", fmt.Errorf("unknown design token %q", name)
		}
	}
	var b strings.Builder
	for _, name := range brandTokens {
		value := strings.TrimSpace(tokens[name])
		if value == "" {
			continue
		}
		if len(value) > tokenMaxLen {
			return "", fmt.Errorf("token %q value is longer than %d characters", name, tokenMaxLen)
		}
		lowered := strings.ToLower(value)
		for _, banned := range tokenBanned {
			if strings.Contains(lowered, banned) {
				return "", fmt.Errorf("token %q value may not contain %q", name, banned)
			}
		}
		if b.Len() == 0 {
			b.WriteString(":root {\n")
		}
		fmt.Fprintf(&b, "  --%s: %s;\n", name, value)
	}
	if b.Len() == 0 {
		return "", nil
	}
	b.WriteString("}\n")
	return b.String(), nil
}
