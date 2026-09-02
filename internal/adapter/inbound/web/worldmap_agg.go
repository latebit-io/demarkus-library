package web

import (
	"maps"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
)

// World-map aggregation (plans/world-map-aggregation.md): the map starts at a
// budget of visible nodes and grows on demand. Groups are the world's own
// directory tree, so the scheme needs no semantics from the data; a group
// past the chunk size shows its top members by rank plus a "more" aggregate
// that pages; the rest state auto-expands the smallest groups until the
// budget is spent. Open state is the overlay fragment's `open` query param.
const (
	wmChunk       = 40      // members an expanded group shows per page
	wmAutoVisible = 50      // rest-state budget of visible items
	wmAggPad      = 14      // spacing between sibling footprints on a ring
	wmFoldMax     = 2       // a subdirectory this small dissolves into its parent
	wmRingMax     = 24      // leaf groups up to this size ring their anchor; bigger ones spiral
	wmPageMax     = 1 << 16 // reader-supplied page numbers clamp here (keeps page*chunk in range)
)

// wmGroup is a directory in the world's path tree.
type wmGroup struct {
	key  string // "" for the root, else "genres" or "a/b"
	name string
	list string            // listing path, "/genres/"
	docs []domain.FloorDoc // direct members
	subs []*wmGroup        // subdirectories, by name
	all  []domain.FloorDoc // every document beneath, recursively
}

// wmBuildTree folds the flat catalog into its directory tree.
func wmBuildTree(docs []domain.FloorDoc) *wmGroup {
	root := &wmGroup{list: "/"}
	byKey := map[string]*wmGroup{"": root}
	for _, d := range docs {
		segs := strings.Split(strings.TrimPrefix(d.Path, "/"), "/")
		g := root
		root.all = append(root.all, d)
		for i, seg := range segs[:len(segs)-1] {
			key := strings.Join(segs[:i+1], "/")
			child, ok := byKey[key]
			if !ok {
				child = &wmGroup{key: key, name: seg, list: "/" + key + "/"}
				byKey[key] = child
				g.subs = append(g.subs, child)
			}
			child.all = append(child.all, d)
			g = child
		}
		g.docs = append(g.docs, d)
	}
	wmFold(root)
	return root
}

// wmFold dissolves trivial subdirectories into their parent, bottom up: a
// world that keeps one document per folder (works/<artist>/<album>.md) would
// otherwise render each as a group of one and its parent as a ring of rings.
func wmFold(g *wmGroup) {
	kept := g.subs[:0]
	for _, s := range g.subs {
		wmFold(s)
		if len(s.all) <= wmFoldMax {
			g.docs = append(g.docs, s.all...)
			continue
		}
		kept = append(kept, s)
	}
	g.subs = kept
	sort.Slice(g.subs, func(i, j int) bool { return g.subs[i].name < g.subs[j].name })
}

// wmOpenSet is the parsed `open` param: keys to expand, keys forced closed
// (a "-" prefix, to undo an auto-expand) and pages ("key@n").
type wmOpenSet struct {
	open   map[string]bool
	closed map[string]bool
	pages  map[string]int
}

func wmParseOpen(keys []string) wmOpenSet {
	s := wmOpenSet{open: map[string]bool{}, closed: map[string]bool{}, pages: map[string]int{}}
	for _, k := range keys {
		k = strings.TrimSpace(k)
		switch {
		case k == "":
		case strings.HasPrefix(k, "-"):
			s.closed[k[1:]] = true
		default:
			if key, n, ok := strings.Cut(k, "@"); ok {
				if p, err := strconv.Atoi(n); err == nil && p > 1 {
					s.pages[key] = min(p, wmPageMax)
				}
				k = key
			}
			s.open[k] = true
		}
	}
	return s
}

// keys serialises the set back to the canonical, sorted `open` value.
func (s wmOpenSet) keys() []string {
	var out []string
	for k := range s.open {
		if p := s.pages[k]; p > 1 {
			out = append(out, k+"@"+strconv.Itoa(p))
		} else {
			out = append(out, k)
		}
	}
	for k := range s.closed {
		out = append(out, "-"+k)
	}
	sort.Strings(out)
	return out
}

