package web

// The stale-answer note (plans/watch-realtime.md, next 3). An expanded
// librarian pane holds one stream over the documents its answers were read
// from, and names each one that has moved past the edition the librarian read.

import (
	"context"
	"errors"
	"html"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/labstack/echo/v5"
	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus-library/internal/core/port"
)

// LiveSourcesPath is the stale-answer stream; LongLived like the edition one.
const LiveSourcesPath = "/live/sources"

// sourceHistory is the librarian's transcript, where each answer keeps the
// sources it read and their editions.
type sourceHistory interface {
	History(conversation string) []domain.LibrarianExchange
}

// sourceReader re-reads a source from cache: a hint evicts the entry first,
// so only a changed source costs a world read.
type sourceReader interface {
	OpenCached(ctx context.Context, world, path string) (domain.Document, error)
}

// readSource is one source an answer was read from, and whether it has moved
// on since.
type readSource struct {
	domain.LibrarianSource
	stale bool
}

// LiveSources streams the note for the reader's conversation until the reader
// leaves or every source has moved on.
func (h LiveHandler) LiveSources(c *echo.Context) error {
	if crossSiteStream(c) {
		return echo.NewHTTPError(http.StatusForbidden, "cross-site stream rejected")
	}
	if h.watcher == nil || h.history == nil {
		return echo.NewHTTPError(http.StatusNotFound, "live sources are off")
	}
	sources := answerSources(h.history.History(conversationKey(c)))
	if len(sources) == 0 {
		return echo.NewHTTPError(http.StatusNotFound, "no sources to watch")
	}
	ctx := c.Request().Context()
	// Subscribe before the first reads, so a write between them still signals.
	// The watch closes with ctx, at renewal too (live_renew.go).
	changed := watchSources(ctx, h.watcher, sources)
	out, err := openSSE(c, liveRegions)
	if err != nil {
		return err
	}
	defer out.close()
	stale, announced := 0, 0
	// check re-reads source i and reports whether the stream goes on.
	check := func(i int, first bool) bool {
		turned, refused := h.checkSource(c, &sources[i])
		if refused {
			endRefused(out, first)
			return false
		}
		if turned {
			stale++
		}
		return true
	}
	// announce names the moved sources when another joined them, and ends
	// the stream once every one has.
	announce := func() bool {
		if stale == announced {
			return true
		}
		announced = stale
		out.send("edition", html.EscapeString(staleNote(sources)))
		if stale == len(sources) {
			out.finish()
			return false
		}
		return true
	}
	// The first reads catch a source that moved while no stream was open.
	for i := range sources {
		if !check(i, true) {
			return nil
		}
	}
	if !announce() {
		return nil
	}
	for i := range changed {
		if !check(i, false) || !announce() {
			return nil
		}
	}
	return nil
}

// checkSource re-reads src, marking it stale when its edition moved on or it
// is gone: turned reports that it just did, refused that the world refused
// this reader.
func (h LiveHandler) checkSource(c *echo.Context, src *readSource) (turned, refused bool) {
	if src.stale {
		return false, false
	}
	ctx := c.Request().Context()
	current, err := h.sources.OpenCached(ctx, src.World, src.Path)
	switch {
	case ctx.Err() != nil:
		return false, false
	case errors.Is(err, domain.ErrUnauthorized):
		return false, true
	case errors.Is(err, domain.ErrNotFound): // archived unwraps to not found
		src.stale = true
	case err != nil:
		// Transient (timeout, dropped connection): the next hint re-reads.
		c.Logger().Warn("live source re-read failed", "world", src.World, "path", src.Path, "err", err)
	case current.Version != src.Version:
		src.stale = true
	}
	return src.stale, false
}

// maxLiveSources bounds one stream's watches and first re-reads, which every
// settled answer reconnects, however long the conversation runs.
const maxLiveSources = 32

// answerSources lists the conversation's sources once each, newest answer
// first and at most maxLiveSources, at the edition the latest answer read; a
// source of unknown edition cannot go stale.
func answerSources(exchanges []domain.LibrarianExchange) []readSource {
	seen := map[domain.Ref]bool{}
	var sources []readSource
	for _, ex := range slices.Backward(exchanges) {
		for _, src := range ex.Sources {
			if src.Version == "" || seen[src.Ref] {
				continue
			}
			seen[src.Ref] = true
			sources = append(sources, readSource{LibrarianSource: src})
			if len(sources) == maxLiveSources {
				return sources
			}
		}
	}
	return sources
}

// watchSources merges every source's change signals into one channel of
// source indexes, closed once every watcher has ended (with ctx).
func watchSources(ctx context.Context, watcher port.DocumentWatcher, sources []readSource) <-chan int {
	changed := make(chan int)
	var watching sync.WaitGroup
	for i, src := range sources {
		signals := watcher.WatchDocument(ctx, src.World, src.Path)
		watching.Go(func() {
			for range signals {
				select {
				case changed <- i:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	go func() {
		watching.Wait()
		close(changed)
	}()
	return changed
}

// staleNote names the sources that moved on.
func staleNote(sources []readSource) string {
	var titles []string
	for _, src := range sources {
		if src.stale {
			titles = append(titles, src.Title)
		}
	}
	return "Changed since the librarian read them: " + strings.Join(titles, ", ")
}
