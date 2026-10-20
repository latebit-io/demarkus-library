package web

// markURL is a document's canonical protocol address: what the pane head
// copies and what readers paste into mark_fetch.
func markURL(world, path string) string { return "mark://" + world + path }
