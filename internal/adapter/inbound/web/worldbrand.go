package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/port"
	"gopkg.in/yaml.v3"
)

// In-world branding (ADR 0008): a world brands itself through markdown
// documents under WorldBrandDir — demarkus serves only markdown, so each asset
// rides in the document's first fenced block and the library unwraps it.
// branding.md is the anchor: without it the other documents are not read, so
// an unbranded world costs one miss per TTL.
const (
	WorldBrandDir  = "/.well-known/library/"
	WorldBrandDoc  = WorldBrandDir + "branding.md" // ```yaml fence: name
	WorldBrandCSS  = WorldBrandDir + "site.css.md" // ```css fence
	WorldBrandLogo = WorldBrandDir + "logo.md"     // ```svg fence, or ```base64 <content-type>

	worldBrandTTL      = time.Minute
	worldBrandMaxBytes = 256 << 10
)

// worldBrandFile is the yaml fence of branding.md. Lenient on unknown keys
// so a newer world does not break an older library.
type worldBrandFile struct {
	Name string `yaml:"name"`
}

// worldAsset is one unwrapped asset, ready to serve.
type worldAsset struct {
	ctype string
	blob  []byte
}

// worldBrand is a world's resolved in-world identity; a zero value means the
// world declares none (also cached, so absence is cheap).
type worldBrand struct {
	name    string
	css     *worldAsset
	logo    *worldAsset
	fetched time.Time
}

// WorldBrands resolves and caches in-world branding per world. Reads go
// through the reader port with the request's identity (broker mode: the
// session bearer); a resolved brand is shared by every reader for the TTL,
// which is fine — branding is not confidential. Failures degrade to "none":
// the file manifest and the room identity still apply.
type WorldBrands struct {
	reader port.Reader
	ttl    time.Duration
	now    func() time.Time

	mu      sync.Mutex
	byWorld map[string]worldBrand
}

// NewWorldBrands builds the resolver over the reading port.
func NewWorldBrands(reader port.Reader) *WorldBrands {
	return &WorldBrands{reader: reader, ttl: worldBrandTTL, now: time.Now, byWorld: map[string]worldBrand{}}
}

// For returns the world's in-world identity as URLs the templates can use;
// ok=false when the world declares none.
func (w *WorldBrands) For(ctx context.Context, world string) (WorldBranding, bool) {
	wb := w.get(ctx, world)
	if wb.name == "" && wb.css == nil && wb.logo == nil {
		return WorldBranding{}, false
	}
	r := WorldBranding{Name: wb.name}
	if wb.logo != nil {
		r.LogoURL = themeWorldsPrefix + world + "/logo"
	}
	if wb.css != nil {
		r.CSSURL = themeWorldsPrefix + world + "/site.css"
	}
	return r, true
}

// Asset returns the unwrapped bytes behind /theme/worlds/<world>/<file>.
func (w *WorldBrands) Asset(ctx context.Context, world, file string) (worldAsset, bool) {
	wb := w.get(ctx, world)
	switch file {
	case "logo":
		if wb.logo != nil {
			return *wb.logo, true
		}
	case "site.css":
		if wb.css != nil {
			return *wb.css, true
		}
	}
	return worldAsset{}, false
}

// Desk returns what the branding desk pre-fills: the name, the stylesheet
// source, and the current logo URL (empty ⇒ none).
func (w *WorldBrands) Desk(ctx context.Context, world string) (name, css, logoURL string) {
	wb := w.get(ctx, world)
	if wb.css != nil {
		css = string(wb.css.blob)
	}
	if wb.logo != nil {
		logoURL = themeWorldsPrefix + world + "/logo"
	}
	return wb.name, css, logoURL
}

// Invalidate drops a world's cached brand so the next request re-reads it
// (the branding desk calls it after a save).
func (w *WorldBrands) Invalidate(world string) {
	w.mu.Lock()
	delete(w.byWorld, world)
	w.mu.Unlock()
}

func (w *WorldBrands) get(ctx context.Context, world string) worldBrand {
	if w == nil || world == "" {
		return worldBrand{}
	}
	w.mu.Lock()
	wb, ok := w.byWorld[world]
	w.mu.Unlock()
	if ok && w.now().Sub(wb.fetched) < w.ttl {
		return wb
	}
	wb = w.load(ctx, world)
	wb.fetched = w.now()
	w.mu.Lock()
	w.byWorld[world] = wb
	w.mu.Unlock()
	return wb
}

// load reads the branding documents. Any read failure (missing, denied,
// transport) is "no in-world branding" — cached like a real absence, so a
// flapping world cannot turn every page render into a retry storm.
func (w *WorldBrands) load(ctx context.Context, world string) worldBrand {
	var wb worldBrand
	raw, err := w.reader.Raw(ctx, world, WorldBrandDoc)
	if err != nil {
		return wb
	}
	if lang, _, body, ok := firstFence(raw.Body); ok && (lang == "yaml" || lang == "yml") {
		var f worldBrandFile
		if yaml.Unmarshal([]byte(body), &f) == nil {
			wb.name = strings.TrimSpace(f.Name)
		}
	}
	if raw, err := w.reader.Raw(ctx, world, WorldBrandCSS); err == nil {
		if lang, _, body, ok := firstFence(raw.Body); ok && lang == "css" && len(body) <= worldBrandMaxBytes {
			wb.css = &worldAsset{ctype: themeCSSType, blob: []byte(body)}
		}
	}
	if raw, err := w.reader.Raw(ctx, world, WorldBrandLogo); err == nil {
		wb.logo = decodeLogo(raw.Body)
	}
	return wb
}

// decodeLogo unwraps logo.md: an ```svg fence verbatim, or a ```base64 fence
// whose info string names the content type (sniffed when absent).
func decodeLogo(body string) *worldAsset {
	lang, info, content, ok := firstFence(body)
	if !ok {
		return nil
	}
	switch lang {
	case "svg", "xml":
		if len(content) > worldBrandMaxBytes {
			return nil
		}
		return &worldAsset{ctype: "image/svg+xml", blob: []byte(content)}
	case "base64":
		blob, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(content), ""))
		if err != nil || len(blob) == 0 || len(blob) > worldBrandMaxBytes {
			return nil
		}
		ctype := strings.TrimSpace(info)
		if ctype == "" {
			ctype = http.DetectContentType(blob)
		}
		return &worldAsset{ctype: ctype, blob: blob}
	}
	return nil
}

// fenceRE matches a fenced block: the info string's first word is the
// language, the rest is free text (the base64 fence carries a content type).
var fenceRE = regexp.MustCompile("(?s)(?:^|\n)```[ \t]*([^\\s`]*)[ \t]*([^\n]*)\n(.*?)\n```[ \t]*(?:\n|$)")

// firstFence returns the first fenced block's language, extra info, and
// content (without the trailing newline).
func firstFence(body string) (lang, info, content string, ok bool) {
	m := fenceRE.FindStringSubmatch(body)
	if m == nil {
		return "", "", "", false
	}
	return strings.ToLower(m[1]), strings.TrimSpace(m[2]), m[3], true
}

// wrapFence is the inverse the branding desk uses: an H1 and summary keep
// the style gate quiet; the fence carries the payload.
func wrapFence(title, summary, lang, info, content string) string {
	var b bytes.Buffer
	b.WriteString("# " + title + "\n\n" + summary + "\n\n```" + lang)
	if info != "" {
		b.WriteString(" " + info)
	}
	b.WriteString("\n" + strings.TrimRight(content, "\n") + "\n```\n")
	return b.String()
}
