package domain

import (
	"reflect"
	"testing"
)

func TestClusterByDir(t *testing.T) {
	t.Parallel()

	// Importance order, as the catalog returns it.
	docs := []FloorDoc{
		{Path: "/index.md", Importance: 0.95},
		{Path: "/adr/0001.md", Importance: 0.9},
		{Path: "/plans/a.md", Importance: 0.8},
		{Path: "/plans/b.md", Importance: 0.7},
		{Path: "/plans/c.md", Importance: 0.6},
	}
	type want struct {
		dir, list string
		paths     []string
		more      int
	}
	tests := []struct {
		name       string
		perCluster int
		want       []want
	}{
		{name: "cap spills into More", perCluster: 2, want: []want{
			{dir: "", list: "/", paths: []string{"/index.md"}},
			{dir: "adr", list: "/adr/", paths: []string{"/adr/0001.md"}},
			{dir: "plans", list: "/plans/", paths: []string{"/plans/a.md", "/plans/b.md"}, more: 1},
		}},
		{name: "zero cap counts every doc in More", perCluster: 0, want: []want{
			{dir: "", list: "/", more: 1},
			{dir: "adr", list: "/adr/", more: 1},
			{dir: "plans", list: "/plans/", more: 3},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got []want
			for _, cl := range ClusterByDir(docs, tt.perCluster) {
				w := want{dir: cl.Dir, list: cl.ListPath, more: cl.More}
				for _, d := range cl.Docs {
					w.paths = append(w.paths, d.Path)
				}
				got = append(got, w)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ClusterByDir(%d) = %+v, want %+v", tt.perCluster, got, tt.want)
			}
		})
	}
}

func TestTopDir(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, path, want string }{
		{name: "root doc", path: "/index.md", want: ""},
		{name: "one level", path: "/plans/reading.md", want: "plans"},
		{name: "nested", path: "/a/b/c.md", want: "a"},
		{name: "missing leading slash", path: "index.md", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := TopDir(tt.path); got != tt.want {
				t.Errorf("TopDir(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
