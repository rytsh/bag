package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallLeavesProjectInstructionsUntouched(t *testing.T) {
	targets := map[string]string{
		"agents":   filepath.Join(".agents", "skills", "bag", "SKILL.md"),
		"claude":   filepath.Join(".claude", "skills", "bag", "SKILL.md"),
		"opencode": filepath.Join(".opencode", "skills", "bag", "SKILL.md"),
		"cursor":   filepath.Join(".cursor", "rules", "bag.mdc"),
	}

	for _, platform := range []string{"agents", "claude", "opencode", "cursor", "all"} {
		for _, existing := range []bool{false, true} {
			name := platform + "/absent"
			if existing {
				name = platform + "/existing"
			}
			t.Run(name, func(t *testing.T) {
				t.Chdir(t.TempDir())
				const content = "# User instructions\n\nKeep this unchanged.\n"
				files := []string{"AGENTS.md", "CLAUDE.md"}
				if existing {
					for _, file := range files {
						if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
							t.Fatal(err)
						}
					}
				}
				var args []string
				if platform != "agents" {
					args = []string{"--platform", platform}
				}
				if err := runInstall(context.Background(), args); err != nil {
					t.Fatal(err)
				}
				for _, file := range files {
					raw, err := os.ReadFile(file)
					if !existing {
						if !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("%s was created or could not be checked: %v", file, err)
						}
					} else if err != nil || string(raw) != content {
						t.Fatalf("%s changed: content=%q error=%v", file, raw, err)
					}
				}
				for target, path := range targets {
					if platform != "all" && platform != target {
						continue
					}
					raw, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					want := skillBody
					if target == "cursor" {
						want = "---\nalwaysApply: true\n---\n" + agentSection
					}
					if string(raw) != want {
						t.Fatalf("unexpected content in %s", path)
					}
				}
			})
		}
	}
}

func TestInstallMCPPreflightsConflicts(t *testing.T) {
	t.Chdir(t.TempDir())
	const config = `{"mcpServers":{"bag":{"command":"user-bag"},"other":{"command":"other-server"}}}`
	if err := os.WriteFile(".mcp.json", []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runInstall(context.Background(), []string{"--platform", "claude", "--mcp"})
	if err == nil || !strings.Contains(err.Error(), "--replace-mcp") {
		t.Fatalf("expected conflict, got %v", err)
	}
	if _, err := os.Stat(".claude"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrote skills before validating MCP: %v", err)
	}
	raw, err := os.ReadFile(".mcp.json")
	if err != nil || string(raw) != config {
		t.Fatal("modified conflicting config")
	}
	if err := runInstall(context.Background(), []string{"--platform", "claude", "--mcp", "--replace-mcp"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"AGENTS.md", "CLAUDE.md"} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("MCP install created %s: %v", path, err)
		}
	}
}
