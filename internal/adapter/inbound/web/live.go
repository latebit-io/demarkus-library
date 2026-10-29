package web

// The live-edition banner (plans/watch-realtime.md, phase 2). The focused
// document pane holds one stream; when a change hint names its document, the
// stream re-reads it as this reader and fills the pane's banner if the edition
// moved on. Hints never reach the browser, only what the reader's read shows.

import (
	"cmp"
	"context"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// LiveEditionPath is the stream endpoint. A stream lasts as long as the reader
// looks at the document, so it is LongLived.
const LiveEditionPath = "/live"

// liveRegions places the banner text in the pane's live-edition block.
var liveRegions = sseRegions{"edition": {"find .edition-text", "innerHTML"}}

// documentReader is the one read the stream makes.
type documentReader interface {
	Read(ctx context.Context, world, path string) (domain.Document, error)
}

// LiveHandler serves the live-edition stream.
type LiveHandler struct {
	reader  documentReader
	watcher port.DocumentWatcher // nil = no change feed; the panes carry no stream
}

// liveDoc is the document a stream watches and the edition its pane rendered.
type liveDoc struct {
	world, path, shown string
}

// liveEditionURL is the stream a focused pane connects to.
func liveEditionURL(doc liveDoc) string {
	return LiveEditionPath + "?" + url.Values{"world": {doc.world}, "path": {doc.path}, "v": {doc.shown}}.Encode()
}

// liveWatchable reports a shown edition that can move on: not a listing, and
// not a pinned edition, which never changes.
func liveWatchable(doc liveDoc) bool {
	return doc.world != "" && doc.shown != "" && strings.HasPrefix(doc.path, "/") &&
		!domain.IsListingPath(doc.path) && !domain.IsVersionPath(doc.path)
}

// LiveEdition streams the banner for one document until the reader leaves or
// the document is gone.
func (h LiveHandler) LiveEdition(c *echo.Context) error {
	if crossSiteStream(c) {
		return echo.NewHTTPError(http.StatusForbidden, "cross-site stream rejected")
	}
	if h.watcher == nil {
		return echo.NewHTTPError(http.StatusNotFound, "live editions are off")
	}
	doc := liveDoc{world: c.QueryParam("world"), path: c.QueryParam("path"), shown: c.QueryParam("v")}
	if !liveWatchable(doc) {
		return echo.NewHTTPError(http.StatusBadRequest, "not a document whose edition can change")
	}
	// Subscribe before the first read, so a write between them still signals.
	changed := h.watcher.WatchDocument(c.Request().Context(), doc.world, doc.path)
	out, err := openSSE(c, liveRegions)
	if err != nil {
		return err
	}
	defer out.close()
	// The first read catches an edition published while the stream was down.
	if !h.report(c, out, doc) {
		return nil
	}
	for range changed {
		if !h.report(c, out, doc) {
			return nil
		}
	}
	return nil
}

// report re-reads the document and fills the banner when its edition moved
// past the one shown. false ends the stream: the reader left or the document
// is gone.
func (h LiveHandler) report(c *echo.Context, out *sseWriter, doc liveDoc) bool {
	ctx := c.Request().Context()
	current, err := h.reader.Read(ctx, doc.world, doc.path)
	switch {
	case ctx.Err() != nil:
		return false
	case errors.Is(err, domain.ErrNotFound): // archived unwraps to not found
		out.send("edition", html.EscapeString(goneNote(err)))
		out.finish()
		return false
	case err != nil:
		// Transient (timeout, dropped connection): the next hint re-reads.
		c.Logger().Warn("live edition re-read failed", "world", doc.world, "path", doc.path, "err", err)
	case current.Version != doc.shown:
		out.send("edition", html.EscapeString(editionNote(current)))
	}
	return true
}

// editionNote names the new edition and who wrote it: the verified person
// when the surface stamped one, else the writing software.
func editionNote(doc domain.Document) string {
	note := "Edition " + doc.Version + " published"
	if by := cmp.Or(doc.User, doc.Agent); by != "" {
		note += " by " + by
	}
	return note
}

func goneNote(err error) string {
	if errors.Is(err, domain.ErrArchived) {
		return "Archived since you opened it"
	}
	return "Removed since you opened it"
}
