package cluster

import (
	"reflect"
	"testing"

	"github.com/rytsh/bag/internal/graph"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// Adapted from Graphify's tests/test_cluster_exclude_hubs.py (Apache-2.0).
func TestExcludedHubKeepsPrivateLeaves(t *testing.T) {
	for _, selfLoop := range []bool{false, true} {
		g := graph.New()
		id := func(name string) string { return ids.MakeID("test", name) }
		add := func(name string) { g.AddNode(&model.Node{ID: id(name), Label: name}) }
		edge := func(u, v string) { g.SetEdge(&model.Edge{Source: id(u), Target: id(v), Weight: 1}) }
		for _, name := range []string{"hub", "a1", "a2", "a3", "a4", "b1", "b2", "b3", "b4", "l1", "l2", "l3", "lonely", "solo"} {
			add(name)
		}
		for _, group := range [][]string{{"a1", "a2", "a3", "a4"}, {"b1", "b2", "b3", "b4"}} {
			for i, u := range group {
				for _, v := range group[i+1:] {
					edge(u, v)
				}
			}
		}
		for _, name := range []string{"a1", "a2", "a3", "a4", "b1", "l1", "l2", "l3"} {
			edge("hub", name)
		}
		edge("solo", "solo")
		if selfLoop {
			edge("l1", "l1")
		}
		c := Cluster(g, Options{ExcludeHubsPercent: 90})
		nc := c.NodeCommunity()
		if len(nc) != g.NumNodes() {
			t.Fatalf("lost nodes: %v", c)
		}
		for _, name := range []string{"l1", "l2", "l3"} {
			if nc[id(name)] != nc[id("hub")] {
				t.Fatalf("leaf %s severed from hub (selfLoop=%v): %v", name, selfLoop, c)
			}
		}
		for _, name := range []string{"lonely", "solo"} {
			if len(c[nc[id(name)]]) != 1 {
				t.Fatalf("true isolate %s was attached: %v", name, c)
			}
		}
		if !reflect.DeepEqual(Cluster(g, Options{ExcludeHubsPercent: 100}), Cluster(g, Options{})) {
			t.Fatal("100th percentile changed default clustering")
		}
	}
}
