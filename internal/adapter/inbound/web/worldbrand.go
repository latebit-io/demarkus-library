package web

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"gopkg.in/yaml.v3"
)

// In-world branding (ADR 0008): a world brands itself through markdown
// documents under WorldBrandDir. branding.md is the anchor, so an unbranded
// world costs one read per TTL and never touches the others. The hub world's
// documents also brand the room as a whole (name, logo, favicon, stylesheet).
const (
	WorldBrandDir     = "/.well-known/library/"
	WorldBrandDoc     = WorldBrandDir + "branding.md"
	WorldBrandCSS     = WorldBrandDir + "site.css.md"
	WorldBrandLogo    = WorldBrandDir + "logo.md"
	WorldBrandFavicon = WorldBrandDir + "favicon.md"

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
	name    string
	tokens  map[string]string
	rawCSS  string      // the stylesheet as published, for the desk
	sheet   *worldAsset // tokens + rawCSS, ready to serve; nil ⇒ none
	logo    *worldAsset
	favicon *worldAsset // read for the hub only: the favicon link lives in the head

	fetched time.Time
}

func (b worldBrand) empty() bool {
	return b.name == "" && b.sheet == nil && b.logo == nil && b.favicon == nil
}

// brandDesk is what the branding desk pre-fills from a world's documents.
type brandDesk struct {
	Name       string
	Tokens     map[string]string
	CSS        string
	LogoURL    string
	FaviconURL string
}

// WorldBrands resolves and caches in-world branding per world. Reads carry
// the requesting reader's identity; a resolved brand is then shared for the
// TTL, which is fine because branding is not confidential.
type WorldBrands struct {
	source rawReader
	hub    string // the world whose documents brand the room; "" ⇒ none
	ttl    time.Duration
	now    func() time.Time

	mu      sync.Mutex
	byWorld map[string]worldBrand
}

// NewWorldBrands builds the resolver over a source of raw documents.
func NewWorldBrands(source rawReader) *WorldBrands {
	return &WorldBrands{source: source, ttl: worldBrandTTL, now: time.Now, byWorld: map[string]worldBrand{}}
}

// WithHub names the world whose branding documents apply to the whole room:
// every page's name, logo, favicon, and stylesheet, the login page included.
func (w *WorldBrands) WithHub(hub string) *WorldBrands {
	w.hub = strings.TrimSpace(hub)
	return w
}

// Hub is the room-branding world, or "".
func (w *WorldBrands) Hub() string {
	if w == nil {
		return ""
	}
	return w.hub
}

// For returns the world's identity as URLs the templates can use; ok is false
// when the world declares no branding of its own.
func (w *WorldBrands) For(ctx context.Context, world string) (WorldBranding, bool) {
	brand := w.get(ctx, world)
	if brand.empty() {
		return WorldBranding{}, false
	}
	resolved := WorldBranding{Name: brand.name}
	if brand.logo != nil {
		resolved.LogoURL = themeWorldsPrefix + world + "/logo"
	}
	if brand.sheet != nil {
		resolved.CSSURL = themeWorldsPrefix + world + "/site.css"
	}
	if brand.favicon != nil {
		resolved.FaviconURL = themeWorldsPrefix + world + "/favicon"
	}
	return resolved, true
}

// Room returns the identity the hub world declares for the whole room; ok is
// false without a hub or when the hub declares nothing.
func (w *WorldBrands) Room(ctx context.Context) (WorldBranding, bool) {
	if w == nil || w.hub == "" {
		return WorldBranding{}, false
	}
	return w.For(ctx, w.hub)
}

// Asset returns the bytes behind /theme/worlds/<world>/<file>.
func (w *WorldBrands) Asset(ctx context.Context, world, file string) (worldAsset, bool) {
	brand := w.get(ctx, world)
	var asset *worldAsset
	switch file {
	case "logo":
		asset = brand.logo
	case "site.css":
		asset = brand.sheet
	case "favicon":
		asset = brand.favicon
	}
	if asset == nil {
		return worldAsset{}, false
	}
	return *asset, true
}

// Desk returns the world's stored branding as the desk's form fields.
func (w *WorldBrands) Desk(ctx context.Context, world string) brandDesk {
	brand := w.get(ctx, world)
	desk := brandDesk{Name: brand.name, Tokens: brand.tokens, CSS: brand.rawCSS}
	if brand.logo != nil {
		desk.LogoURL = themeWorldsPrefix + world + "/logo"
	}
	if brand.favicon != nil {
		desk.FaviconURL = themeWorldsPrefix + world + "/favicon"
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
	cached, ok := w.byWorld[world]
	w.mu.Unlock()
	if ok && w.now().Sub(cached.fetched) < w.ttl {
		return cached
	}
	brand, err := w.load(ctx, world)
	// A read that failed for a reason other than absence keeps whatever was
	// resolved before: the login page reads without a session, and a private
	// hub must not lose its identity for a TTL every time that happens.
	if err != nil && ok && !cached.empty() {
		brand = cached
	}
	brand.fetched = w.now()
	w.mu.Lock()
	w.byWorld[world] = brand
	w.mu.Unlock()
	return brand
}

// load reads the branding documents. A missing anchor is a real absence; any
// other failure (denied, transport) is returned so the caller can decide,
// but is cached either way so a flapping world cannot make every render retry.
func (w *WorldBrands) load(ctx context.Context, world string) (worldBrand, error) {
	var brand worldBrand
	raw, err := w.source.Raw(ctx, world, WorldBrandDoc)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return brand, nil
		}
		return brand, err
	}
	if f, ok := firstFence(raw.Body); ok && (f.Lang == "yaml" || f.Lang == "yml") {
		var declared brandingFile
		if yaml.Unmarshal([]byte(f.Content), &declared) == nil {
			brand.name = strings.TrimSpace(declared.Name)
			brand.tokens = declared.Theme
		}
	}
	// A stylesheet that could reach another origin is dropped whole, like an
	// unsafe token block: the world keeps its name and logo.
	if raw, err := w.source.Raw(ctx, world, WorldBrandCSS); err == nil {
		if f, ok := firstFence(raw.Body); ok && f.Lang == "css" && checkCSS(f.Content) == nil {
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
	if world == w.hub {
		if raw, err := w.source.Raw(ctx, world, WorldBrandFavicon); err == nil {
			brand.favicon = decodeLogo(raw.Body)
		}
	}
	return brand, nil
}
