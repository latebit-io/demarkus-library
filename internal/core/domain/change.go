package domain

import "time"

// Change is a hint that World changed (demarkus WATCH): the document at Path
// was published, appended or archived. Resync means changes may have been
// missed, so every view derived from World is stale; Path is then empty.
type Change struct {
	World   string
	Path    string
	Op      string // OpPublish, OpAppend or OpArchive; empty on a resync
	Version int    // the edition the write made
	Agent   string // the writing software, as the write stamped it
	User    string // the verified person behind it, when the surface stamped one
	Resync  bool
	// Derived marks a document generated from other writes, such as the graph
	// export or a world-graph checkpoint: it still evicts, but is no one's activity.
	Derived bool
}

// Activity reports whether the change is a write a reader would call activity.
func (c Change) Activity() bool { return !c.Resync && !c.Derived }

// The writes a Change names; the watch adapter maps the protocol's ops onto them.
const (
	OpPublish = "publish"
	OpAppend  = "append"
	OpArchive = "archive"
)

// Pulse is one write as the floor's recent activity lists it: the change and
// when the room heard of it.
type Pulse struct {
	Change
	At time.Time
}
