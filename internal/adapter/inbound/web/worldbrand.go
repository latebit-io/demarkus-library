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
	Name      string            `yaml:"name"`
	Theme     map[string]string `yaml:"theme,omitempty"`
	Librarian *brandLibrarian   `yaml:"librarian,omitempty"`
}

// brandLibrarian is how a hub world presents the room's librarian: the name
// it answers to and house instructions for its voice and focus.
type brandLibrarian struct {
	Name         string `yaml:"name,omitempty"`
	Instructions string `yaml:"instructions,omitempty"`
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
	// librarian is honored for the hub only: the librarian serves the room.
	librarian brandLibrarian

	fetched time.Time
}

// empty reports whether the world brands its pages; a librarian block alone
// does not, since it names no page identity.
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
	Librarian  brandLibrarian
}

// WorldBrands resolves and caches in-world branding per world. Reads carry
// the requesting reader's identity; a resolved brand is then shared for the
// TTL, which is fine because branding is not confidential.
type WorldBrands struct {
	source rawReader
	hub    string // the world whose documents brand the room; "" ⇒ none
	// hubSource reads the hub without a bearer (the broker's public branding
	// route); nil, or a 404 on the anchor, means an older broker: use source.
	hubSource rawReader
	ttl       time.Duration
	now       func() time.Time

	mu      sync.Mutex
	byWorld map[string]worldBrand
	gen     uint64 // invalidations, so a load across one stores its result expired
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

// WithHubSource reads the hub's documents through a bearer-less source first,
// so a cold pod and the sign-in page resolve the room before anyone signs in.
func (w *WorldBrands) WithHubSource(source rawReader) *WorldBrands {
	w.hubSource = source
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
	resolved := WorldBranding{Name: brand.name, Accent: strings.TrimSpace(brand.tokens["accent"])}
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

// RoomLibrarian returns the librarian identity the hub world declares; the
// zero value without a hub or when the hub is silent.
func (w *WorldBrands) RoomLibrarian(ctx context.Context) brandLibrarian {
	if w == nil || w.hub == "" {
		return brandLibrarian{}
	}
	return w.get(ctx, w.hub).librarian
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
	desk := brandDesk{Name: brand.name, Tokens: brand.tokens, CSS: brand.rawCSS, Librarian: brand.librarian}
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
	w.gen++
	w.mu.Unlock()
}

// ApplyChange expires every cached brand when a change touches a branding
// document, or a resync may have: a hint spells its world as the feed does
// (host:port), not as the room asks, and brand writes are rare. Expired, not
// dropped, so a failed reload still falls back to the last good brand.
func (w *WorldBrands) ApplyChange(change domain.Change) {
	if !change.Resync && !strings.HasPrefix(change.Path, WorldBrandDir) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for world, brand := range w.byWorld {
		brand.fetched = time.Time{}
		w.byWorld[world] = brand
	}
	w.gen++
}

func (w *WorldBrands) get(ctx context.Context, world string) worldBrand {
	if w == nil || world == "" {
		return worldBrand{}
	}
	w.mu.Lock()
	cached, ok := w.byWorld[world]
	gen := w.gen
	w.mu.Unlock()
	if ok && w.now().Sub(cached.fetched) < w.ttl {
		return cached
	}
	brand, err := w.load(ctx, world)
	if err != nil {
		// A failed load keeps the last good brand for another TTL, so a
		// flapping hub does not lose its identity. With nothing good cached it
		// is no result at all: a refused or half-read brand must not pin the
		// stock room, or a name without its sheet, for a TTL (#90).
		if !ok || (cached.empty() && cached.librarian == brandLibrarian{}) {
			return worldBrand{}
		}
		brand = cached
	}
	brand.fetched = w.now()
	w.mu.Lock()
	// An invalidation since the load began may postdate what it read: keep
	// the result as the fallback, but expired, so the next request reloads.
	if w.gen != gen {
		brand.fetched = time.Time{}
	}
	w.byWorld[world] = brand
	w.mu.Unlock()
	return brand
}

// load reads the branding documents. A missing anchor is a real absence; any
// other failure (denied, transport) is returned so the caller can decide.
func (w *WorldBrands) load(ctx context.Context, world string) (worldBrand, error) {
	var brand worldBrand
	src, raw, err := w.anchor(ctx, world)
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
			if declared.Librarian != nil && world == w.hub {
				brand.librarian = brandLibrarian{
					Name:         strings.TrimSpace(declared.Librarian.Name),
					Instructions: strings.TrimSpace(declared.Librarian.Instructions),
				}
			}
		}
	}
	// A stylesheet that could reach another origin is dropped whole, like an
	// unsafe token block: the world keeps its name and logo.
	body, err := optional(ctx, src, world, WorldBrandCSS)
	if err != nil {
		return brand, err
	}
	if f, ok := firstFence(body); ok && f.Lang == "css" && checkCSS(f.Content) == nil {
		brand.rawCSS = f.Content
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
	if body, err = optional(ctx, src, world, WorldBrandLogo); err != nil {
		return brand, err
	} else if body != "" {
		brand.logo = decodeLogo(body)
	}
	if world == w.hub {
		if body, err = optional(ctx, src, world, WorldBrandFavicon); err != nil {
			return brand, err
		} else if body != "" {
			brand.favicon = decodeLogo(body)
		}
	}
	return brand, nil
}

// anchor reads branding.md and picks the source for the rest of the load: the
// hub's public route when it answers, else the reader's bearer (an older
// broker's 404, or a gate refusing the route). Deciding once here keeps a
// public 404 on an optional document a plain absence.
func (w *WorldBrands) anchor(ctx context.Context, world string) (rawReader, domain.RawDocument, error) {
	if world == w.hub && w.hubSource != nil {
		raw, err := w.hubSource.Raw(ctx, world, WorldBrandDoc)
		if !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrUnauthorized) {
			return w.hubSource, raw, err
		}
	}
	raw, err := w.source.Raw(ctx, world, WorldBrandDoc)
	return w.source, raw, err
}

// optional reads an asset document the anchor made possible: absence is an
// empty body, so a world with a name and no logo stays cheap, while any other
// failure surfaces so a transient error cannot cache a half brand.
func optional(ctx context.Context, src rawReader, world, path string) (string, error) {
	raw, err := src.Raw(ctx, world, path)
	switch {
	case err == nil:
		return raw.Body, nil
	case errors.Is(err, domain.ErrNotFound):
		return "", nil
	default:
		return "", err
	}
}
