package web

// The journal tail (plans/watch-realtime.md): when the focused document's new
// edition only adds to its end, as an append does, the pane grows in place
// instead of raising the banner, so a reader can watch a journal being written.

import (
	"context"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// liveTail is the focused canvas pane a stream may grow: the trail its links
// continue (focused on the pane), and the edition it shows once read.
type liveTail struct {
	trail trail
	shown *tailBase // nil until read
	lost  bool      // the shown edition could not be read; the banner stands in
}

// tailBase is what growing in place must leave alone (the title and the
// status chip) and the body HTML it extends, as the world rendered it.
type tailBase struct{ title, status, html string }

// tailOf reads the pane a stream may grow from its query: nil when it names
// none, or a pane that does not show this document.
func tailOf(c *echo.Context, doc liveDoc) *liveTail {
	if doc.editing || domain.IsListingPath(doc.path) {
		return nil
	}
	t, err := streamTrail(c)
	if err != nil {
		return nil
	}
	if p := t.Panes[t.Focus]; p.Kind != paneDoc || p.World != doc.world || p.Value != doc.path {
		return nil
	}
	return &liveTail{trail: t}
}

// render is a slice of a document's body as the focused pane renders it.
// Every pass rewrites one node at a time, so a slice renders as it does in
// the whole; only the preview anchor names differ, as on every render.
func (t *liveTail) render(fragment string, doc liveDoc) string {
	content, _ := rewriteLinks(fragment, doc.world, doc.path)
	return paneBody(content, t.trail, t.trail.Focus, false)
}

// know records the edition the pane shows, from the read at connect.
func (t *liveTail) know(shown domain.Document) {
	if t != nil && t.shown == nil {
		t.shown = &tailBase{title: shown.Title, status: shown.Status, html: shown.HTML}
	}
}

// base is the edition the pane shows: known from the read at connect, or
// read pinned when the document had already moved on (a tab that was hidden).
// A failed read is not retried: a pruned edition never returns, and the
// stream renews within a bearer's life anyway.
func (t *liveTail) base(ctx context.Context, reader documentReader, doc liveDoc) (tailBase, error) {
	if t.shown == nil {
		pinned, err := reader.Open(ctx, doc.world, doc.path+"/v"+doc.shown)
		if err != nil {
			t.lost = true
			return tailBase{}, err
		}
		t.know(pinned)
	}
	return *t.shown, nil
}

// grow adds what current appended to the pane, then swaps the pane's stream
// block for one at current's edition, ending this stream: a reconnect from the
// old edition would add the same text twice. false when current did more than
// append, or the shown edition cannot be read; the banner then asks for a reload.
func (h LiveHandler) grow(c *echo.Context, out *sseWriter, doc liveDoc, current domain.Document) bool {
	tail := doc.tail
	if tail == nil || tail.lost {
		return false
	}
	base, err := tail.base(c.Request().Context(), h.reader, doc)
	if err != nil {
		c.Logger().Warn("live tail could not read the shown edition", "world", doc.world, "path", doc.path, "v", doc.shown, "err", err)
		return false
	}
	added, ok := strings.CutPrefix(current.HTML, base.html)
	if !ok || strings.TrimSpace(added) == "" || current.Title != base.title || current.Status != base.status {
		return false
	}
	out.send("tail", tail.render(added, doc))
	doc.shown = current.Version
	out.send("rearm", fragment(c, "live-edition", paneVM{LiveURL: liveEditionURL(doc), FocusURL: trailURL(tail.trail)}))
	return true
}