// with returns the keys after one user action on a group.
func (s wmOpenSet) with(key string, action wmOpenAction) []string {
	n := wmOpenSet{open: maps.Clone(s.open), closed: maps.Clone(s.closed), pages: maps.Clone(s.pages)}
	switch action {
	case wmOpenExpand:
		n.open[key] = true
		delete(n.closed, key)
	case wmOpenCollapse:
		delete(n.open, key)
		delete(n.pages, key)
		n.closed[key] = true
	case wmOpenMore:
		n.open[key] = true
		delete(n.closed, key)
		n.pages[key] = min(max(n.pages[key], 1)+1, wmPageMax)
	}
	return n.keys()
}

type wmOpenAction int

const (
	wmOpenExpand wmOpenAction = iota
	wmOpenCollapse
	wmOpenMore
)

// wmItemKind is what a visible node stands for.
type wmItemKind int

const (
	wmItemDoc    wmItemKind = iota // one document
	wmItemGroup                    // a collapsed directory
	wmItemMore                     // the unshown tail of a chunked directory
	wmItemAnchor                   // the centre of an expanded directory
	wmItemRoot                     // the world itself (drawn as nothing)
)

// wmItem is one visible node with its subtree, footprint and placement.
type wmItem struct {
	id       string
	kind     wmItemKind
	doc      domain.FloorDoc
	group    *wmGroup
	count    int // documents represented (group: all beneath; more: unshown)
	children []*wmItem
	hub      bool    // member linked to most of its siblings: sits at the centre
	ringed   bool    // member of a ring group: labeled at rest, labels fan outward
	pack     []wmPt  // packed child offsets for a mixed group (unstretched)
	foot     float64 // footprint radius, vertical units
	x, y, r  int     // placed centre and visual radius
}

// wmPt is an unstretched offset from a group's centre.
type wmPt struct{ x, y float64 }

// wmPack places children of mixed footprint by greedy spiral packing:
// largest first, each walking an Archimedean spiral from the centre to the
// first spot that overlaps nothing placed. reserve keeps the centre clear
// for the anchor. Deterministic, compact, no empty middle.
func wmPack(children []*wmItem, reserve float64) (pos []wmPt, foot float64) {
	order := make([]int, len(children))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return children[order[a]].foot > children[order[b]].foot })
	type disc struct {
		p wmPt
		r float64
	}
	var placed []disc
	if reserve > 0 {
		placed = append(placed, disc{wmPt{0, 0}, reserve})
	}
	pos = make([]wmPt, len(children))
	const turn = wmPitch / (4 * math.Pi) // radial growth per radian: half a pitch per turn
	for _, i := range order {
		f := children[i].foot
		for t := 0.0; t < 4000; t += 0.2 {
			p := wmPt{turn * t * math.Cos(t), turn * t * math.Sin(t)}
			fits := true
			for _, q := range placed {
				if math.Hypot(p.x-q.p.x, p.y-q.p.y) < f+q.r+wmAggPad {
					fits = false
					break
				}
			}
			if fits {
				pos[i] = p
				placed = append(placed, disc{p, f})
				break
			}
		}
	}
	for i, p := range pos {
		foot = math.Max(foot, math.Hypot(p.x, p.y)+children[i].foot)
	}
	return pos, foot + 10
}

