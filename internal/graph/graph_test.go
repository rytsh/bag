package graph_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rytsh/bag/internal/analyze"
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
		{Source: "a", Target: "b", Relation: "imports", Confidence: model.Extracted, SourceFile: "a.py", SourceLocation: "L2", Weight: 1, Context: "import", Extra: map[string]any{"type_only": true}},
		{Source: "a", Target: "b", Relation: "uses", Confidence: model.Extracted, SourceFile: "a.py", SourceLocation: "L1", Weight: 1},
	}

	e := graph.Build(nodes, edges, nil, "").Edge("a", "b")
	if e == nil || e.Relation != "imports" || e.SourceLocation != "L2" {
		t.Fatalf("unexpected collapsed edge: %+v", e)
	}

	if e.Context != "import" || e.Extra["type_only"] != true {
		t.Fatalf("missing retained attributes: %+v", e)
	}
}

// Containment is implied by location: on a pair it never replaces a
// relation that says more (a function calling the local function it
// declares, a nested class extending its outer class).
func TestBuildKeepsTheRelationOverContainment(t *testing.T) {
	nodes := []*model.Node{node("a", "A", "a.py"), node("b", "B", "a.py")}
	edges := []*model.Edge{
		{Source: "a", Target: "b", Relation: "calls", Confidence: model.Extracted, SourceFile: "a.py", SourceLocation: "L2", Weight: 1, Context: "call"},
		{Source: "a", Target: "b", Relation: "contains", Confidence: model.Extracted, SourceFile: "a.py", SourceLocation: "L1", Weight: 1},
	}

	if e := graph.Build(nodes, edges, nil, "").Edge("a", "b"); e == nil || e.Relation != "calls" {
		t.Fatalf("collapsed edge = %+v, want the call", e)
	}
}

// Edges() is the order every unsorted output (reports, exports, questions)
// inherits, so it must not depend on map iteration.
func TestEdgesOrderIsStable(t *testing.T) {
	var nodes []*model.Node
	var edges []*model.Edge
	nodes = append(nodes, node("hub", "Hub", "h.go"))
	for i := range 40 {
		id := "n" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		nodes = append(nodes, node(id, id, "h.go"))
		edges = append(edges, edge("hub", id, "calls"))
	}
	g := graph.Build(nodes, edges, nil, "")

	key := func() string {
		var b strings.Builder
		for _, e := range g.Edges() {
			b.WriteString(e.Source + ">" + e.Target + ";")
		}

		return b.String()
	}

	want := key()
	for range 30 {
		if got := key(); got != want {
			t.Fatal("Edges() order changed between calls")
		}
	}
	if g.NumEdges() != 40 {
		t.Errorf("NumEdges = %d, want 40", g.NumEdges())
	}

	g.RemoveNode("hub")
	if g.NumEdges() != 0 || len(g.Edges()) != 0 {
		t.Errorf("after removing the hub: NumEdges = %d, Edges = %d", g.NumEdges(), len(g.Edges()))
	}
}

// Caller-supplied sizes are clamped: a huge budget overflowed into a negative
// slice bound, a huge depth spun for ever, and the output order of a query
// must not change between runs.
func TestQueryBoundsAndDeterminism(t *testing.T) {
	g := buildSample()
	e := query.New(g, nil, nil)

	done := make(chan string, 1)
	go func() {
		done <- e.Query("X", query.Options{Depth: 1 << 31, Budget: 1 << 62})
	}()
	select {
	case out := <-done:
		if out == "" {
			t.Fatal("empty answer")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a huge depth did not finish")
	}

	if out := e.Query("X", query.Options{Budget: -1}); out == "" {
		t.Fatal("empty answer for a negative budget")
	}

	want := e.Query("X", query.Options{})
	for range 20 {
		if got := e.Query("X", query.Options{}); got != want {
			t.Fatal("query output changed between runs")
		}
	}
}

// A hub percentile above 100 is clamped, not an index past the end.
func TestHubPercentileAboveHundred(t *testing.T) {
	g := buildSample()
	_ = cluster.Cluster(g, cluster.Options{ExcludeHubsPercent: 150})
}

func TestGodNodesPercentileAboveHundred(t *testing.T) {
	_ = analyze.GodNodes(buildSample(), 10, 150)
}

// Loading a graph and writing it again keeps integers integers.
func TestLoadWriteKeepsIntegers(t *testing.T) {
	g := graph.New()
	g.AddNode(&model.Node{ID: "a", Label: "A", FileType: model.FileTypeCode, SourceFile: "a.rs", SourceLocation: "L1",
		Metadata: map[string]any{"line": 7, "ratio": 0.5}, Extra: map[string]any{"_rust_declaration_count": 1}})

	first, err := g.MarshalGraphJSON(graph.WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := graph.Parse(first)
	if err != nil {
		t.Fatal(err)
	}

	second, err := loaded.G.MarshalGraphJSON(graph.WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if string(first) != string(second) {
		t.Errorf("round trip changed the graph:\n%s\n---\n%s", first, second)
	}
}

// A hyperedge member merged into a duplicate stays a member, as the node it
// was merged into.
func TestHyperedgeFollowsMergedMembers(t *testing.T) {
	nodes := []*model.Node{
		{ID: "doc_rate_limiter", Label: "Rate Limiter", FileType: "concept", SourceFile: "a.md"},
		{ID: "doc_rate_limiter_c2", Label: "Rate Limiter", FileType: "concept", SourceFile: "b.md"},
		{ID: "doc_cache", Label: "Cache", FileType: "concept", SourceFile: "a.md"},
	}
	hyper := []*model.Hyperedge{{ID: "h", Label: "flow", Relation: "flow", Nodes: []string{"doc_rate_limiter_c2", "doc_cache"}}}

	g := graph.Build(nodes, nil, hyper, "")
	if len(g.Hyperedges) != 1 || len(g.Hyperedges[0].Nodes) != 2 {
		t.Fatalf("hyperedges = %+v, want both members", g.Hyperedges)
	}
}
