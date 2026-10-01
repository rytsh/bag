package export

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rytsh/bag/internal/analyze"
	"github.com/rytsh/bag/internal/graph"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// Adapted from Graphify's tests/test_wiki.py (Apache-2.0).
func TestWikiReservesIndex(t *testing.T) {
	g := graph.New()
	id := ids.MakeID("test", "index")
	g.AddNode(&model.Node{ID: id, Label: "index()", SourceFile: "views.py", FileType: model.FileTypeCode})
	dir := t.TempDir()
	count, err := ToWiki(WikiInput{
		Graph: g, Communities: graph.Communities{0: {id}},
		Labels: map[int]string{0: "index"},
		Gods:   []analyze.GodNode{{ID: id, Label: "index()"}},
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("articles = %d, want 2", count)
	}
	for _, name := range []string{"index_2.md", "index_3.md"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "[index](index.md) to navigate") {
			t.Fatalf("%s navigation does not point to catalog", name)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "index.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index_2.md", "index_3.md"} {
		if !strings.Contains(string(raw), name) {
			t.Fatalf("catalog missing article %s", name)
		}
	}
}
