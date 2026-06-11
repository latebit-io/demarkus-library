// Package markdown is the outbound adapter that implements port.Renderer with
// goldmark + bluemonday. goldmark mirrors the rest of the project (the TUI uses
// Glamour for terminals; the web uses goldmark for HTML); bluemonday closes the
// markdown-to-HTML XSS path on org-authored content.
package markdown

import (
	"bytes"

	"github.com/latebit/demarkus-library/internal/core/port"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// Renderer renders GFM markdown to sanitized HTML.
type Renderer struct {
	md     goldmark.Markdown
	policy *bluemonday.Policy
}

// compile-time check that Renderer satisfies the outbound port.
var _ port.Renderer = (*Renderer)(nil)

// NewRenderer builds a GFM renderer with a UGC sanitization policy.
func NewRenderer() *Renderer {
	return &Renderer{
		md:     goldmark.New(goldmark.WithExtensions(extension.GFM)),
		policy: bluemonday.UGCPolicy(),
	}
}

// Render renders markdown then sanitizes the result. Sanitize always runs after
// render — never trust the HTML goldmark emits from untrusted source.
func (r *Renderer) Render(markdown string) (string, error) {
	var buf bytes.Buffer
	if err := r.md.Convert([]byte(markdown), &buf); err != nil {
		return "", err
	}
	return r.policy.Sanitize(buf.String()), nil
}
