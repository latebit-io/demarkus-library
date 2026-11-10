package web

import "context"

// worldIdentity is what a world declares for itself, its documents over its
// manifest entry, with no room fallback: a page listing several worlds must
// not give each one the room's name.
type worldIdentity struct {
	manifest map[string]WorldBranding
	brands   *WorldBrands // nil ⇒ manifest only
}

func (w worldIdentity) own(ctx context.Context, world string) WorldBranding {
	own := w.manifest[world]
	if w.brands != nil {
		if iw, ok := w.brands.For(ctx, world); ok {
			own = own.over(iw)
		}
	}
	return own
}
