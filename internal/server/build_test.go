package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	_ "github.com/rytsh/bag/internal/extract/langs"
	"github.com/rytsh/bag/internal/pipeline"
)

func connectTestClient(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callTool(t *testing.T, client *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func resultText(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestProjectMCPBuildAndUpdate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "graphify-out", "graph.json")
	writeCode := func(code string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(code), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeCode("package main\nfunc Start() { End() }\nfunc End() {}\n")
	if err := os.WriteFile(filepath.Join(root, ".bagignore"), []byte("ignored.go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored.go"), []byte("package main\nfunc Ignored() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewProjectStore(path, pipeline.Options{Root: root, Gitignore: true, Workers: 1, Resolution: 1})
	if err != nil {
		t.Fatal(err)
	}
	client := connectTestClient(t, NewMCPServer(store, "test"))
	before := callTool(t, client, "query_graph", map[string]any{"question": "Start"})
	if !before.IsError || !strings.Contains(resultText(before), "extract_graph") {
		t.Fatalf("query before build = %+v", before)
	}
	tools, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "extract_graph" || tool.Name == "update_graph" {
			if tool.Annotations == nil || tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
				t.Fatalf("missing write annotations for %s", tool.Name)
			}
		}
	}
	build := callTool(t, client, "extract_graph", map[string]any{})
	if build.IsError {
		t.Fatal(resultText(build))
	}
	for _, file := range []string{"graph.json", "GRAPH_REPORT.md", "graph.html", ".graphify_root", "cache"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), file)); err != nil {
			t.Fatal(err)
		}
	}
	_, engine := store.Get()
	if len(engine.Find("Start")) == 0 || len(engine.Find("Ignored")) != 0 {
		t.Fatal("build did not extract Start or did not honor .bagignore")
	}
	query := callTool(t, client, "shortest_path", map[string]any{"source": "Start", "target": "End"})
	if query.IsError || !strings.Contains(resultText(query), "calls") {
		t.Fatal(resultText(query))
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "query_graph", Arguments: map[string]any{"question": "Start End calls"}})
			if err != nil {
				t.Error(err)
			} else if result.IsError {
				t.Error(resultText(result))
			}
		})
	}
	wg.Wait()
	writeCode("package main\nfunc Start() { End() }\nfunc End() {}\nfunc Next() {}\n")
	update := callTool(t, client, "update_graph", map[string]any{})
	if update.IsError {
		t.Fatal(resultText(update))
	}
	_, engine = store.Get()
	if len(engine.Find("Next")) == 0 {
		t.Fatal("updated graph was not reloaded")
	}
	writeCode("package main\nfunc Start() { End() }\nfunc End() {}\n")
	shrink := callTool(t, client, "update_graph", map[string]any{})
	if !shrink.IsError || !strings.Contains(resultText(shrink), "refusing to overwrite") {
		t.Fatalf("unguarded shrink: %s", resultText(shrink))
	}
	_, engine = store.Get()
	if len(engine.Find("Next")) == 0 {
		t.Fatal("refused build changed the served graph")
	}
	forced := callTool(t, client, "update_graph", map[string]any{"force": true, "no_viz": true})
	if forced.IsError {
		t.Fatal(resultText(forced))
	}
	_, engine = store.Get()
	if len(engine.Find("Next")) != 0 {
		t.Fatal("forced update did not remove Next")
	}
	store.buildMu.Lock()
	busy := callTool(t, client, "extract_graph", map[string]any{})
	store.buildMu.Unlock()
	if !busy.IsError || !strings.Contains(resultText(busy), "already running") {
		t.Fatalf("concurrent build = %s", resultText(busy))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.build(ctx, buildArgs{}); err == nil {
		t.Fatal("cancelled build succeeded")
	}
}

func TestReadOnlyMCPDoesNotOfferBuildTools(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "graphify-out", "graph.json")
	project, err := NewProjectStore(path, pipeline.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	// HTTP uses this read-only server even if given a writable project store.
	client := connectTestClient(t, newMCPServer(project, "test", false))
	tools, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "extract_graph" || tool.Name == "update_graph" {
			t.Fatalf("read-only server offers %s", tool.Name)
		}
	}
	if _, err := NewStore(path); err == nil {
		t.Fatal("read-only store should still require an existing graph")
	}
}

func TestProjectStoreRejectsEscapingOutput(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if _, err := NewProjectStore(filepath.Join(outside, "graph.json"), pipeline.Options{Root: root}); err == nil {
		t.Fatal("accepted output outside project")
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := NewProjectStore(filepath.Join(link, "new", "graph.json"), pipeline.Options{Root: root}); err == nil {
		t.Fatal("accepted output through symlink outside project")
	}
	path := filepath.Join(root, "graphify-out", "graph.json")
	project, err := NewProjectStore(path, pipeline.Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "report.md"), filepath.Join(filepath.Dir(path), "GRAPH_REPORT.md")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := project.build(context.Background(), buildArgs{}); err == nil {
		t.Fatal("accepted a report symlink outside project")
	}
}
