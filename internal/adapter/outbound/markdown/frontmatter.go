package markdown

import (
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// splitFrontmatter separates a leading YAML frontmatter block from the body:
// a first line of exactly "---", closed by a line of exactly "---" or "...",
// whose content actually parses as YAML metadata (isFrontmatterFence). It
// returns the fence content (without the delimiters) and the remaining body.
// Anything that does not match precisely (no opener, no closer, content
// before the fence, or fence content that is ordinary prose between two
// thematic breaks) returns ("", markdown) unchanged — a thematic break is
// content, and an unclosed fence is safer rendered than silently swallowing
// the whole body.
//
// demarkus carries metadata out of band, but worlds contain bodies that open
// with a metadata fence anyway (publishers that hand-wrote frontmatter, or
// republished a fetched document verbatim, header included). The fence is
// stripped from the body and rendered friendly instead: parseFrontmatter
// turns it into the margin's document-properties block (ADR 0005 decision 7).
func splitFrontmatter(markdown string) (fence, body string) {
	rest, ok := strings.CutPrefix(markdown, "---\n")
	if !ok {
		if rest, ok = strings.CutPrefix(markdown, "---\r\n"); !ok {
			return "", markdown
		}
	}
	for off := 0; off < len(rest); {
		lineEnd := strings.IndexByte(rest[off:], '\n')
		if lineEnd < 0 {
			// Last line has no trailing newline: a closer here means the
			// document was nothing but frontmatter.
			if line := strings.TrimSuffix(rest[off:], "\r"); line == "---" || line == "..." {
				if !isFrontmatterFence(rest[:off]) {
					return "", markdown
				}
				return rest[:off], ""
			}
			break // unclosed fence — leave the document alone
		}
		line := strings.TrimSuffix(rest[off:off+lineEnd], "\r")
		if line == "---" || line == "..." {
			if !isFrontmatterFence(rest[:off]) {
				return "", markdown
			}
			return rest[:off], rest[off+lineEnd+1:]
		}
		off += lineEnd + 1
	}
	return "", markdown
}

// isFrontmatterFence reports whether candidate fence content is actually
// metadata: empty, or a YAML mapping. A document that merely opens with a
// thematic break ("---\nintro\n---\nbody") parses as a scalar or sequence,
// not a mapping — that is content, and stripping it would silently lose it.
// Deliberately looser than parseFrontmatter, which additionally drops
// non-displayable values: a mapping of nested structures is still
// frontmatter (strip it) even though it yields no margin properties.
func isFrontmatterFence(fence string) bool {
	if strings.TrimSpace(fence) == "" {
		return true // degenerate "---\n---" header: metadata-shaped, zero keys
	}
	var root yaml.Node
	if yaml.Unmarshal([]byte(fence), &root) != nil {
		return false
	}
	return root.Kind == yaml.DocumentNode && len(root.Content) > 0 &&
		root.Content[0].Kind == yaml.MappingNode
}

// parseFrontmatter turns a frontmatter fence into ordered display properties.
// Only top-level scalar keys survive; values are scalars or sequences of
// scalars (joined ", "). Nested structures don't flatten legibly into one
// margin line and are skipped, as is anything that fails to parse as YAML —
// the properties block is a courtesy rendering, never a reason to fail.
func parseFrontmatter(fence string) []domain.Property {
	if strings.TrimSpace(fence) == "" {
		return nil
	}
	var root yaml.Node
	if yaml.Unmarshal([]byte(fence), &root) != nil {
		return nil
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil
	}
	mapping := root.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return nil
	}
	props := make([]domain.Property, 0, len(mapping.Content)/2)
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key, value := mapping.Content[i], mapping.Content[i+1]
		if key.Kind != yaml.ScalarNode || strings.TrimSpace(key.Value) == "" {
			continue
		}
		text, ok := propertyValue(value)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		props = append(props, domain.Property{Key: key.Value, Value: text})
	}
	if len(props) == 0 {
		return nil
	}
	return props
}

// propertyValue flattens a scalar or a sequence of scalars to display text.
func propertyValue(v *yaml.Node) (string, bool) {
	switch v.Kind {
	case yaml.ScalarNode:
		return v.Value, true
	case yaml.SequenceNode:
		parts := make([]string, 0, len(v.Content))
		for _, item := range v.Content {
			if item.Kind != yaml.ScalarNode {
				return "", false
			}
			parts = append(parts, item.Value)
		}
		return strings.Join(parts, ", "), true
	default:
		return "", false
	}
}
