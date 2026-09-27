package domain

import (
	"sort"
	"strings"
)

// ClusterByDir groups catalog docs by their top-level path segment (the
// directory), preserving the catalog's importance order within each cluster.
// The root cluster (docs directly under "/") sorts first, then directories
// alphabetically. Each cluster keeps its top perCluster docs; the remainder
// becomes the More count behind the dir's listing pane.
func ClusterByDir(docs []FloorDoc, perCluster int) []WorldCluster {
	order := []string{}
	byDir := map[string][]FloorDoc{}
	for _, d := range docs {
		dir := TopDir(d.Path)
		if _, seen := byDir[dir]; !seen {
			order = append(order, dir)
		}
		byDir[dir] = append(byDir[dir], d)
	}
	sort.SliceStable(order, func(i, j int) bool {
		if (order[i] == "") != (order[j] == "") {
			return order[i] == "" // root cluster first
		}
		return order[i] < order[j]
	})

	out := make([]WorldCluster, 0, len(order))
	for _, dir := range order {
		group := byDir[dir]
		cl := WorldCluster{Dir: dir, ListPath: "/"}
		if dir != "" {
			cl.ListPath = "/" + dir + "/"
		}
		if len(group) > perCluster {
			cl.Docs = group[:perCluster]
			cl.More = len(group) - perCluster
		} else {
			cl.Docs = group
		}
		out = append(out, cl)
	}
	return out
}

// TopDir returns a path's top-level directory segment — "plans" for
// "/plans/reading-room.md", "" for a root-level doc like "/index.md".
func TopDir(path string) string {
	p := strings.TrimPrefix(path, "/")
	if dir, _, found := strings.Cut(p, "/"); found {
		return dir
	}
	return ""
}
