package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rytsh/bag/internal/analyze"
	"github.com/rytsh/bag/internal/config"
	"github.com/rytsh/bag/internal/export"
	"github.com/rytsh/bag/internal/graph"
	"github.com/rytsh/bag/internal/pipeline"
	"github.com/rytsh/bag/internal/query"
)

func init() {
	register(command{name: "query", usage: "answer a question with a scoped subgraph", run: runQuery})
	register(command{name: "path", usage: "shortest path between two concepts", run: runPath})
	register(command{name: "explain", usage: "explain one node and its connections", run: runExplain})
	register(command{name: "stats", usage: "print graph statistics", run: runStats})
	register(command{name: "cluster-only", usage: "re-cluster an existing graph.json and rewrite the report", run: runClusterOnly})
	register(command{name: "export", usage: "export graph.json to html|graphml|cypher|wiki|obsidian", run: runExport})
	register(command{name: "diff", usage: "compare two graph.json files", run: runDiff})
}

// resolveGraphPath finds graph.json from a flag, a directory, or cwd.
func resolveGraphPath(ctx context.Context, p string) string {
	if p != "" {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			if _, err := os.Stat(filepath.Join(p, "graph.json")); err == nil {
				return filepath.Join(p, "graph.json")
			}

			return filepath.Join(p, config.From(ctx).OutDir, "graph.json")
		}

		return p
	}

	out := config.From(ctx).OutDir

	cwd, _ := os.Getwd()
	for dir := cwd; ; {
		cand := filepath.Join(dir, out, "graph.json")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Join(cwd, out, "graph.json")
		}

		dir = parent
	}
}

func loadEngine(ctx context.Context, p string) (*query.Engine, *graph.Loaded, error) {
	path := resolveGraphPath(ctx, p)

	l, err := graph.Load(path)
	if err != nil {
		return nil, nil, fmt.Errorf("load %s; %w (run `bag extract` first)", path, err)
	}

	return query.New(l.G, l.Communities, l.Labels), l, nil
}

func runQuery(ctx context.Context, args []string) error {
	fs := newFlags("query")
	gp := fs.String("graph", "", "path to graph.json (default: nearest graphify-out/graph.json)")
	depth := fs.Int("depth", 2, "traversal depth")
	dfs := fs.Bool("dfs", false, "depth-first traversal")
	budget := fs.Int("budget", 2000, "approximate token budget of the answer")

	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}

	if len(pos) == 0 {
		return fmt.Errorf("usage: bag query \"<question>\"")
	}

	e, _, err := loadEngine(ctx, *gp)
	if err != nil {
		return err
	}

	fmt.Println(e.Query(strings.Join(pos, " "), query.Options{Depth: *depth, DFS: *dfs, Budget: *budget}))

	return nil
}

func runPath(ctx context.Context, args []string) error {
	fs := newFlags("path")
	gp := fs.String("graph", "", "path to graph.json")

	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}

	if len(pos) != 2 {
		return fmt.Errorf("usage: bag path <from> <to>")
	}

	e, _, err := loadEngine(ctx, *gp)
	if err != nil {
		return err
	}

	fmt.Println(e.Path(pos[0], pos[1]))

	return nil
}

func runExplain(ctx context.Context, args []string) error {
	fs := newFlags("explain")
	gp := fs.String("graph", "", "path to graph.json")

	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}

	if len(pos) == 0 {
		return fmt.Errorf("usage: bag explain <concept>")
	}

	e, _, err := loadEngine(ctx, *gp)
	if err != nil {
		return err
	}

	fmt.Println(e.Explain(strings.Join(pos, " ")))

	return nil
}

func runStats(ctx context.Context, args []string) error {
	fs := newFlags("stats")
	gp := fs.String("graph", "", "path to graph.json")
	asJSON := fs.Bool("json", false, "print JSON")

	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}

	_, l, err := loadEngine(ctx, *gp)
	if err != nil {
		return err
	}

	st := Stats(l)

	if *asJSON {
		raw, _ := json.MarshalIndent(st, "", "  ")
		fmt.Println(string(raw))

		return nil
	}

	fmt.Printf("nodes: %d\nedges: %d\ncommunities: %d\n", st.Nodes, st.Edges, st.Communities)

	for _, k := range sortedMapKeys(st.Confidence) {
		fmt.Printf("  %-10s %d\n", k, st.Confidence[k])
	}

	fmt.Println("relations:")

	for _, k := range sortedMapKeys(st.Relations) {
		fmt.Printf("  %-18s %d\n", k, st.Relations[k])
	}

	return nil
}

