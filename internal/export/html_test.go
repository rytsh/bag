package export

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rytsh/bag/internal/graph"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

func TestAggregatedHTMLIncludesMemberCounts(t *testing.T) {
	g := graph.New()
	a, b, c := ids.MakeID("test", "a"), ids.MakeID("test", "b"), ids.MakeID("test", "c")
	for _, id := range []string{a, b, c} {
		g.AddNode(&model.Node{ID: id, Label: id, FileType: model.FileTypeCode, SourceFile: "src/main.go"})
	}
	g.SetEdge(&model.Edge{Source: a, Target: b, Weight: 1})
	g.SetEdge(&model.Edge{Source: b, Target: c, Weight: 1})
	path := filepath.Join(t.TempDir(), "graph.html")
	if err := ToHTML(g, graph.Communities{0: {a, b}, 1: {c}}, path, HTMLOptions{NodeLimit: 1}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"member_count":2`, `"member_count":1`, "_member_count: n.member_count", "Members:"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %q in aggregated HTML", want)
		}
	}
}
