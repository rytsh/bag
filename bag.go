// Package bag builds Graphify-compatible code knowledge graphs without a
// subprocess. It is the supported library entry point for embedding bag in Go
// applications; command-specific wiring remains under cmd/bag.
package bag

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/rytsh/bag/internal/extract/langs"
	"github.com/rytsh/bag/internal/merge"
	"github.com/rytsh/bag/internal/pipeline"
	"github.com/rytsh/bag/internal/store"
)

// EngineVersion identifies the AST extraction/cache contract. Applications
// can persist this value beside graph.json and rebuild when it changes.
const EngineVersion = store.ASTVersion

// BuildOptions configures an AST-only graph build.
type BuildOptions struct {
	// OutDir is absolute or relative to Root. Empty uses graphify-out.
	OutDir string
	// Excludes are additional gitignore-style patterns anchored at Root.
	Excludes []string
	// Workers bounds concurrent file extraction. Zero uses GOMAXPROCS.
	Workers int
	// Resolution controls community granularity. Zero uses the clustering
	// implementation's default.
	Resolution float64
	// ExcludeHubsPercent excludes high-degree hubs from clustering.
	ExcludeHubsPercent float64
	// Force permits replacing an existing graph with a smaller graph.
	Force bool
	// NoGitignore disables .gitignore handling. .graphifyignore and .bagignore
	// remain active for compatibility.
	NoGitignore bool
	// NoCache disables the content-addressed AST cache.
	NoCache bool
	// NoCluster skips community detection and analysis.
	NoCluster bool
	// NoVisualization skips graph.html.
	NoVisualization bool
	// NoReport skips GRAPH_REPORT.md.
	NoReport bool
}

// BuildResult summarizes files produced by Build.
type BuildResult struct {
	Root          string
	OutDir        string
	GraphPath     string
	ReportPath    string
	HTMLPath      string
	Nodes         int
	Edges         int
	Communities   int
	Extracted     int
	Failed        int
	SyntaxErrored int
}

// Build extracts root and writes a Graphify-compatible graph.json. It performs
// no LLM or network calls.
func Build(ctx context.Context, root string, opt BuildOptions) (*BuildResult, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root; %w", err)
	}

	st, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat root; %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("root is not a directory: %s", root)
	}

	outDir := pipeline.OutDir(root, opt.OutDir)
	popt := pipeline.Options{
		Root:               root,
		OutDir:             outDir,
		Gitignore:          !opt.NoGitignore,
		Excludes:           append([]string(nil), opt.Excludes...),
		Workers:            opt.Workers,
		Resolution:         opt.Resolution,
		ExcludeHubsPercent: opt.ExcludeHubsPercent,
		Force:              opt.Force,
		NoCluster:          opt.NoCluster,
		NoViz:              opt.NoVisualization,
		NoReport:           opt.NoReport,
	}

	if !opt.NoCache {
		cache, err := store.OpenCache(filepath.Join(outDir, "cache"))
		if err != nil {
			return nil, fmt.Errorf("open AST cache; %w", err)
		}
		popt.Cache = cache
	}

	out, err := pipeline.Run(ctx, popt)
	if err != nil {
		return nil, fmt.Errorf("build graph; %w", err)
	}

	return &BuildResult{
		Root:          out.Root,
		OutDir:        out.OutDir,
		GraphPath:     out.GraphPath,
		ReportPath:    filepath.Join(out.OutDir, "GRAPH_REPORT.md"),
		HTMLPath:      filepath.Join(out.OutDir, "graph.html"),
		Nodes:         out.Graph.NumNodes(),
		Edges:         out.Graph.NumEdges(),
		Communities:   len(out.Communities),
		Extracted:     len(out.Extract.Extracted),
		Failed:        len(out.Extract.Failed),
		SyntaxErrored: len(out.Extract.SyntaxErrored),
	}, nil
}

// MergeResult summarizes a cross-repository graph merge.
type MergeResult struct {
	Path  string
	Nodes int
	Edges int
}

// MergeGraphs merges two or more Graphify-compatible graph.json files. Node
// IDs are repo-prefixed, external stubs remain global, community IDs are made
// disjoint, and supported cross-repository type/call links are added.
func MergeGraphs(ctx context.Context, out string, graphs ...string) (*MergeResult, error) {
	result, err := merge.Graphs(ctx, out, graphs...)
	if err != nil {
		return nil, fmt.Errorf("merge graphs; %w", err)
	}

	return &MergeResult{Path: result.Path, Nodes: result.Nodes, Edges: result.Edges}, nil
}

// GraphPath returns graph.json's compatibility path under a repository.
func GraphPath(root string) string { return filepath.Join(root, "graphify-out", "graph.json") }

// ReportPath returns GRAPH_REPORT.md's compatibility path under a repository.
func ReportPath(root string) string {
	return filepath.Join(root, "graphify-out", "GRAPH_REPORT.md")
}

// HTMLPath returns graph.html's compatibility path under a repository.
func HTMLPath(root string) string { return filepath.Join(root, "graphify-out", "graph.html") }
