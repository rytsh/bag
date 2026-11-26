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

// A wiki export removes the pages its last run wrote and no longer writes,
// never Markdown it did not write.
func TestWikiKeepsMarkdownItDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	build := func(label string) {
		g := graph.New()
		id := ids.MakeID("test", label)
		g.AddNode(&model.Node{ID: id, Label: label + "()", SourceFile: "a.py", FileType: model.FileTypeCode})
		if _, err := ToWiki(WikiInput{
			Graph: g, Communities: graph.Communities{0: {id}},
			Labels: map[int]string{0: label},
			Gods:   []analyze.GodNode{{ID: id, Label: label + "()"}},
		}, dir); err != nil {
			t.Fatal(err)
		}
	}

	build("alpha")
	first, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	build("beta")

	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Fatalf("the user's README was removed: %v", err)
	}
	for _, f := range first {
		name := filepath.Base(f)
		if name == "README.md" || name == "index.md" {
			continue
		}
		if raw, err := os.ReadFile(f); err == nil && strings.Contains(string(raw), "alpha") {
			t.Errorf("stale page %s of the earlier export survived", name)
		}
	}
}

// Outputs are written readable by others (0644), as os.WriteFile would.
func TestWriteFileAtomicKeepsOutputsReadable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "graph.json")
	if err := graph.WriteFileAtomic(p, []byte("{}")); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", fi.Mode().Perm())
	}
}
