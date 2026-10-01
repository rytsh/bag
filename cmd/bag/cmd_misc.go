package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rytsh/bag/internal/config"
	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/integrate"
	"github.com/rytsh/bag/internal/pipeline"
	"github.com/rytsh/bag/internal/server"
)

func init() {
	register(command{name: "languages", usage: "list supported languages and extensions", run: runLanguages})
	register(command{name: "install", usage: "install assistant skills/rules and optional --mcp (leaves AGENTS.md / CLAUDE.md untouched)", run: runInstall})
}

func runLanguages(_ context.Context, _ []string) error {
	ls := extract.Languages()
	sort.Slice(ls, func(i, j int) bool { return ls[i].Name < ls[j].Name })

	seen := map[string]bool{}

	for _, l := range ls {
		if seen[l.Name] {
			continue
		}

		seen[l.Name] = true

		items := append(append([]string(nil), l.Extensions...), l.Filenames...)
		fmt.Printf("%-16s %s\n", l.Name, strings.Join(items, " "))
	}

	return nil
}

const agentSection = `## bag knowledge graph

This repository has a code knowledge graph at graphify-out/graph.json
(Graphify-compatible, built by bag).

- Before grepping or reading many files to answer an architecture question,
  run ` + "`bag query \"<question>\"`" + ` for a scoped subgraph.
- Use ` + "`bag path <A> <B>`" + ` to see how two concepts connect and
  ` + "`bag explain <name>`" + ` for one node's connections.
- graphify-out/GRAPH_REPORT.md summarizes god nodes, communities and
  surprising connections.
- After code changes run ` + "`bag update .`" + ` (AST only, no API cost).
- If the bag MCP server is connected, prefer its query_graph, shortest_path,
  get_node and get_neighbors tools over CLI calls. Build with extract_graph
  and refresh with update_graph. These write local graph outputs only.
`

const skillBody = `---
name: bag
description: Query the repository knowledge graph (graphify-out/graph.json) built by bag instead of grepping. Use for architecture questions, "how does X connect to Y", impact analysis, and finding where a concept lives.
---

# bag

If the bag MCP server is connected, prefer its tools over spawning CLI commands:

- extract_graph: first build of this project's graph (AST only, no LLM).
- update_graph: refresh after code changes, reusing the AST cache.
- query_graph: scoped context for a question or symbol search.
- shortest_path, get_node, get_neighbors, get_community: explore relationships.
- graph_stats, god_nodes: overview and central symbols.

Build tools write graph outputs and cache inside the configured project. Ask
before using force=true to replace a larger graph with a smaller one. No other
project or output path can be selected through tool arguments.

Without MCP, use the CLI below (run from the repository root).

Build or refresh the graph:

    bag extract .          # first build (AST only, no LLM)
    bag update .           # fast rebuild after changes

Ask the graph:

    bag query "how does auth reach the database"
    bag path AuthService Database
    bag explain RateLimiter

Read graphify-out/GRAPH_REPORT.md for the high-level map (god nodes,
communities, surprising connections). Serve it to MCP clients with
` + "`bag serve`" + ` (stdio) or ` + "`bag serve --transport http`" + `.
`

func runInstall(ctx context.Context, args []string) error {
	fs := newFlags("install")
	platform := fs.String("platform", "agents", "agents|claude|opencode|cursor|all")
	mcpEnabled := fs.Bool("mcp", false, "also configure project-local stdio MCP (claude|opencode|cursor|all)")
	replaceMCP := fs.Bool("replace-mcp", false, "replace an existing different bag MCP entry (requires --mcp)")

	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if *replaceMCP && !*mcpEnabled {
		return fmt.Errorf("--replace-mcp requires --mcp")
	}

	targets := map[string]func() error{
		"agents": func() error {
			return writeSkill(filepath.Join(".agents", "skills", "bag", "SKILL.md"))
		},
		"claude": func() error {
			return writeSkill(filepath.Join(".claude", "skills", "bag", "SKILL.md"))
		},
		"opencode": func() error {
			return writeSkill(filepath.Join(".opencode", "skills", "bag", "SKILL.md"))
		},
		"cursor": func() error {
			p := filepath.Join(".cursor", "rules", "bag.mdc")
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}

			return os.WriteFile(p, []byte("---\nalwaysApply: true\n---\n"+agentSection), 0o644)
		},
	}

	var names []string
	if *platform == "all" {
		for k := range targets {
			names = append(names, k)
		}

		sort.Strings(names)
	} else {
		if _, ok := targets[*platform]; !ok {
			return fmt.Errorf("unknown platform %q", *platform)
		}

		names = []string{*platform}
	}

	var mcpFiles []integrate.MCPFile
	if *mcpEnabled {
		root, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve project root; %w", err)
		}
		binary, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve bag executable; %w", err)
		}
		graphPath := filepath.Join(pipeline.OutDir(root, config.From(ctx).OutDir), "graph.json")
		if _, err := server.NewProjectStore(graphPath, pipeline.Options{Root: root}); err != nil {
			return fmt.Errorf("validate project MCP graph; %w", err)
		}
		mcpFiles, err = integrate.PrepareMCP(*platform, binary, root, graphPath, *replaceMCP)
		if err != nil {
			return err
		}
	}

	for _, n := range names {
		if err := targets[n](); err != nil {
			return fmt.Errorf("%s; %w", n, err)
		}

		fmt.Printf("installed bag instructions for %s\n", n)
	}
	for _, file := range mcpFiles {
		if err := file.Write(); err != nil {
			return err
		}
		fmt.Printf("configured bag stdio MCP in %s (project-local; restart/reconnect your assistant)\n", file.Path)
	}

	return nil
}

func writeSkill(p string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}

	return os.WriteFile(p, []byte(skillBody), 0o644)
}
