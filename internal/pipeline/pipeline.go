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
	"unicode"

	"github.com/rytsh/bag/internal/analyze"
	"github.com/rytsh/bag/internal/cluster"
	"github.com/rytsh/bag/internal/detect"
	"github.com/rytsh/bag/internal/export"
	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/graph"
	"github.com/rytsh/bag/internal/model"
	"github.com/rytsh/bag/internal/report"
	"github.com/rytsh/bag/internal/semantic"
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
	NoViz              bool
	NoReport           bool
	// Semantic, when set, extracts non-code files (docs/papers/images).
	Semantic func(ctx context.Context, det *detect.Result) (*model.Extraction, error)
	// Transcribe, when set together with Semantic, turns video/audio files
	// into transcripts that the semantic pass reads as documents. prompt is
	// the Whisper domain hint built from the AST god nodes.
	Transcribe func(ctx context.Context, files []string, prompt string) []string
	// Tokens receives the semantic token usage (input, output).
	Tokens *[2]int
}

// Output is the result of a pipeline run.
type Output struct {
	Graph       *graph.Graph
	Communities graph.Communities
	Labels      map[int]string
	Cohesion    map[int]float64
	Gods        []analyze.GodNode
	Surprises   []analyze.Surprise
	Questions   []analyze.Question
	Detect      *detect.Result
	Extract     *extract.Result
	GraphPath   string
	Root        string
	OutDir      string
	Tokens      [2]int
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

// Run executes the full pipeline.
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

	code := append([]string(nil), det.Files[detect.Code]...)
	for _, d := range det.Files[detect.Document] {
		if extract.HasExtractor(d) {
			code = append(code, d)
		}
	}

	slog.Info("detected corpus", "code", len(code), "docs", len(det.Files[detect.Document]),
		"papers", len(det.Files[detect.Paper]), "images", len(det.Files[detect.Image]),
		"video", len(det.Files[detect.Video]))

	ex, err := extract.Run(ctx, code, extract.Options{Root: root, Workers: opt.Workers, Cache: opt.Cache})
	if err != nil {
		return nil, fmt.Errorf("extract; %w", err)
	}

	if len(ex.SyntaxErrored) > 0 {
		slog.Warn("files had syntax errors and may be partially extracted", "count", len(ex.SyntaxErrored))
	}

	nodes, edges := ex.Nodes, ex.Edges

	var hyper []*model.Hyperedge

	o := &Output{Detect: det, Extract: ex, Root: root, OutDir: out}

	if opt.Semantic != nil {
		if opt.Transcribe != nil && len(det.Files[detect.Video]) > 0 {
			var labels []string
			for _, g := range analyze.GodNodes(graph.Build(nodes, edges, nil, root), 10, 0) {
				labels = append(labels, g.Label)
			}

			ts := opt.Transcribe(ctx, det.Files[detect.Video], semantic.BuildWhisperPrompt(labels))
			slog.Info("transcribed media, treating as docs", "count", len(ts))
			det.Files[detect.Document] = append(det.Files[detect.Document], ts...)
		}

		sem, err := opt.Semantic(ctx, det)
		if err != nil {
			slog.Warn("semantic extraction failed", "error", err)
		} else if sem != nil {
			nodes = append(nodes, sem.Nodes...)
			edges = append(edges, sem.Edges...)
			hyper = sem.Hyperedges
		}
	}

	if opt.Tokens != nil {
		o.Tokens = *opt.Tokens
	}

	o.Graph = graph.Build(nodes, edges, hyper, root)
	o.GraphPath = filepath.Join(out, "graph.json")

	if !opt.NoCluster {
		o.Analyze(opt.Resolution, opt.ExcludeHubsPercent)
	}

	if err := o.Write(opt.Force); err != nil {
		return nil, err
	}

	if !opt.NoCluster && !opt.NoReport {
		if err := o.WriteReport(); err != nil {
			return nil, err
		}
	}

	if !opt.NoCluster && !opt.NoViz {
		if err := export.ToHTML(o.Graph, o.Communities, filepath.Join(out, "graph.html"), export.HTMLOptions{Labels: o.Labels}); err != nil {
			slog.Warn("graph.html not written", "error", err)
		}
	}

	return o, nil
}

