// Package pipeline wires detect → extract → build → cluster → analyze →
// export into a single run.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rytsh/bag/internal/analyze"
	"github.com/rytsh/bag/internal/cluster"
	"github.com/rytsh/bag/internal/detect"
	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/graph"
)

// Options configure a build.
type Options struct {
	Root               string
	OutDir             string
	Gitignore          bool
	Excludes           []string
	Workers            int
	Resolution         float64
	ExcludeHubsPercent float64
	Cache              extract.Cache
	Force              bool
	NoCluster          bool
}

// Output is the result of a pipeline run.
type Output struct {
	Graph       *graph.Graph
	Communities graph.Communities
	Labels      map[int]string
	Cohesion    map[int]float64
	Gods        []analyze.GodNode
	Surprises   []analyze.Surprise
	Detect      *detect.Result
	Extract     *extract.Result
	GraphPath   string
}

// Analysis is persisted next to graph.json (.graphify_analysis.json).
type Analysis struct {
	Communities map[string][]string `json:"communities"`
	Cohesion    map[string]float64  `json:"cohesion"`
	Gods        []analyze.GodNode   `json:"gods"`
	Surprises   []analyze.Surprise  `json:"surprises"`
	Tokens      map[string]int      `json:"tokens"`
}

// OutDir returns the output directory for root.
func OutDir(root, out string) string {
	if out == "" {
		out = detect.DefaultOutDir
	}

	if filepath.IsAbs(out) {
		return out
	}

	return filepath.Join(root, out)
}

// Run executes the full code pipeline.
func Run(ctx context.Context, opt Options) (*Output, error) {
	root, err := filepath.Abs(opt.Root)
	if err != nil {
		return nil, err
	}

	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}

	out := OutDir(root, opt.OutDir)

	det, err := detect.Detect(root, detect.Options{
		Gitignore: opt.Gitignore,
		Excludes:  opt.Excludes,
		IsCode:    extract.IsCode,
		OutDir:    out,
	})
	if err != nil {
		return nil, fmt.Errorf("detect; %w", err)
	}

	code := det.Files[detect.Code]
	slog.Info("detected corpus", "code", len(code), "docs", len(det.Files[detect.Document]),
		"papers", len(det.Files[detect.Paper]), "images", len(det.Files[detect.Image]))

	ex, err := extract.Run(ctx, code, extract.Options{Root: root, Workers: opt.Workers, Cache: opt.Cache})
	if err != nil {
		return nil, fmt.Errorf("extract; %w", err)
	}

	if len(ex.SyntaxErrored) > 0 {
		slog.Warn("files had syntax errors and may be partially extracted", "count", len(ex.SyntaxErrored))
	}

	g := graph.Build(ex.Nodes, ex.Edges, nil, root)

	o := &Output{Graph: g, Detect: det, Extract: ex}

	if !opt.NoCluster {
		o.Communities = cluster.Cluster(g, cluster.Options{Resolution: opt.Resolution, ExcludeHubsPercent: opt.ExcludeHubsPercent})
		o.Cohesion = cluster.ScoreAll(g, o.Communities)
		o.Labels = cluster.LabelByHub(g, o.Communities)
		o.Gods = analyze.GodNodes(g, 10, opt.ExcludeHubsPercent)
		o.Surprises = analyze.SurprisingConnections(g, o.Communities, 5)
	}

	o.GraphPath = filepath.Join(out, "graph.json")

	if err := o.Write(root, out, opt.Force); err != nil {
		return nil, err
	}

	return o, nil
}

// Write persists graph.json and the analysis sidecar.
func (o *Output) Write(root, out string, force bool) error {
	if !force {
		if prev, err := graph.Load(o.GraphPath); err == nil && prev.G.NumNodes() > o.Graph.NumNodes() {
			return fmt.Errorf("new graph has %d nodes but existing graph.json has %d; refusing to overwrite (use --force)",
				o.Graph.NumNodes(), prev.G.NumNodes())
		}
	}

	if err := o.Graph.WriteJSON(o.GraphPath, graph.WriteOptions{
		Communities: o.Communities,
		Commit:      gitHead(root),
	}); err != nil {
		return fmt.Errorf("write graph.json; %w", err)
	}

	an := Analysis{
		Communities: map[string][]string{},
		Cohesion:    map[string]float64{},
		Gods:        o.Gods,
		Surprises:   o.Surprises,
		Tokens:      map[string]int{"input": 0, "output": 0},
	}

	for k, v := range o.Communities {
		an.Communities[fmt.Sprint(k)] = v
	}

	for k, v := range o.Cohesion {
		an.Cohesion[fmt.Sprint(k)] = v
	}

	raw, err := json.MarshalIndent(an, "", "  ")
	if err != nil {
		return err
	}

	if err := graph.WriteFileAtomic(filepath.Join(out, ".graphify_analysis.json"), raw); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(out, ".graphify_root"), []byte(root), 0o644)
}

func gitHead(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")

	b, err := cmd.Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(b))
}
