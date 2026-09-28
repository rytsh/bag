package bag_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/rytsh/bag"
)

func TestBuildLibrary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.py"), []byte("def hello():\n    return 'hi'\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := bag.Build(context.Background(), root, bag.BuildOptions{
		NoCache: true, NoVisualization: true, NoReport: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Nodes < 2 || result.Edges < 1 || result.Extracted != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.GraphPath != bag.GraphPath(root) {
		t.Fatalf("GraphPath = %q, want %q", result.GraphPath, bag.GraphPath(root))
	}
	if _, err := os.Stat(result.GraphPath); err != nil {
		t.Fatal(err)
	}
}

func TestBuildLibraryCustomOutDir(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := bag.Build(context.Background(), root, bag.BuildOptions{
		OutDir: "artifacts", NoCache: true, NoVisualization: true, NoReport: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "artifacts")
	if result.OutDir != want || result.ReportPath != filepath.Join(want, "GRAPH_REPORT.md") || result.HTMLPath != filepath.Join(want, "graph.html") {
		t.Fatalf("custom output paths: %+v", result)
	}
}

func TestMergeGraphsLibrary(t *testing.T) {
	root := t.TempDir()
	alpha := writeGraph(t, root, "alpha", map[string]any{
		"directed": false, "multigraph": false, "graph": map[string]any{},
		"nodes": []any{
			node("service", "Service", "src/Service.java", 0, map[string]any{"namespace": "Acme"}, true),
			node("run", ".Run()", "src/Service.java", 0, map[string]any{"namespace": "Acme"}, false),
			node("contract", "Contract", "src/Contract.java", 1, map[string]any{"namespace": "Shared"}, true),
			map[string]any{"id": "java_std", "label": "java_std", "external": true, "file_type": "concept", "source_file": "", "type": "external"},
		},
		"links": []any{
			map[string]any{"source": "service", "target": "run", "relation": "method", "confidence": "EXTRACTED", "source_file": "src/Service.java", "weight": 1.0},
		},
		"hyperedges": []any{map[string]any{"id": "flow", "nodes": []any{"service", "run"}}},
	})
	beta := writeGraph(t, root, "beta", map[string]any{
		"directed": false, "multigraph": false, "graph": map[string]any{},
		"nodes": []any{
			node("worker", ".Work()", "src/Worker.java", 0, map[string]any{
				"namespace": "Other", "unresolved_calls": []any{map[string]any{
					"callee": "Run", "receiver_type": "Service", "lang": "java", "line": "L8",
				}},
			}, false),
			node("contract", "Contract", "src/Contract.java", 1, map[string]any{"namespace": "Shared"}, true),
			map[string]any{"id": "java_std", "label": "java_std", "external": true, "file_type": "concept", "source_file": "", "type": "external"},
		},
		"links":      []any{},
		"hyperedges": []any{},
	})

	out := filepath.Join(root, "merged", "graph.json")
	result, err := bag.MergeGraphs(context.Background(), out, alpha, beta)
	if err != nil {
		t.Fatal(err)
	}
	if result.Nodes != 6 || result.Edges != 3 {
		t.Fatalf("unexpected merge result: %+v", result)
	}

	var merged struct {
		Graph      map[string]any   `json:"graph"`
		Nodes      []map[string]any `json:"nodes"`
		Links      []map[string]any `json:"links"`
		Hyperedges []map[string]any `json:"hyperedges"`
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &merged); err != nil {
		t.Fatal(err)
	}

	byID := map[string]map[string]any{}
	for _, n := range merged.Nodes {
		byID[n["id"].(string)] = n
	}
	if byID["java_std"] == nil || byID["alpha::service"] == nil || byID["beta::worker"] == nil {
		t.Fatalf("missing prefixed/global nodes: %v", byID)
	}
	if got := int(byID["beta::worker"]["community"].(float64)); got != 2 {
		t.Fatalf("beta community = %d, want 2", got)
	}

	relations := map[string]bool{}
	for _, e := range merged.Links {
		relations[e["relation"].(string)] = true
	}
	if !relations["method"] || !relations["same_type_as"] || !relations["calls"] {
		t.Fatalf("merged relations: %v", relations)
	}
	if len(merged.Hyperedges) != 1 || merged.Hyperedges[0]["id"] != "alpha::flow" {
		t.Fatalf("hyperedges: %v", merged.Hyperedges)
	}
	if _, ok := merged.Graph["hyperedges"]; !ok {
		t.Fatal("nested hyperedges missing")
	}
}

func node(id, label, source string, community int, metadata map[string]any, class bool) map[string]any {
	n := map[string]any{
		"id": id, "label": label, "file_type": "code", "source_file": source,
		"source_location": "L1", "community": community, "metadata": metadata,
	}
	if class {
		n["_callable_class"] = true
	}
	return n
}

func writeGraph(t *testing.T, root, repo string, doc map[string]any) string {
	t.Helper()
	path := filepath.Join(root, repo, "graphify-out", "graph.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
