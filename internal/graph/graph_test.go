package graph_test

import (
	"testing"

	"github.com/rytsh/bag/internal/cluster"
	"github.com/rytsh/bag/internal/graph"
	"github.com/rytsh/bag/internal/model"
	"github.com/rytsh/bag/internal/query"
)

func node(id, label, sf string) *model.Node {
	return &model.Node{ID: id, Label: label, FileType: model.FileTypeCode, SourceFile: sf, SourceLocation: "L1"}
}

func edge(s, t, rel string) *model.Edge {
	return &model.Edge{Source: s, Target: t, Relation: rel, Confidence: model.Extracted, Weight: 1}
}

func buildSample() *graph.Graph {
	nodes := []*model.Node{
		node("a", "a.go", "a.go"), node("a_x", "X()", "a.go"), node("a_y", "Y()", "a.go"),
		node("b", "b.go", "b.go"), node("b_z", "Z()", "b.go"), node("b_w", "W()", "b.go"),
	}
	edges := []*model.Edge{
		edge("a", "a_x", "contains"), edge("a", "a_y", "contains"), edge("a_x", "a_y", "calls"),
		edge("b", "b_z", "contains"), edge("b", "b_w", "contains"), edge("b_z", "b_w", "calls"),
		edge("a_y", "b_z", "calls"),
		edge("a", "go_pkg_fmt", "imports_from"),
		edge("a_x", "missing", "calls"),
	}

	return graph.Build(nodes, edges, nil, "")
}

func TestBuildAndRoundTrip(t *testing.T) {
	g := buildSample()

	if !g.Has("go_pkg_fmt") || g.Node("go_pkg_fmt").Type != "external" {
		t.Fatalf("expected external stub for import target")
	}

	if g.Has("missing") {
		t.Fatalf("dangling call target must be dropped")
	}

	if g.NumEdges() != 8 {
		t.Fatalf("edges = %d, want 8", g.NumEdges())
	}

	c := cluster.Cluster(g, cluster.Options{})

	raw, err := g.MarshalGraphJSON(graph.WriteOptions{Communities: c})
	if err != nil {
		t.Fatal(err)
	}

	l, err := graph.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}

	if l.G.NumNodes() != g.NumNodes() || l.G.NumEdges() != g.NumEdges() {
		t.Fatalf("round trip lost data: %d/%d vs %d/%d", l.G.NumNodes(), l.G.NumEdges(), g.NumNodes(), g.NumEdges())
	}

	if e := l.G.Edge("a_y", "b_z"); e == nil || e.Source != "a_y" || e.Target != "b_z" {
		t.Fatalf("edge direction not preserved: %+v", e)
	}

	if len(l.Communities) != len(c) {
		t.Fatalf("communities not preserved")
	}
}

func TestClusterDeterministic(t *testing.T) {
	g := buildSample()
	a := cluster.Cluster(g, cluster.Options{})
	b := cluster.Cluster(g, cluster.Options{})

	if len(a) != len(b) {
		t.Fatal("cluster not deterministic")
	}

	for k, v := range a {
		if len(b[k]) != len(v) {
			t.Fatal("cluster not deterministic")
		}

		for i := range v {
			if b[k][i] != v[i] {
				t.Fatal("cluster not deterministic")
			}
		}
	}
}

func TestQuery(t *testing.T) {
	g := buildSample()
	e := query.New(g, cluster.Cluster(g, cluster.Options{}), nil)

	if p := e.ShortestPath("a_x", "b_w"); len(p) != 4 {
		t.Fatalf("path = %v", p)
	}

	if ids := e.Find("Z"); len(ids) == 0 || ids[0] != "b_z" {
		t.Fatalf("find = %v", ids)
	}

	if out := e.Query("Y calls", query.Options{}); out == "" {
		t.Fatal("empty query result")
	}
}

func TestDedupeEntities(t *testing.T) {
	nodes := []*model.Node{
		{ID: "doc_a", Label: "Authentication Service Design", FileType: "concept", SourceFile: "a.md"},
		{ID: "doc_b", Label: "Authentication Service Design", FileType: "concept", SourceFile: "b.md"},
		{ID: "code_x", Label: "Authentication Service Design", FileType: "code", SourceFile: "x.go"},
	}
	edges := []*model.Edge{edge("doc_b", "code_x", "references")}

	out, oe := graph.DedupeEntities(nodes, edges)
	if len(out) != 2 {
		t.Fatalf("expected concept duplicates merged, got %d nodes", len(out))
	}

	if oe[0].Source != "doc_a" {
		t.Fatalf("edge not rewired: %+v", oe[0])
	}
}

func TestBuildMergesAttributesForCollapsedEdgePair(t *testing.T) {
	nodes := []*model.Node{node("a", "A", "a.py"), node("b", "B", "a.py")}
	edges := []*model.Edge{
		{Source: "a", Target: "b", Relation: "calls", Confidence: model.Extracted, SourceFile: "a.py", SourceLocation: "L2", Weight: 1, Context: "call", Extra: map[string]any{"type_only": true}},
		{Source: "a", Target: "b", Relation: "contains", Confidence: model.Extracted, SourceFile: "a.py", SourceLocation: "L1", Weight: 1},
	}

	e := graph.Build(nodes, edges, nil, "").Edge("a", "b")
	if e == nil || e.Relation != "contains" || e.SourceLocation != "L1" {
		t.Fatalf("unexpected collapsed edge: %+v", e)
	}

	if e.Context != "call" || e.Extra["type_only"] != true {
		t.Fatalf("missing retained attributes: %+v", e)
	}
}
