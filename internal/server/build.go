package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rytsh/bag/internal/pipeline"
	"github.com/rytsh/bag/internal/store"
)

// NewProjectStore enables local AST builds for one fixed project. Unlike
// NewStore, it can start before graph.json exists. No LLM calls are enabled.
func NewProjectStore(path string, opt pipeline.Options) (*Store, error) {
	root, err := filepath.Abs(opt.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve project root; %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve project root; %w", err)
	}
	st, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat project root; %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("project root is not a directory: %s", root)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve graph path; %w", err)
	}
	if filepath.Base(path) != "graph.json" {
		return nil, fmt.Errorf("writable project graph must be named graph.json")
	}
	if err := projectPath(root, path); err != nil {
		return nil, err
	}
	opt.Root, opt.OutDir = root, filepath.Dir(path)
	opt.Semantic, opt.Transcribe, opt.Tokens = nil, nil, nil
	opt.Cache = nil
	s := &Store{path: path, buildOptions: &opt}
	if err := s.reload(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load project graph; %w", err)
	}
	return s, nil
}

// projectPath verifies containment after resolving existing symlink ancestors.
func projectPath(root, path string) error {
	inside := func(p string) bool {
		rel, err := filepath.Rel(root, p)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	}
	// Canonicalize existing ancestors even when the output directory is absent.
	ancestor := path
	for {
		resolved, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			if !inside(resolved) {
				return fmt.Errorf("graph output must stay inside project root %s", root)
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("resolve graph output; %w", err)
		}
		if st, statErr := os.Lstat(ancestor); statErr == nil && st.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(ancestor)
			if err != nil {
				return fmt.Errorf("read output symlink; %w", err)
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(ancestor), target)
			}
			return projectPath(root, target)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return fmt.Errorf("cannot resolve graph output %s", path)
		}
		ancestor = parent
	}
}

type buildArgs struct {
	Force bool `json:"force,omitempty" jsonschema:"allow replacing a larger graph with a smaller graph; default false"`
	NoViz bool `json:"no_viz,omitempty" jsonschema:"skip graph.html; default false"`
}

func addBuildTools(s *mcp.Server, project *Store) {
	for _, name := range []string{"extract_graph", "update_graph"} {
		description := "Build this project's graph from local code (AST only, no API key or network). Writes graph.json, report, HTML and cache. Uses per-file AST cache."
		if name == "update_graph" {
			description = "Refresh this project's graph after code changes using the AST cache (no API key or network). Rebuilds relationships and outputs. Set force=true to accept shrinkage after deleting code."
		}
		mcp.AddTool(s, &mcp.Tool{Name: name, Description: description,
			Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPtr(true), OpenWorldHint: boolPtr(false)}},
			func(ctx context.Context, _ *mcp.CallToolRequest, a buildArgs) (*mcp.CallToolResult, any, error) {
				return project.build(ctx, a)
			})
	}
}

func boolPtr(v bool) *bool { return &v }

func (s *Store) build(ctx context.Context, a buildArgs) (*mcp.CallToolResult, any, error) {
	if !s.buildMu.TryLock() {
		return nil, nil, fmt.Errorf("a graph build is already running")
	}
	defer s.buildMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	opt := *s.buildOptions
	for _, name := range []string{"graph.json", "GRAPH_REPORT.md", "graph.html", ".graphify_root", ".graphify_analysis.json"} {
		if err := projectPath(opt.Root, filepath.Join(opt.OutDir, name)); err != nil {
			return nil, nil, err
		}
	}
	cacheDir := filepath.Join(opt.OutDir, "cache")
	if err := projectPath(opt.Root, cacheDir); err != nil {
		return nil, nil, err
	}
	cache, err := store.OpenCache(cacheDir)
	if err != nil {
		return nil, nil, fmt.Errorf("open AST cache; %w", err)
	}
	opt.Cache, opt.Force, opt.NoViz = cache, a.Force, a.NoViz
	start := time.Now()
	res, err := pipeline.Run(ctx, opt)
	if err != nil {
		return nil, nil, fmt.Errorf("build project graph; %w", err)
	}
	if err := s.reload(); err != nil {
		return nil, nil, fmt.Errorf("reload built graph; %w", err)
	}
	return text(fmt.Sprintf("Built %s: %d nodes, %d edges, %d communities in %s. Files failed or empty: %d; syntax-error files: %d. AST only, no LLM calls.",
		res.GraphPath, res.Graph.NumNodes(), res.Graph.NumEdges(), len(res.Communities), time.Since(start).Round(time.Millisecond),
		len(res.Extract.Failed), len(res.Extract.SyntaxErrored)))
}
