package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
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
// whose info string names the content type (sniffed when absent). The bytes
// pass checkLogo like an upload would — a world is a shared write surface,
// so the document is not trusted just because it exists.
func decodeLogo(body string) *worldAsset {
	lang, info, content, ok := firstFence(body)
	if !ok {
		return nil
	}
	var blob []byte
	declared := ""
	switch lang {
	case "svg", "xml":
		blob, declared = []byte(content), "image/svg+xml"
	case "base64":
		var err error
		if blob, err = base64.StdEncoding.DecodeString(strings.Join(strings.Fields(content), "")); err != nil {
			return nil
		}
		declared = info
	default:
		return nil
	}
	ctype, err := checkLogo(blob, declared)
	if err != nil {
		return nil
	}
	return &worldAsset{ctype: ctype, blob: blob}
}

// rasterTypes are the inert image types a logo may be; anything else that
// is not an inert SVG is refused at both boundaries (upload and read).
var rasterTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// svgActive matches SVG constructs that could run or fetch: script, event
// handlers, javascript: URLs, embedded documents, and external references.
var svgActive = regexp.MustCompile(`(?i)<script|<foreignobject|<iframe|<embed|<object|javascript:|\son[a-z]+\s*=|href\s*=\s*["']?\s*(https?:|//)|@import|url\(\s*["']?\s*(https?:|//)|data:text/html`)

// checkLogo validates logo bytes against their declared type and returns the
// type to serve. Raster bytes must sniff as the declared type; an SVG must
// be inert. The served type is always one the browser treats as an image.
func checkLogo(blob []byte, declared string) (string, error) {
	if len(blob) == 0 {
		return "", errors.New("logo is empty")
	}
	if len(blob) > worldBrandMaxBytes {
		return "", errors.New("logo is larger than 256 KB")
	}
	declared = strings.ToLower(strings.TrimSpace(strings.SplitN(declared, ";", 2)[0]))
	if declared == "application/octet-stream" {
		declared = ""
	}
	trimmed := bytes.TrimSpace(blob)
	isSVG := bytes.HasPrefix(trimmed, []byte("<svg")) ||
		(bytes.HasPrefix(trimmed, []byte("<?xml")) && bytes.Contains(trimmed, []byte("<svg")))
	if declared == "image/svg+xml" || (declared == "" && isSVG) {
		if !isSVG {
			return "", errors.New("logo is not SVG markup")
		}
		if svgActive.Match(trimmed) {
			return "", errors.New("SVG logo must not contain scripts, event handlers, or external references")
		}
		return "image/svg+xml", nil
	}
	sniffed := strings.SplitN(http.DetectContentType(blob), ";", 2)[0]
	if declared == "" {
		declared = sniffed
	}
	if !rasterTypes[declared] {
		return "", errors.New("logo must be PNG, JPEG, GIF, WebP, or SVG")
	}
	if sniffed != declared {
		return "", errors.New("logo bytes do not match the declared image type")
	}
	return declared, nil
}

// firstFence returns the first fenced block's language, extra info, and
// content. A fence is three or more backticks; the closer must be at least
// as long (CommonMark), so content may itself contain shorter backtick runs
// (wrapFence picks the opener accordingly).
func firstFence(body string) (lang, info, content string, ok bool) {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		run := backtickPrefix(line)
		if run < 3 {
			continue
		}
		rest := strings.TrimSpace(line[run:])
		if strings.Contains(rest, "`") {
			continue // not a fence opener
		}
		lang, info, _ = strings.Cut(rest, " ")
		for j := i + 1; j < len(lines); j++ {
			if r := backtickPrefix(lines[j]); r >= run && strings.TrimSpace(lines[j][r:]) == "" {
				return strings.ToLower(lang), strings.TrimSpace(info), strings.Join(lines[i+1:j], "\n"), true
			}
		}
		return "", "", "", false // unterminated
	}
	return "", "", "", false
}

// backtickPrefix counts the leading backticks of a line.
func backtickPrefix(line string) int {
	return len(line) - len(strings.TrimLeft(line, "`"))
}

// wrapFence is the inverse the branding desk uses: an H1 and summary keep
// the style gate quiet; the fence carries the payload, opened with more
// backticks than any run inside it so the content can never close it early.
func wrapFence(title, summary, lang, info, content string) string {
	content = strings.TrimRight(content, "\n")
	fence := strings.Repeat("`", max(3, longestBacktickRun(content)+1))
	var b bytes.Buffer
	b.WriteString("# " + title + "\n\n" + summary + "\n\n" + fence + lang)
	if info != "" {
		b.WriteString(" " + info)
	}
	b.WriteString("\n" + content + "\n" + fence + "\n")
	return b.String()
}

// longestBacktickRun is the longest sequence of consecutive backticks in s.
func longestBacktickRun(s string) int {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return longest
}
