package web

// What each pane kind is, by its address alone, in one table: a new bare kind
// is an entry here plus its builder in the canvas's pane loop (ADR 0012). The
// chunk grammar of kinds with a world stays in trail.go.

// paneKind describes one kind of pane.
type paneKind struct {
	// bare kinds have a single-segment chunk naming no world, so a shared
	// trail shows each reader their own pane.
	bare bool
	// pulse marks a kind whose bare form lists recent activity, the pane
	// /live/pulse streams to.
	pulse bool
	// label is the palette's recent row: the pane's title and where it is.
	label func(p paneAddr, terms Terms) (title, loc string)
	// about names the pane in the librarian's context block; nil for a
	// document, which is named by its address and cached title.
	about func(p paneAddr, terms Terms) string
}

var paneKinds = map[string]paneKind{
	paneDoc: {label: docPaneLabel},
	paneGraph: {
		label: func(p paneAddr, terms Terms) (string, string) {
			title, loc := docPaneLabel(p, terms)
			return "graph: " + title, loc
		},
		about: func(p paneAddr, _ Terms) string { return "graph of mark://" + p.World + p.Value },
	},
	paneTag: {
		label: func(p paneAddr, _ Terms) (string, string) { return "#" + p.Value, p.World },
		about: func(p paneAddr, _ Terms) string { return "tag page #" + p.Value + " in world " + p.World },
	},
	paneFloor: {
		bare: true, pulse: true,
		label: func(p paneAddr, terms Terms) (string, string) {
			if p.World == "" {
				return terms.UniverseLower(), ""
			}
			return p.World + " — map", p.World
		},
		about: func(p paneAddr, terms Terms) string {
			if p.World == "" {
				return "the " + terms.UniverseLower() + " floor"
			}
			return "map of world " + p.World
		},
	},
	paneLibrarian: {
		bare:  true,
		label: func(paneAddr, Terms) (string, string) { return "librarian", "" },
		about: func(paneAddr, Terms) string { return "this librarian conversation" },
	},
	paneActivity: {
		bare: true, pulse: true,
		label: func(paneAddr, Terms) (string, string) { return activityLabel, "" },
		about: func(paneAddr, Terms) string { return "the recent activity list" },
	},
}

func docPaneLabel(p paneAddr, _ Terms) (title, loc string) {
	return docName(p.Value), p.World + p.Value
}