// Analyze clusters and computes insights on o.Graph.
func (o *Output) Analyze(resolution, excludeHubs float64) {
	g := o.Graph
	o.Communities = cluster.Cluster(g, cluster.Options{Resolution: resolution, ExcludeHubsPercent: excludeHubs})
	o.Cohesion = cluster.ScoreAll(g, o.Communities)
	o.Labels = cluster.LabelByHub(g, o.Communities)
	o.Gods = analyze.GodNodes(g, 10, excludeHubs)
	o.Surprises = analyze.SurprisingConnections(g, o.Communities, 5)
	o.Questions = analyze.SuggestQuestions(g, o.Communities, o.Labels, 7)
}

// Write persists graph.json and the analysis sidecar.
func (o *Output) Write(force bool) error {
	if !force {
		if prev, err := graph.Load(o.GraphPath); err == nil && prev.G.NumNodes() > o.Graph.NumNodes() {
			return fmt.Errorf("new graph has %d nodes but existing graph.json has %d; refusing to overwrite (use --force)",
				o.Graph.NumNodes(), prev.G.NumNodes())
		}
	}

	if err := o.Graph.WriteJSON(o.GraphPath, graph.WriteOptions{
		Communities: o.Communities,
		Labels:      o.Labels,
		Commit:      GitHead(o.Root),
	}); err != nil {
		return fmt.Errorf("write graph.json; %w", err)
	}

	an := Analysis{
		Communities: map[string][]string{},
		Cohesion:    map[string]float64{},
		Gods:        o.Gods,
		Surprises:   o.Surprises,
		Tokens:      map[string]int{"input": o.Tokens[0], "output": o.Tokens[1]},
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

	if err := graph.WriteFileAtomic(filepath.Join(o.OutDir, ".graphify_analysis.json"), raw); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(o.OutDir, ".graphify_root"), []byte(o.Root), 0o644)
}

// WriteReport renders GRAPH_REPORT.md.
func (o *Output) WriteReport() error {
	in := report.Input{
		Graph: o.Graph, Communities: o.Communities, Cohesion: o.Cohesion, Labels: o.Labels,
		Gods: o.Gods, Surprises: o.Surprises, Questions: o.Questions, Root: o.Root,
		Commit: GitHead(o.Root), InputTokens: o.Tokens[0], OutTokens: o.Tokens[1],
	}

	if o.Detect != nil {
		in.TotalFiles = o.Detect.TotalFiles
		in.TotalWords = CountWords(o.Detect)
	} else {
		in.Warning = "cluster-only mode — file stats not available"
	}

	return graph.WriteFileAtomic(filepath.Join(o.OutDir, "GRAPH_REPORT.md"), []byte(report.Generate(in)))
}

// FromGraph re-analyzes an existing graph.json (cluster-only).
func FromGraph(graphPath string) (*Output, error) {
	l, err := graph.Load(graphPath)
	if err != nil {
		return nil, err
	}

	out := filepath.Dir(graphPath)

	root := filepath.Dir(out)
	if raw, err := os.ReadFile(filepath.Join(out, ".graphify_root")); err == nil {
		root = strings.TrimSpace(string(raw))
	}

	return &Output{Graph: l.G, Communities: l.Communities, Labels: l.Labels, GraphPath: graphPath, OutDir: out, Root: root}, nil
}

// CountWords approximates the corpus word count.
func CountWords(det *detect.Result) int {
	total := 0

	for _, files := range det.Files {
		for _, f := range files {
			st, err := os.Stat(f)
			if err != nil || st.Size() > 5<<20 {
				continue
			}

			raw, err := os.ReadFile(f)
			if err != nil {
				continue
			}

			total += len(strings.FieldsFunc(string(raw), unicode.IsSpace))
		}
	}

	return total
}

// GitHead returns HEAD for the repo containing dir, or "".
func GitHead(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")

	b, err := cmd.Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(b))
}
