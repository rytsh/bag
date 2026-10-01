package integrate

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tailscale/hujson"
)

func TestMCPInstallPreservesSettingsAndComments(t *testing.T) {
	for _, client := range []string{"claude", "cursor", "opencode"} {
		t.Run(client, func(t *testing.T) {
			t.Chdir(t.TempDir())
			path := ".mcp.json"
			input := `{"custom":true,"mcpServers":{"other":{"command":"other-server"}}}`
			pointer := "/mcpServers/bag"
			if client == "cursor" {
				path = filepath.Join(".cursor", "mcp.json")
			}
			if client == "opencode" {
				path = "opencode.jsonc"
				pointer = "/mcp/servers/bag"
				input = "{\n// retain this comment\n\"model\":\"custom/model\",\n\"mcp\":{\"timeout\":{\"execution\":600000},\"servers\":{\"other\":{\"type\":\"local\",\"command\":[\"other-server\"]},}},\n}"
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(t.TempDir(), "project with spaces")
			binary := filepath.Join(t.TempDir(), "bag executable")
			graphPath := filepath.Join(root, "graphify-out", "graph.json")
			files, err := PrepareMCP(client, binary, root, graphPath, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 1 || files[0].Path != path {
				t.Fatalf("unexpected plan: %+v", files)
			}
			if err := files[0].Write(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := hujson.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			otherPointer := "/mcpServers/other"
			if client == "opencode" {
				otherPointer = "/mcp/servers/other"
				if !strings.Contains(string(raw), "retain this comment") || doc.Find("/model") == nil || doc.Find("/mcp/timeout") == nil {
					t.Fatal("lost comments or unrelated OpenCode settings")
				}
			} else if doc.Find("/custom") == nil {
				t.Fatal("lost unrelated setting")
			}
			if doc.Find(pointer) == nil || doc.Find(otherPointer) == nil {
				t.Fatal("missing bag or other server")
			}
			if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
				t.Fatalf("changed config permissions: %v, %v", st, err)
			}
			entry := doc.Find(pointer).Clone()
			entry.Standardize()
			var decoded map[string]any
			if err := json.Unmarshal(entry.Pack(), &decoded); err != nil {
				t.Fatal(err)
			}
			var command []any
			if client == "opencode" {
				command = decoded["command"].([]any)
				if command[0] != binary || decoded["cwd"] != root {
					t.Fatal("wrong OpenCode executable or cwd")
				}
				command = command[1:]
			} else {
				if decoded["command"] != binary {
					t.Fatal("wrong executable")
				}
				command = decoded["args"].([]any)
			}
			want := []string{"serve", "--root", root, "--graph", graphPath}
			if len(command) != len(want) {
				t.Fatalf("wrong command: %v", command)
			}
			for i := range want {
				if command[i] != want[i] {
					t.Fatalf("wrong command: %v", command)
				}
			}
			again, err := PrepareMCP(client, binary, root, graphPath, false)
			if err != nil || !bytes.Equal(again[0].Data, raw) {
				t.Fatalf("installation not idempotent: %v", err)
			}
			if _, err := PrepareMCP(client, "different-bag", root, graphPath, false); err == nil {
				t.Fatal("silently replaced existing bag entry")
			}
			if _, err := PrepareMCP(client, "different-bag", root, graphPath, true); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMCPRejectsInvalidConfigsWithoutWriting(t *testing.T) {
	for _, input := range []string{"broken", "[]", `{"mcp":null}`, `{"mcp":{"servers":[]}}`, `{"mcp":{"bag":{"type":"local"}}}`} {
		t.Run(input, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if err := os.WriteFile("opencode.json", []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := PrepareMCP("opencode", "bag", "root", "graph", false); err == nil {
				t.Fatal("accepted invalid config")
			}
			raw, err := os.ReadFile("opencode.json")
			if err != nil || string(raw) != input {
				t.Fatal("modified invalid config")
			}
		})
	}
}

func TestMCPAllAndAgents(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := PrepareMCP("agents", "bag", "root", "graph", false); err == nil {
		t.Fatal("agents has no universal MCP config")
	}
	files, err := PrepareMCP("all", "bag", "root", "graph", false)
	if err != nil || len(files) != 3 {
		t.Fatalf("all: files=%v error=%v", files, err)
	}
	if err := os.MkdirAll(".opencode", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(".opencode", "opencode.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err = PrepareMCP("opencode", "bag", "root", "graph", false)
	if err != nil || files[0].Path != filepath.Join(".opencode", "opencode.json") {
		t.Fatalf("did not reuse nested config: %v %v", files, err)
	}
	if err := os.WriteFile("opencode.json", []byte(`{"mcp":{"servers":{"bag":{"type":"local","command":["user-bag"]}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareMCP("opencode", "bag", "root", "graph", false); err == nil {
		t.Fatal("silently shadowed an existing server in the direct project config")
	}
}
