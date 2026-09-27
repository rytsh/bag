package semantic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rytsh/bag/internal/detect"
)

func TestParseJSON(t *testing.T) {
	f, err := ParseJSON("Here you go:\n```json\n{\"nodes\":[{\"id\":\"a\"}],\"edges\":[]}\n```\n")
	if err != nil {
		t.Fatal(err)
	}

	if len(f.Nodes) != 1 {
		t.Fatalf("nodes = %v", f.Nodes)
	}

	if _, err := ParseJSON("not json"); err == nil {
		t.Fatal("expected error")
	}
}

func TestExtract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)

			return
		}

		frag := `{"nodes":[{"id":"Notes","label":"notes.txt","file_type":"document","source_file":"notes.txt"},` +
			`{"id":"design","label":"Design","file_type":"concept","source_file":"notes.txt"}],` +
			`"edges":[{"source":"notes","target":"design","relation":"references","confidence":"extracted","source_file":"notes.txt"}]}`

		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": frag}}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	defer srv.Close()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("design notes"), 0o644); err != nil {
		t.Fatal(err)
	}

	det, err := detect.Detect(root, detect.Options{})
	if err != nil {
		t.Fatal(err)
	}

	c, err := New(Config{BaseURL: srv.URL + "/v1", Model: "test", CacheDir: filepath.Join(root, "cache")})
	if err != nil {
		t.Fatal(err)
	}

	ex, tokens, err := c.Extract(context.Background(), det, nil)
	if err != nil {
		t.Fatal(err)
	}

	if tokens != [2]int{10, 5} {
		t.Fatalf("tokens = %v", tokens)
	}

	if len(ex.Nodes) != 2 || len(ex.Edges) != 1 {
		t.Fatalf("extraction = %d nodes %d edges", len(ex.Nodes), len(ex.Edges))
	}

	if ex.Edges[0].Confidence != "EXTRACTED" || ex.Nodes[0].ID != "notes" {
		t.Fatalf("normalization failed: %+v %+v", ex.Nodes[0], ex.Edges[0])
	}

	// Second run is served from cache (no tokens billed).
	_, tokens, err = c.Extract(context.Background(), det, nil)
	if err != nil || tokens != [2]int{} {
		t.Fatalf("cache miss: %v %v", tokens, err)
	}
}