// wmVisible builds the visible item tree for the open set plus the
// rest-state auto-expansion, and returns it with the map from document path
// to the item that represents it (itself, its "more" chunk, or its nearest
// collapsed ancestor).
func wmVisible(root *wmGroup, open wmOpenSet, rank, degree map[string]int, chunk int) (tree *wmItem, owner map[string]*wmItem) {
	expanded := map[string]bool{"": true}
	for k := range open.open {
		if !open.closed[k] {
			expanded[k] = true
		}
	}
	// A page never exceeds the last one a group has, so page*chunk stays small.
	page := func(g *wmGroup) int {
		last := max((len(g.docs)+chunk-1)/chunk, 1)
		return min(max(open.pages[g.key], 1), last)
	}
	// Items an expanded group contributes: anchor, subgroups, shown docs, more.
	cost := func(g *wmGroup) int {
		shown := min(len(g.docs), page(g)*chunk)
		n := 1 + len(g.subs) + shown
		if shown < len(g.docs) {
			n++
		}
		return n
	}
	// Auto-expand: smallest collapsed group whose parent is expanded, while
	// the budget holds. Deterministic: ties break by key.
	for {
		visible := 0
		var candidates []*wmGroup
		var walk func(g *wmGroup)
		walk = func(g *wmGroup) {
			visible += cost(g) - 1 // the anchor of the root is not drawn, groups count once
			for _, s := range g.subs {
				if expanded[s.key] {
					walk(s)
				} else if !open.closed[s.key] {
					candidates = append(candidates, s)
				}
			}
		}
		walk(root)
		visible++ // root anchor slot, for a stable budget
		sort.Slice(candidates, func(i, j int) bool {
			if len(candidates[i].all) != len(candidates[j].all) {
				return len(candidates[i].all) < len(candidates[j].all)
			}
			return candidates[i].key < candidates[j].key
		})
		grew := false
		for _, c := range candidates {
			if visible+cost(c)-1 <= wmAutoVisible {
				expanded[c.key] = true
				grew = true
				break
			}
		}
		if !grew {
			break
		}
	}

	owner = map[string]*wmItem{}
	var build func(g *wmGroup, kind wmItemKind) *wmItem
	build = func(g *wmGroup, kind wmItemKind) *wmItem {
		it := &wmItem{id: "g:" + g.key, kind: kind, group: g, count: len(g.all)}
		if kind == wmItemGroup {
			for _, d := range g.all {
				owner[d.Path] = it
			}
			return it
		}
		for _, s := range g.subs {
			if expanded[s.key] {
				it.children = append(it.children, build(s, wmItemAnchor))
			} else {
				it.children = append(it.children, build(s, wmItemGroup))
			}
		}
		docs := append([]domain.FloorDoc(nil), g.docs...)
		sort.SliceStable(docs, func(i, j int) bool { return rank[docs[i].Path] < rank[docs[j].Path] })
		shown := min(len(docs), page(g)*chunk)
		for i, d := range docs[:shown] {
			c := &wmItem{id: d.Path, kind: wmItemDoc, doc: d, count: 1}
			owner[d.Path] = c
			// The top-ranked member is the group's hub when it links to at
			// least half its siblings; it goes first so it lands at the centre.
			if i == 0 && len(docs) > 2 && degree[d.Path] >= len(docs)/2 {
				c.hub = true
				it.children = append([]*wmItem{c}, it.children...)
				continue
			}
			it.children = append(it.children, c)
		}
		if shown < len(docs) {
			more := &wmItem{id: "m:" + g.key, kind: wmItemMore, group: g, count: len(docs) - shown}
			for _, d := range docs[shown:] {
				owner[d.Path] = more
			}
			it.children = append(it.children, more)
		}
		return it
	}
	return build(root, wmItemRoot), owner
}

// wmAggRadius sizes an aggregate by the square root of what it holds.
func wmAggRadius(count int) int {
	return int(math.Min(34, 8+3*math.Sqrt(float64(count))))
}

// wmMeasure computes footprints bottom up. A group of leaves is a sunflower;
// a group with expanded children is spiral-packed by footprint.
func wmMeasure(it *wmItem) float64 {
	switch it.kind {
	case wmItemDoc:
		it.r = 5 + int(it.doc.Importance*9)
		it.foot = wmPitch / 2
		return it.foot
	case wmItemGroup, wmItemMore:
		it.r = wmAggRadius(it.count)
		it.foot = math.Max(wmPitch/2, float64(it.r)+12)
		return it.foot
	case wmItemAnchor, wmItemRoot:
	}
	it.r = 7
	extra := 0.0
	simple := true
	for _, c := range it.children {
		f := wmMeasure(c)
		extra = math.Max(extra, f-wmPitch/2)
		if c.kind == wmItemAnchor {
			simple = false
		}
	}
	if len(it.children) == 0 {
		it.foot = wmPitch
		return it.foot
	}
	if simple {
		if n := wmRingCount(it); n > 0 {
			for _, c := range it.children {
				c.ringed = true
			}
			it.foot = wmRingRadius(n) + extra + wmPitch/2
			return it.foot
		}
		slots := len(it.children)
		if it.kind == wmItemAnchor {
			slots++ // index 0 is the anchor
		}
		it.foot = float64(wmSpiralRadius(slots)) + extra
		return it.foot
	}
	reserve := 0.0
	if it.kind == wmItemAnchor {
		reserve = float64(it.r) + 10
	}
	it.pack, it.foot = wmPack(it.children, reserve)
	return it.foot
}

