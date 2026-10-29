package domain

// Change is a hint that World changed (demarkus WATCH): the document at Path
// was published, appended or archived. Resync means changes may have been
// missed, so every view derived from World is stale; Path is then empty.
type Change struct {
	World  string
	Path   string
	Resync bool
}
