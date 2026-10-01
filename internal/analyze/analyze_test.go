package analyze

import (
	"strings"
	"testing"

	"github.com/rytsh/bag/internal/graph"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// Adapted from Graphify's tests/test_analyze.py (Apache-2.0).
func TestSurpriseDirectoryBonus(t *testing.T) {
	for _, tc := range []struct {
		name, left, right, leftRepo, rightRepo string
		cross                                  bool
	}{
		{"root files", "a.py", "b.py", "", "", false},
		{"same directory", "src/a.py", "src/b.py", "", "", false},
		{"different directories", "src/a.py", "lib/b.py", "", "", true},
		{"root and directory", "a.py", "src/b.py", "", "", true},
		{"relative prefixes", "././src/a.py", "src/b.py", "", "", false},
		{"backslashes", `src\a.py`, "./src/b.py", "", "", false},
		{"absolute outside root", "a.py", "/outside/b.py", "", "", true},
		{"different repos same directory", "src/a.py", "src/b.py", "repo-a", "repo-b", true},
		{"same repo", "a.py", "b.py", "repo-a", "repo-a", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := graph.New()
			u, v := ids.MakeID("test", "left"), ids.MakeID("test", "right")
			left := &model.Node{ID: u, Label: "Left", SourceFile: tc.left}
			right := &model.Node{ID: v, Label: "Right", SourceFile: tc.right}
			if tc.leftRepo != "" {
				left.Extra = map[string]any{"repo": tc.leftRepo}
			}
			if tc.rightRepo != "" {
				right.Extra = map[string]any{"repo": tc.rightRepo}
			}
			g.AddNode(left)
			g.AddNode(right)
			e := &model.Edge{Source: u, Target: v, Relation: "references", Confidence: model.Extracted, Weight: 1}
			g.SetEdge(e)
			score, reasons := surpriseScore(g, e, nil)
			want := 1
			if tc.cross {
				want += 2
			}
			if score != want || strings.Contains(strings.Join(reasons, " "), "different repos/directories") != tc.cross {
				t.Fatalf("score=%d reasons=%v; want score=%d cross=%v", score, reasons, want, tc.cross)
			}
		})
	}
}
