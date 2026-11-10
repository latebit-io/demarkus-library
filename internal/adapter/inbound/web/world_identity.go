package web

import "context"

// worldIdentity is what a world declares for itself, its documents over its
// manifest entry, with no room fallback: a page listing several worlds must
// not give each one the room's name.
type worldIdentity struct {
	manifest map[string]WorldBranding
	brands   *WorldBrands // nil ⇒ manifest only (its methods are nil-safe)
}

func (w worldIdentity) own(ctx context.Context, world string) WorldBranding {
	iw, ok := w.brands.For(ctx, world)
	return w.layered(world, iw, ok)
}

// held is own without a read: the manifest under whatever brand is cached.
func (w worldIdentity) held(world string) WorldBranding {
	iw, ok := w.brands.Cached(world)
	return w.layered(world, iw, ok)
}

// layered lays the world's documents, when it declares any, over its manifest entry.
func (w worldIdentity) layered(world string, iw WorldBranding, declared bool) WorldBranding {
	if !declared {
		return w.manifest[world]
	}
	return w.manifest[world].over(iw)
}
