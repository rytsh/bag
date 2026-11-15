package extract_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// The golden files were captured from Graphify and are now bag's regression
// baselines: they pin node ids, labels, locations and edges. bag no longer
// has to match Graphify, so an intended change updates the golden file in the
// same commit. Declaration end lines are bag's own and are pinned separately
// in testdata/end_lines_golden.json.

// TestGraphifyParity builds testdata/fixtures and compares node ids/labels
// and edge (source, target, relation, confidence) tuples with the baseline
// in testdata/graphify_golden.json.
func TestGraphifyParity(t *testing.T) {
	// Known, documented divergence: Groovy uses gotreesitter's grammar shape,
	// which differs from tree-sitter-groovy.
	checkParity(t, "../../testdata/fixtures", "../../testdata/graphify_golden.json", func(k string) bool {
		return strings.Contains(k, "groovy")
	}, false)
}

// TestGraphifyResolveParity covers the cross-file resolution passes
// (header/impl merge, JS/TS symbol facts, receiver-typed member calls for
// TS/C#/Swift/Ruby, Kotlin/Elixir import targets, C# interface dispatch) on a
// multi-file corpus; both directions must match exactly.
func TestGraphifyResolveParity(t *testing.T) {
	checkParity(t, "../../testdata/resolve", "../../testdata/graphify_resolve_golden.json", nil, true)
}

// TestGraphifyUpstreamParity covers Graphify 0.9.73's enums, Java inheritance,
// imported Python modules containing nested symbols and inferred Kotlin fields.
func TestGraphifyUpstreamParity(t *testing.T) {
	checkParity(t, "../../testdata/upstream", "../../testdata/graphify_upstream_golden.json", nil, true)
}

// TestGraphifyFrameworkParity covers Astro, Solidity and VB.NET 0.9.73 output.
func TestGraphifyFrameworkParity(t *testing.T) {
	checkParity(t, "../../testdata/frameworks", "../../testdata/graphify_frameworks_golden.json", nil, true)
}

func checkParity(t *testing.T, dir, goldenPath string, skip func(string) bool, exact bool) {
	t.Helper()

	root, _ := filepath.Abs(dir)

	raw, err := os.ReadFile(goldenPath)
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

	res, err := extract.Run(context.Background(), det.Files[detect.Code], extract.Options{Root: root, Workers: 1})
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

	if skip == nil {
		skip = func(string) bool { return false }
	}

	missingN := missing(want.Nodes, gotNodes, skip)
	missingE := missing(want.Edges, gotEdges, skip)

	if len(missingN) > 0 || len(missingE) > 0 {
		t.Errorf("missing %d nodes / %d edges vs Graphify:\n%v\n%v", len(missingN), len(missingE), missingN, missingE)
	}

	if !exact {
		return
	}

	extraN := extra(want.Nodes, gotNodes)
	extraE := extra(want.Edges, gotEdges)

	if len(extraN) > 0 || len(extraE) > 0 {
		t.Errorf("extra %d nodes / %d edges vs Graphify:\n%v\n%v", len(extraN), len(extraE), extraN, extraE)
	}
}

func missing(want [][]string, got map[string]bool, skip func(string) bool) []string {
	var out []string

	for _, w := range want {
		k := fmt.Sprint(w)
		if !got[k] && !skip(k) {
			out = append(out, k)
		}
	}

	sort.Strings(out)

	return out
}

func extra(want [][]string, got map[string]bool) []string {
	w := map[string]bool{}
	for _, x := range want {
		w[fmt.Sprint(x)] = true
	}

	var out []string

	for k := range got {
		if !w[k] {
			out = append(out, k)
		}
	}

	sort.Strings(out)

	return out
}

// endLinesGolden is the path of the end-line baseline; UPDATE_GOLDEN=1
// rewrites it from the current extraction.
const endLinesGolden = "../../testdata/end_lines_golden.json"

// TestEndLines pins the declaration end lines bag records for every corpus.
// A start location is pinned by the parity tests; this pins where each
// declaration stops, keyed by node id.
func TestEndLines(t *testing.T) {
	got := map[string]map[string]int{}

	for _, dir := range []string{"fixtures", "resolve", "upstream", "frameworks"} {
		root, _ := filepath.Abs(filepath.Join("../../testdata", dir))

		det, err := detect.Detect(root, detect.Options{IsCode: extract.IsCode})
		if err != nil {
			t.Fatal(err)
		}

		res, err := extract.Run(context.Background(), det.Files[detect.Code], extract.Options{Root: root, Workers: 1})
		if err != nil {
			t.Fatal(err)
		}

		ends := map[string]int{}
		for _, n := range graph.Build(res.Nodes, res.Edges, nil, root).Nodes() {
			if n.EndLine > 0 {
				ends[n.ID] = n.EndLine
			}
		}
		got[dir] = ends
	}

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		raw, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(endLinesGolden, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}

		return
	}

	raw, err := os.ReadFile(endLinesGolden)
	if err != nil {
		t.Fatalf("%v; run with UPDATE_GOLDEN=1 to create it", err)
	}

	var want map[string]map[string]int
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}

	for dir, ends := range want {
		for id, end := range ends {
			if got[dir][id] != end {
				t.Errorf("%s: %s ends on %d, want %d", dir, id, got[dir][id], end)
			}
		}
		for id, end := range got[dir] {
			if _, ok := ends[id]; !ok {
				t.Errorf("%s: %s gained end line %d not in the baseline", dir, id, end)
			}
		}
	}
}
