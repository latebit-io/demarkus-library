package web

import (
	"cmp"
	"fmt"
	"html/template"
	"math"
	"slices"
	"strings"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// The universe's world cards (ADR 0006 §5): each world is a door showing its
// catalog at a glance. Everything comes from the floor's catalog sample, so a
// card costs no read beyond the world's own branding.
const (
	cardSections = 6  // section chips per card, largest first
	cardFeatured = 3  // featured documents per card, by importance
	skyDots      = 24 // documents drawn in a card's constellation
	skyDotScale  = 1.6
)

// floorVM is the universe pane's body: a summary, the worlds/map switch, and
// either the world cards or the map (or the empty state).
type floorVM struct {
	Summary string // "3 worlds · 412 docs · 1 portal"; "" beside the map
	Toggle  template.HTML
	Cards   []worldCardVM
	Map     template.HTML
}

// worldCardVM is one world's door. The template lays the world's own
// branding (name, logo, accent) over it.
type worldCardVM struct {
	World        string // the world's name, which is also its address
	Title        string // its root index.md title; "" when uncatalogued
	URL          string // the door: the world's stacks on the trail
	Portal       bool
	Unreadable   bool
	Sky          template.HTML // "" without documents
	Stats        string
	Sections     []cardLink
	MoreSections int
	Featured     []cardLink
}

// cardLink is one link inside a card; Hint is its tooltip.
type cardLink struct {
	Label, URL, Hint string
}

// floorSummary counts the universe for the pane's summary line.
func floorSummary(floor domain.Floor) string {
	worlds, portals, unreadable, docs, truncated := 0, 0, 0, 0, false
	for _, fw := range floor.Worlds {
		switch {
		case fw.Portal:
			portals++
			continue
		case fw.Err:
			unreadable++
		}
		worlds++
		docs += len(contentDocs(fw.Docs))
		truncated = truncated || fw.Truncated
	}
	// The total leaves out unreadable worlds, so they are named beside it.
	parts := []string{plural(worlds, "world"), sampledCount(docs, "doc", "docs", truncated)}
	if unreadable > 0 {
		parts = append(parts, fmt.Sprintf("%d unreadable", unreadable))
	}
	if portals > 0 {
		parts = append(parts, plural(portals, "portal"))
	}
	return strings.Join(parts, " · ")
}

// worldCards builds one door per world, in the floor's order.
func worldCards(floor domain.Floor, t trail, idx int) []worldCardVM {
	linksOut, linksIn := worldLinks(floor.Edges)
	cards := make([]worldCardVM, 0, len(floor.Worlds))
	for _, fw := range floor.Worlds {
		open := func(path string) string {
			return trailURL(trailAfterClick(t, idx, paneAddr{Kind: paneDoc, World: fw.World.Name, Value: path}))
		}
		card := worldCardVM{World: fw.World.Name, URL: open("/"), Portal: fw.Portal, Unreadable: fw.Err}
		var stats []string
		if !fw.Portal && !fw.Err {
			docs := contentDocs(fw.Docs)
			card.Title = indexTitle(docs)
			card.Sky = worldSky(docs)
			card.Sections, card.MoreSections = worldSections(docs, open)
			card.Featured = featuredDocs(docs, open)
			stats = append(stats, sampledCount(len(docs), "doc", "docs", fw.Truncated))
			if n := acceptedCount(docs); n > 0 {
				stats = append(stats, sampledCount(n, "accepted", "accepted", fw.Truncated))
			}
			if n := len(card.Sections) + card.MoreSections; n > 0 {
				stats = append(stats, sampledCount(n, "section", "sections", fw.Truncated))
			}
		}
		if n := linksOut[fw.World.Name]; n > 0 {
			stats = append(stats, "links to "+plural(n, "world"))
		}
		if n := linksIn[fw.World.Name]; n > 0 {
			stats = append(stats, fmt.Sprintf("linked from %d", n))
		}
		card.Stats = strings.Join(stats, " · ")
		cards = append(cards, card)
	}
	return cards
}

// contentDocs drops documents under dot directories (a world's branding and
// the like): machinery, not what a reader comes for.
func contentDocs(docs []domain.FloorDoc) []domain.FloorDoc {
	out := make([]domain.FloorDoc, 0, len(docs))
	for _, d := range docs {
		if !strings.HasPrefix(domain.TopDir(d.Path), ".") {
			out = append(out, d)
		}
	}
	return out
}

// indexTitle is the world's root index.md title, the name its keepers gave
// its front door; "" when it has none beyond the file name.
func indexTitle(docs []domain.FloorDoc) string {
	for _, d := range docs {
		if d.Path == "/index.md" && d.Title != "index" {
			return d.Title
		}
	}
	return ""
}

func acceptedCount(docs []domain.FloorDoc) int {
	n := 0
	for _, d := range docs {
		if d.Status == "accepted" {
			n++
		}
	}
	return n
}

// sampledCount formats a count from the floor's catalog sample; truncated
// marks it a lower bound, since the rest of the catalog went unread.
func sampledCount(n int, one, many string, truncated bool) string {
	switch {
	case truncated:
		return fmt.Sprintf("%d+ %s", n, many)
	case n == 1:
		return "1 " + one
	default:
		return fmt.Sprintf("%d %s", n, many)
	}
}

// worldSections are the world's top-level directories, largest first, and
// how many more did not fit.
func worldSections(docs []domain.FloorDoc, open func(string) string) (shown []cardLink, more int) {
	dirs := slices.DeleteFunc(domain.ClusterByDir(docs, 0), func(c domain.WorldCluster) bool { return c.Dir == "" })
	// Stable, so equal sizes keep ClusterByDir's alphabetical order.
	slices.SortStableFunc(dirs, func(a, b domain.WorldCluster) int { return cmp.Compare(b.More, a.More) })
	fits := dirs[:min(len(dirs), cardSections)]
	shown = make([]cardLink, 0, len(fits))
	for _, c := range fits {
		shown = append(shown, cardLink{Label: c.Dir + "/", URL: open(c.ListPath), Hint: plural(c.More, "doc")})
	}
	return shown, len(dirs) - len(fits)
}

// featuredDocs are the world's most important documents; the root index is
// the card's own name, so it is not repeated.
func featuredDocs(docs []domain.FloorDoc, open func(string) string) []cardLink {
	var links []cardLink
	for _, d := range docs {
		if len(links) == cardFeatured {
			break
		}
		if d.Path == "/index.md" {
			continue
		}
		links = append(links, cardLink{Label: d.Title, URL: open(d.Path), Hint: d.Path})
	}
	return links
}

// worldLinks counts, per world, the distinct worlds it links to and is
// linked from.
func worldLinks(edges []domain.Edge) (out, in map[string]int) {
	type pair struct{ from, to string }
	seen := map[pair]bool{}
	out, in = map[string]int{}, map[string]int{}
	for _, e := range edges {
		p := pair{e.From.World, e.To.World}
		if p.from == p.to || seen[p] {
			continue
		}
		seen[p] = true
		out[p.from]++
		in[p.to]++
	}
	return out, in
}

// worldSky draws a world's documents as a constellation: the map's spiral and
// node radii at thumbnail scale, accepted documents lit. Every sky shares one
// viewBox, so a small world reads small.
func worldSky(docs []domain.FloorDoc) template.HTML {
	if len(docs) == 0 {
		return ""
	}
	ry := int(wmSpiralScale*math.Sqrt(skyDots-1) + skyDotScale*float64(wmDocRadius(1)))
	rx := int(float64(ry) * wmTierRatio)
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="world-sky" viewBox="%d %d %d %d" aria-hidden="true">`, -rx, -ry, 2*rx, 2*ry)
	for i, d := range docs[:min(len(docs), skyDots)] {
		x, y := spiralAt(0, 0, i)
		cls := "sky-dot"
		if d.Status == "accepted" {
			cls += " lit"
		}
		fmt.Fprintf(&b, `<circle class="%s" cx="%d" cy="%d" r="%.1f"/>`, cls, x, y, skyDotScale*float64(wmDocRadius(d.Importance)))
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String()) //nolint:gosec // numbers and fixed class names only
}