// wmPlace positions an item tree top down around (cx, cy).
func wmPlace(it *wmItem, cx, cy int) {
	it.x, it.y = cx, cy
	if it.kind != wmItemAnchor && it.kind != wmItemRoot {
		return
	}
	simple := true
	for _, c := range it.children {
		if c.kind == wmItemAnchor {
			simple = false
		}
	}
	if simple {
		if n := wmRingCount(it); n > 0 {
			ry := wmRingRadius(n)
			rx := int(float64(ry) * wmTierRatio)
			j := 0
			for _, c := range it.children {
				if c.hub {
					wmPlace(c, cx, cy)
					continue
				}
				x, y := ellipseAt(cx, cy, j, n, rx, int(ry))
				j++
				wmPlace(c, x, y)
			}
			return
		}
		// Index 0 is the centre: the hub if there is one, else the anchor.
		next := 0
		if it.kind == wmItemAnchor {
			next = 1
		}
		for _, c := range it.children {
			if c.hub {
				wmPlace(c, cx, cy)
				next = max(next, 1)
				continue
			}
			x, y := spiralAt(cx, cy, next)
			next++
			wmPlace(c, x, y)
		}
		return
	}
	for i, c := range it.children {
		wmPlace(c, cx+int(it.pack[i].x*wmTierRatio), cy+int(it.pack[i].y))
	}
}

// wmRingCount is the number of members a leaf group rings around its centre,
// or 0 when the group is big enough to spiral. A hub member sits at the
// centre and is not on the ring.
func wmRingCount(it *wmItem) int {
	n := len(it.children)
	for _, c := range it.children {
		if c.hub {
			n--
		}
	}
	if len(it.children) > wmRingMax || n == 0 {
		return 0
	}
	return n
}

// wmRingRadius keeps neighbours about a pitch apart along the ring, with a
// floor that clears the anchor and a hub.
func wmRingRadius(n int) float64 {
	return math.Max(wmPitch*1.4, float64(n)*wmPitch/(2*math.Pi))
}

// ellipseAt places slot j of n on an ellipse of radii (rx, ry) around (cx, cy),
// starting at the top and going clockwise.
func ellipseAt(cx, cy, j, n, rx, ry int) (x, y int) {
	angle := 2*math.Pi*float64(j)/float64(n) - math.Pi/2
	return cx + int(float64(rx)*math.Cos(angle)), cy + int(float64(ry)*math.Sin(angle))
}

// wmFlatten lists every placed item in draw order.
func wmFlatten(it *wmItem, out []*wmItem) []*wmItem {
	if it.kind != wmItemRoot {
		out = append(out, it)
	}
	for _, c := range it.children {
		out = wmFlatten(c, out)
	}
	return out
}

// wmRolled is one drawn edge between visible items, with the number of
// document edges it stands for.
type wmRolled struct {
	from, to *wmItem
	rel      string
	count    int
}

// wmRollup resolves each document edge to its visible endpoints, drops the
// ones internal to a single item, and merges the rest by endpoint pair.
func wmRollup(edges []domain.Edge, owner map[string]*wmItem) []*wmRolled {
	at := map[[2]string]int{}
	var out []*wmRolled
	for _, e := range edges {
		f, okF := owner[e.From.Path]
		t, okT := owner[e.To.Path]
		if !okF || !okT || f == t {
			continue
		}
		k := [2]string{f.id, t.id}
		if i, ok := at[k]; ok {
			out[i].count++
			if out[i].rel == "" {
				out[i].rel = e.Rel
			}
			continue
		}
		at[k] = len(out)
		out = append(out, &wmRolled{from: f, to: t, rel: e.Rel, count: 1})
	}
	return out
}
