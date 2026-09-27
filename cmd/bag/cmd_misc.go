package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rytsh/bag/internal/extract"
)

func init() {
	register(command{name: "languages", usage: "list supported languages and extensions", run: runLanguages})
	register(command{name: "install", usage: "write assistant instructions (AGENTS.md / CLAUDE.md / skill)", run: runInstall})
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
`

const (
	sectionBegin = "<!-- bag:begin -->"
	sectionEnd   = "<!-- bag:end -->"
)

func upsertSection(path, body string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	content := string(raw)
	block := sectionBegin + "\n" + body + sectionEnd + "\n"

	if i := strings.Index(content, sectionBegin); i >= 0 {
		if j := strings.Index(content[i:], sectionEnd); j >= 0 {
			content = content[:i] + block + strings.TrimLeft(content[i+j+len(sectionEnd):], "\n")
		}
	} else {
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}

		if content != "" {
			content += "\n"
		}

		content += block
	}

	return os.WriteFile(path, []byte(content), 0o644)
}

const skillBody = `---
name: bag
description: Query the repository knowledge graph (graphify-out/graph.json) built by bag instead of grepping. Use for architecture questions, "how does X connect to Y", impact analysis, and finding where a concept lives.
---

# bag

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

func runInstall(_ context.Context, args []string) error {
	fs := newFlags("install")
	platform := fs.String("platform", "agents", "agents|claude|opencode|cursor|all")

	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}

	targets := map[string]func() error{
		"agents": func() error { return upsertSection("AGENTS.md", agentSection) },
		"claude": func() error {
			if err := upsertSection("CLAUDE.md", agentSection); err != nil {
				return err
			}

			return writeSkill(filepath.Join(".claude", "skills", "bag", "SKILL.md"))
		},
		"opencode": func() error {
			if err := upsertSection("AGENTS.md", agentSection); err != nil {
				return err
			}

			return writeSkill(filepath.Join(".opencode", "skill", "bag", "SKILL.md"))
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

	for _, n := range names {
		if err := targets[n](); err != nil {
			return fmt.Errorf("%s; %w", n, err)
		}

		fmt.Printf("installed bag instructions for %s\n", n)
	}

	return nil
}

func writeSkill(p string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}

	return os.WriteFile(p, []byte(skillBody), 0o644)
}