// GraphStats summarizes a graph.
type GraphStats struct {
	Nodes       int            `json:"nodes"`
	Edges       int            `json:"edges"`
	Communities int            `json:"communities"`
	Confidence  map[string]int `json:"confidence"`
	Relations   map[string]int `json:"relations"`
	FileTypes   map[string]int `json:"file_types"`
}

// Stats computes GraphStats.
func Stats(l *graph.Loaded) GraphStats {
	st := GraphStats{
		Nodes: l.G.NumNodes(), Communities: len(l.Communities),
		Confidence: map[string]int{}, Relations: map[string]int{}, FileTypes: map[string]int{},
	}

	for _, e := range l.G.Edges() {
		st.Edges++
		st.Confidence[e.Confidence]++
		st.Relations[e.Relation]++
	}

	for _, n := range l.G.Nodes() {
		st.FileTypes[n.FileType]++
	}

	return st
}

func sortedMapKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}

func runClusterOnly(ctx context.Context, args []string) error {
	fs := newFlags("cluster-only")
	resolution := fs.Float64("resolution", 1.0, "community resolution")
	excludeHubs := fs.Float64("exclude-hubs", 0, "exclude nodes above this degree percentile")
	noViz := fs.Bool("no-viz", false, "skip graph.html")

	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}

	target := ""
	if len(pos) > 0 {
		target = pos[0]
	}

	o, err := pipeline.FromGraph(resolveGraphPath(ctx, target))
	if err != nil {
		return err
	}

	o.Analyze(*resolution, *excludeHubs)

	if err := o.Write(true); err != nil {
		return err
	}

	if err := o.WriteReport(); err != nil {
		return err
	}

	if !*noViz {
		if err := export.ToHTML(o.Graph, o.Communities, filepath.Join(o.OutDir, "graph.html"), export.HTMLOptions{Labels: o.Labels}); err != nil {
			fmt.Fprintln(os.Stderr, "graph.html not written:", err)
		}
	}

	fmt.Printf("[bag cluster-only] %d communities. GRAPH_REPORT.md, graph.json and graph.html updated.\n", len(o.Communities))

	return nil
}

func runExport(ctx context.Context, args []string) error {
	fs := newFlags("export")
	gp := fs.String("graph", "", "path to graph.json")
	out := fs.String("out", "", "output path (default: next to graph.json)")

	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}

	if len(pos) == 0 {
		return fmt.Errorf("usage: bag export <html|graphml|cypher|wiki|obsidian> [--out PATH]")
	}

	path := resolveGraphPath(ctx, *gp)

	l, err := graph.Load(path)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	dest := func(def string) string {
		if *out != "" {
			return *out
		}

		return filepath.Join(dir, def)
	}

	switch pos[0] {
	case "html":
		err = export.ToHTML(l.G, l.Communities, dest("graph.html"), export.HTMLOptions{Labels: l.Labels})
	case "graphml":
		err = export.ToGraphML(l.G, l.Communities, dest("graph.graphml"))
	case "cypher":
		err = export.ToCypher(l.G, dest("cypher.txt"))
	case "wiki":
		var n int

		n, err = export.ToWiki(export.WikiInput{
			Graph: l.G, Communities: l.Communities, Labels: l.Labels,
			Gods: analyze.GodNodes(l.G, 10, 0),
		}, dest("wiki"))
		if err == nil {
			fmt.Printf("wrote %d wiki articles\n", n)
		}
	case "obsidian":
		var n int

		n, err = export.ToObsidian(l.G, l.Communities, l.Labels, dest("obsidian"))
		if err == nil {
			fmt.Printf("wrote %d obsidian notes\n", n)
		}
	default:
		return fmt.Errorf("unknown export format %q", pos[0])
	}

	return err
}

func runDiff(_ context.Context, args []string) error {
	fs := newFlags("diff")

	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}

	if len(pos) != 2 {
		return fmt.Errorf("usage: bag diff <old graph.json> <new graph.json>")
	}

	a, err := graph.Load(pos[0])
	if err != nil {
		return err
	}

	b, err := graph.Load(pos[1])
	if err != nil {
		return err
	}

	d := analyze.GraphDiff(a.G, b.G)
	raw, _ := json.MarshalIndent(d, "", "  ")
	fmt.Println(string(raw))

	return nil
}
