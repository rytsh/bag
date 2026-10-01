// Package integrate installs project-local assistant integrations.
package integrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rytsh/bag/internal/graph"
	"github.com/tailscale/hujson"
)

// MCPFile is a validated configuration update. Prepare all updates before
// writing any so a conflicting server never causes a partial installation.
type MCPFile struct {
	Path string
	Data []byte
}

// PrepareMCP adds one stdio server named bag while preserving other settings
// and JSONC comments. Existing, different bag entries require explicit replace.
func PrepareMCP(platform, binary, root, graphPath string, replace bool) ([]MCPFile, error) {
	platforms := []string{platform}
	if platform == "all" {
		platforms = []string{"claude", "opencode", "cursor"}
	}
	args := []string{"serve", "--root", root, "--graph", graphPath}
	var files []MCPFile
	for _, client := range platforms {
		var path, pointer string
		var entry any
		switch client {
		case "claude":
			path, pointer = ".mcp.json", "/mcpServers/bag"
			entry = map[string]any{"type": "stdio", "command": binary, "args": args}
		case "cursor":
			path, pointer = filepath.Join(".cursor", "mcp.json"), "/mcpServers/bag"
			entry = map[string]any{"type": "stdio", "command": binary, "args": args}
		case "opencode":
			var err error
			path, err = opencodeConfig()
			if err != nil {
				return nil, err
			}
			pointer = "/mcp/servers/bag"
			entry = map[string]any{"type": "local", "command": append([]string{binary}, args...), "cwd": root}
		default:
			return nil, fmt.Errorf("MCP requires --platform claude, opencode, cursor or all; agents has no universal MCP config")
		}
		if client == "opencode" && filepath.Dir(path) == ".opencode" {
			// A new entry in .opencode/ would also override a server in the
			// direct project config. Treat that as replacement, not a new install.
			for _, inherited := range []string{"opencode.json", "opencode.jsonc"} {
				if _, err := os.Stat(inherited); errors.Is(err, os.ErrNotExist) {
					continue
				} else if err != nil {
					return nil, fmt.Errorf("stat inherited OpenCode config; %w", err)
				}
				if _, err := mergeMCP(inherited, pointer, entry, replace); err != nil {
					return nil, fmt.Errorf("check inherited OpenCode MCP; %w", err)
				}
			}
		}
		data, err := mergeMCP(path, pointer, entry, replace)
		if err != nil {
			return nil, fmt.Errorf("configure %s MCP; %w", client, err)
		}
		files = append(files, MCPFile{Path: path, Data: data})
	}
	return files, nil
}

// Write persists the prepared update atomically, retaining existing permissions.
func (f MCPFile) Write() error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(f.Path); err == nil {
		mode = st.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s; %w", f.Path, err)
	}
	if err := graph.WriteFileAtomic(f.Path, f.Data); err != nil {
		return fmt.Errorf("write %s; %w", f.Path, err)
	}
	if err := os.Chmod(f.Path, mode); err != nil {
		return fmt.Errorf("restore %s permissions; %w", f.Path, err)
	}
	return nil
}

func opencodeConfig() (string, error) {
	// Prefer the most specific project config rather than creating a second
	// config which could shadow an existing bag server.
	for _, base := range []string{".opencode", "."} {
		jsonPath := filepath.Join(base, "opencode.json")
		jsoncPath := filepath.Join(base, "opencode.jsonc")
		var existing []string
		for _, path := range []string{jsonPath, jsoncPath} {
			if _, err := os.Stat(path); err == nil {
				existing = append(existing, path)
			} else if !errors.Is(err, os.ErrNotExist) {
				return "", fmt.Errorf("stat OpenCode config; %w", err)
			}
		}
		if len(existing) > 1 {
			return "", fmt.Errorf("both %s and %s exist; choose one before installing MCP", jsonPath, jsoncPath)
		}
		if len(existing) == 1 {
			return existing[0], nil
		}
	}
	return "opencode.json", nil
}

func mergeMCP(path, pointer string, entry any, replace bool) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		raw = []byte("{}\n")
	} else if err != nil {
		return nil, fmt.Errorf("read %s; %w", path, err)
	}
	doc, err := hujson.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s; %w", path, err)
	}
	if doc.Value.Kind() != '{' {
		return nil, fmt.Errorf("%s must contain a JSON object", path)
	}
	if pointer == "/mcp/servers/bag" && doc.Find("/mcp/bag") != nil {
		return nil, fmt.Errorf("%s contains a legacy mcp.bag entry; migrate it to mcp.servers.bag first", path)
	}
	entryBytes, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("encode MCP server; %w", err)
	}
	if existing := doc.Find(pointer); existing != nil {
		old := existing.Clone()
		old.Standardize()
		var a, b any
		if err := json.Unmarshal(old.Pack(), &a); err != nil {
			return nil, fmt.Errorf("decode existing MCP server; %w", err)
		}
		if err := json.Unmarshal(entryBytes, &b); err != nil {
			return nil, fmt.Errorf("decode new MCP server; %w", err)
		}
		canonicalA, _ := json.Marshal(a)
		canonicalB, _ := json.Marshal(b)
		if bytes.Equal(canonicalA, canonicalB) {
			return raw, nil
		}
		if !replace {
			return nil, fmt.Errorf("%s already defines a different bag MCP server; use --replace-mcp to replace only that entry", path)
		}
	}
	parents := []string{"/mcpServers"}
	if pointer == "/mcp/servers/bag" {
		parents = []string{"/mcp", "/mcp/servers"}
	}
	for _, parent := range parents {
		if v := doc.Find(parent); v != nil {
			if v.Value.Kind() != '{' {
				return nil, fmt.Errorf("%s: %s must be an object", path, parent)
			}
		} else if err := addJSON(&doc, parent, json.RawMessage("{}")); err != nil {
			return nil, err
		}
	}
	if err := addJSON(&doc, pointer, entry); err != nil {
		return nil, err
	}
	doc.Format()
	return doc.Pack(), nil
}

func addJSON(doc *hujson.Value, path string, value any) error {
	patch, err := json.Marshal([]map[string]any{{"op": "add", "path": path, "value": value}})
	if err != nil {
		return fmt.Errorf("encode config patch; %w", err)
	}
	if err := doc.Patch(patch); err != nil {
		return fmt.Errorf("patch MCP config; %w", err)
	}
	return nil
}
