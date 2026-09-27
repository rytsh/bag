package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rytsh/bag/internal/config"
	"github.com/rytsh/bag/internal/pipeline"
	"github.com/rytsh/bag/internal/store"
)

type stringList []string

func (s *stringList) String() string     { return fmt.Sprint(*s) }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func init() {
	register(command{
		name:  "extract",
		usage: "build graph.json for a directory (AST only, no LLM)",
		run:   runExtract,
	})
}

func runExtract(ctx context.Context, args []string) error {
	cfg := config.From(ctx)

	fs := newFlags("extract")
	out := fs.String("out", cfg.OutDir, "output directory (relative to the target)")
	noGitignore := fs.Bool("no-gitignore", false, "do not honor .gitignore")
	workers := fs.Int("max-workers", cfg.Workers, "parallel extraction workers")
	resolution := fs.Float64("resolution", 1.0, "community resolution (>1 = more, smaller communities)")
	excludeHubs := fs.Float64("exclude-hubs", 0, "exclude nodes above this degree percentile from clustering")
	force := fs.Bool("force", false, "overwrite even if the new graph is smaller")
	noCache := fs.Bool("no-cache", false, "disable the per-file AST cache")
	noCluster := fs.Bool("no-cluster", false, "skip community detection")

	var excludes stringList
	fs.Var(&excludes, "exclude", "extra gitignore-style exclude pattern (repeatable)")

	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}

	target := "."
	if len(pos) > 0 {
		target = pos[0]
	}

	root, err := filepath.Abs(target)
	if err != nil {
		return err
	}

	if _, err := os.Stat(root); err != nil {
		return fmt.Errorf("path not found: %s", root)
	}

	outDir := pipeline.OutDir(root, *out)

	opt := pipeline.Options{
		Root:               root,
		OutDir:             outDir,
		Gitignore:          !*noGitignore,
		Excludes:           excludes,
		Workers:            *workers,
		Resolution:         *resolution,
		ExcludeHubsPercent: *excludeHubs,
		Force:              *force,
		NoCluster:          *noCluster,
	}

	if !*noCache {
		c, err := store.OpenCache(filepath.Join(outDir, "cache"))
		if err == nil {
			opt.Cache = c
		}
	}

	res, err := pipeline.Run(ctx, opt)
	if err != nil {
		return err
	}

	fmt.Printf("[bag extract] wrote %s: %d nodes, %d edges, %d communities\n",
		res.GraphPath, res.Graph.NumNodes(), res.Graph.NumEdges(), len(res.Communities))

	if len(res.Extract.Failed) > 0 {
		fmt.Printf("[bag extract] %d file(s) failed or produced no nodes\n", len(res.Extract.Failed))
	}

	return nil
}
