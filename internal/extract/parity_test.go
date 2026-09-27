package extract_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/rytsh/bag/internal/detect"
	"github.com/rytsh/bag/internal/extract"
	_ "github.com/rytsh/bag/internal/extract/langs"
	"github.com/rytsh/bag/internal/graph"
)

type golden struct {
	Nodes [][]string `json:"nodes"`
	Edges [][]string `json:"edges"`
}

// TestGraphifyParity builds testdata/fixtures and compares node ids/labels
// and edge (source, target, relation, confidence) tuples with the graph
// Graphify produced for the same files (testdata/graphify_golden.json).
func TestGraphifyParity(t *testing.T) {
	root, _ := filepath.Abs("../../testdata/fixtures")

	raw, err := os.ReadFile("../../testdata/graphify_golden.json")
	if err != nil {
		t.Fatal(err)
	}

	var want golden
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}

	det, err := detect.Detect(root, detect.Options{IsCode: extract.IsCode})
	if err != nil {
		t.Fatal(err)
	}

	files := det.Files[detect.Code]

	res, err := extract.Run(context.Background(), files, extract.Options{Root: root, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}

	g := graph.Build(res.Nodes, res.Edges, nil, root)

	gotNodes := map[string]bool{}
	for _, n := range g.Nodes() {
		gotNodes[fmt.Sprint([]string{n.ID, n.Label, n.SourceLocation, n.SourceFile})] = true
	}

	gotEdges := map[string]bool{}
	for _, e := range g.Edges() {
		gotEdges[fmt.Sprint([]string{e.Source, e.Target, e.Relation, e.Confidence})] = true
	}

	// Known, documented divergences: Graphify's C# interface-dispatch pass
	// (dispatches_to) is not ported yet; Groovy uses gotreesitter's grammar
	// shape, which differs from tree-sitter-groovy.
	skip := func(k string) bool {
		return contains(k, "dispatches_to") || contains(k, "sample.groovy") || contains(k, "groovy")
	}

	var missingN, missingE []string

	for _, n := range want.Nodes {
		k := fmt.Sprint(n)
		if !gotNodes[k] && !skip(k) {
			missingN = append(missingN, k)
		}
	}

	for _, e := range want.Edges {
		k := fmt.Sprint(e)
		if !gotEdges[k] && !skip(k) {
			missingE = append(missingE, k)
		}
	}

	sort.Strings(missingN)
	sort.Strings(missingE)

	if len(missingN) > 0 || len(missingE) > 0 {
		t.Errorf("missing %d nodes / %d edges vs Graphify:\n%v\n%v", len(missingN), len(missingE), missingN, missingE)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}

	return false
}
