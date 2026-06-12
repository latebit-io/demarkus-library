// Package markdown is the outbound adapter that implements port.Renderer with
// goldmark + bluemonday. goldmark mirrors the rest of the project (the TUI uses
// Glamour for terminals; the web uses goldmark for HTML); bluemonday closes the
// markdown-to-HTML XSS path on org-authored content.
package markdown

import (
	"bytes"
	"strings"

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
//
// A leading YAML frontmatter block is stripped first: demarkus carries
// metadata out of band, but worlds contain bodies that open with a ---…---
// metadata fence anyway (publishers that hand-wrote frontmatter, or
// republished a fetched document verbatim, header included). goldmark would
// render that fence as garbled text; to a reader it is metadata, not
// content.
func (r *Renderer) Render(markdown string) (string, error) {
	var buf bytes.Buffer
	if err := r.md.Convert([]byte(stripFrontmatter(markdown)), &buf); err != nil {
		return "", err
	}
	return r.policy.Sanitize(buf.String()), nil
}

// stripFrontmatter drops a leading YAML frontmatter block: a first line of
// exactly "---", closed by a line of exactly "---" or "...". Anything that
// does not match precisely (no opener, no closer, content before the fence)
// is returned unchanged — a thematic break mid-document is content, and an
// unclosed fence is safer rendered than silently swallowing the whole body.
func stripFrontmatter(markdown string) string {
	rest, ok := strings.CutPrefix(markdown, "---\n")
	if !ok {
		if rest, ok = strings.CutPrefix(markdown, "---\r\n"); !ok {
			return markdown
		}
	}
	for off := 0; off < len(rest); {
		lineEnd := strings.IndexByte(rest[off:], '\n')
		if lineEnd < 0 {
			break // unclosed fence — leave the document alone
		}
		line := strings.TrimSuffix(rest[off:off+lineEnd], "\r")
		if line == "---" || line == "..." {
			return rest[off+lineEnd+1:]
		}
		off += lineEnd + 1
	}
	return markdown
}
