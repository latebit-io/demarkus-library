package web

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"gopkg.in/yaml.v3"
)

// In-world branding (ADR 0008): a world brands itself through markdown
// documents under WorldBrandDir. branding.md is the anchor, so an unbranded
// world costs one read per TTL and never touches the others.
const (
	WorldBrandDir  = "/.well-known/library/"
	WorldBrandDoc  = WorldBrandDir + "branding.md"
	WorldBrandCSS  = WorldBrandDir + "site.css.md"
	WorldBrandLogo = WorldBrandDir + "logo.md"

	worldBrandTTL      = time.Minute
	worldBrandMaxBytes = 256 << 10
)

// rawReader is the slice of the reading port this resolver needs: branding
// documents are read as source, never rendered.
type rawReader interface {
	Raw(ctx context.Context, world, path string) (domain.RawDocument, error)
}

// brandingFile is the yaml fence of branding.md. Unknown keys are ignored so
// a newer world does not break an older library.
type brandingFile struct {
	Name  string            `yaml:"name"`
	Theme map[string]string `yaml:"theme,omitempty"`
}

// worldAsset is one unwrapped asset, ready to serve.
type worldAsset struct {
	ctype string
	blob  []byte
}

// worldBrand is a world's resolved identity; the zero value means the world
// declares none, which is cached too so absence stays cheap.
type worldBrand struct {
	name   string
	tokens map[string]string
	rawCSS string      // the stylesheet as published, for the desk
	sheet  *worldAsset // tokens + rawCSS, ready to serve; nil ⇒ none
	logo   *worldAsset

	fetched time.Time
}

// brandDesk is what the branding desk pre-fills from a world's documents.
type brandDesk struct {
	Name    string
	Tokens  map[string]string
	CSS     string
	LogoURL string
}

// WorldBrands resolves and caches in-world branding per world. Reads carry
// the requesting reader's identity; a resolved brand is then shared for the
// TTL, which is fine because branding is not confidential.
type WorldBrands struct {
	source rawReader
	ttl    time.Duration
	now    func() time.Time

	mu      sync.Mutex
	byWorld map[string]worldBrand
}

// NewWorldBrands builds the resolver over a source of raw documents.
func NewWorldBrands(source rawReader) *WorldBrands {
	return &WorldBrands{source: source, ttl: worldBrandTTL, now: time.Now, byWorld: map[string]worldBrand{}}
}

// For returns the world's identity as URLs the templates can use; ok is false
// when the world declares no branding of its own.
func (w *WorldBrands) For(ctx context.Context, world string) (WorldBranding, bool) {
	brand := w.get(ctx, world)
	if brand.name == "" && brand.sheet == nil && brand.logo == nil {
		return WorldBranding{}, false
	}
	resolved := WorldBranding{Name: brand.name}
	if brand.logo != nil {
		resolved.LogoURL = themeWorldsPrefix + world + "/logo"
	}
	if brand.sheet != nil {
		resolved.CSSURL = themeWorldsPrefix + world + "/site.css"
	}
	return resolved, true
}

// Asset returns the bytes behind /theme/worlds/<world>/<file>.
func (w *WorldBrands) Asset(ctx context.Context, world, file string) (worldAsset, bool) {
	brand := w.get(ctx, world)
	switch file {
	case "logo":
		if brand.logo != nil {
			return *brand.logo, true
		}
	case "site.css":
		if brand.sheet != nil {
			return *brand.sheet, true
		}
	}
	return worldAsset{}, false
}

// Desk returns the world's stored branding as the desk's form fields.
func (w *WorldBrands) Desk(ctx context.Context, world string) brandDesk {
	brand := w.get(ctx, world)
	desk := brandDesk{Name: brand.name, Tokens: brand.tokens, CSS: brand.rawCSS}
	if brand.logo != nil {
		desk.LogoURL = themeWorldsPrefix + world + "/logo"
	}
	return desk
}

// Invalidate drops a world's cached brand so the next request re-reads it;
// the branding desk calls it after every successful write.
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
	brand, ok := w.byWorld[world]
	w.mu.Unlock()
	if ok && w.now().Sub(brand.fetched) < w.ttl {
		return brand
	}
	brand = w.load(ctx, world)
	brand.fetched = w.now()
	w.mu.Lock()
	w.byWorld[world] = brand
	w.mu.Unlock()
	return brand
}

// load reads the branding documents. Best effort by design: any read failure
// (missing, denied, transport) means "no in-world branding" and is cached
// like a real absence, so a flapping world cannot make every render retry.
func (w *WorldBrands) load(ctx context.Context, world string) worldBrand {
	var brand worldBrand
	raw, err := w.source.Raw(ctx, world, WorldBrandDoc)
	if err != nil {
		return brand
	}
	if f, ok := firstFence(raw.Body); ok && (f.Lang == "yaml" || f.Lang == "yml") {
		var declared brandingFile
		if yaml.Unmarshal([]byte(f.Content), &declared) == nil {
			brand.name = strings.TrimSpace(declared.Name)
			brand.tokens = declared.Theme
		}
	}
	if raw, err := w.source.Raw(ctx, world, WorldBrandCSS); err == nil {
		if f, ok := firstFence(raw.Body); ok && f.Lang == "css" && len(f.Content) <= worldBrandMaxBytes {
			brand.rawCSS = f.Content
		}
	}
	// Tokens first so the world's own CSS can still override them. Unsafe or
	// unknown tokens drop the block, like any other unreadable branding.
	sheet, err := tokensCSS(brand.tokens)
	if err != nil {
		sheet, brand.tokens = "", nil
	}
	if sheet+brand.rawCSS != "" {
		brand.sheet = &worldAsset{ctype: themeCSSType, blob: []byte(sheet + brand.rawCSS)}
	}
	if raw, err := w.source.Raw(ctx, world, WorldBrandLogo); err == nil {
		brand.logo = decodeLogo(raw.Body)
	}
	return brand
}
