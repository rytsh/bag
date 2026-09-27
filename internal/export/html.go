// Package export renders the graph to other formats (HTML, GraphML,
// Cypher, Obsidian, wiki).
package export

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/rytsh/bag/internal/graph"
	"github.com/rytsh/bag/internal/model"
)

// CommunityColors is the palette shared with Graphify.
var CommunityColors = []string{
	"#4E79A7", "#F28E2B", "#E15759", "#76B7B2", "#59A14F",
	"#EDC948", "#B07AA1", "#FF9DA7", "#9C755F", "#BAB0AC",
}

// MaxNodesForViz is the default node limit for graph.html.
const MaxNodesForViz = 5000

//go:embed assets/graph.html.tmpl
var htmlTemplate string

var htmlTmpl = template.Must(template.New("graph").Delims("{{", "}}").Parse(htmlTemplate))

var controlChars = regexp.MustCompile(`[\x00-\x1f\x7f]`)

// SanitizeLabel strips control characters and caps the length at 256 runes.
func SanitizeLabel(s string) string {
	s = controlChars.ReplaceAllString(s, "")
	if r := []rune(s); len(r) > 256 {
		s = string(r[:256])
	}

	return s
}

type visNode struct {
	ID            string         `json:"id"`
	Label         string         `json:"label"`
	Color         map[string]any `json:"color"`
	Size          float64        `json:"size"`
	Font          map[string]any `json:"font"`
	Title         string         `json:"title"`
	Community     int            `json:"community"`
	CommunityName string         `json:"community_name"`
	SourceFile    string         `json:"source_file"`
	FileType      string         `json:"file_type"`
	Degree        int            `json:"degree"`
}

type visEdge struct {
	From       string         `json:"from"`
	To         string         `json:"to"`
	Label      string         `json:"label"`
	Title      string         `json:"title"`
	Dashes     bool           `json:"dashes"`
	Width      int            `json:"width"`
	Color      map[string]any `json:"color"`
	Confidence string         `json:"confidence"`
}

type legendEntry struct {
	CID   int    `json:"cid"`
	Color string `json:"color"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

// HTMLOptions configure graph.html rendering.
type HTMLOptions struct {
	Labels       map[int]string
	MemberCounts map[int]int
	NodeLimit    int
	Title        string
}

func jsSafe(v any) string {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)

	return strings.ReplaceAll(strings.TrimRight(buf.String(), "\n"), "</", `<\/`)
}

// ToHTML renders an interactive vis-network visualization. When the graph
// exceeds the node limit an aggregated community-level view is rendered.
func ToHTML(g *graph.Graph, c graph.Communities, outPath string, opt HTMLOptions) error {
	limit := opt.NodeLimit
	if limit == 0 {
		limit = MaxNodesForViz
	}

	if g.NumNodes() > limit {
		meta, mc, ok := aggregate(g, c, opt.Labels)
		if !ok {
			return nil
		}

		metaComm := graph.Communities{}
		for cid := range c {
			metaComm[cid] = []string{fmt.Sprint(cid)}
		}

		opt.MemberCounts = mc
		opt.NodeLimit = math.MaxInt

		return ToHTML(meta, metaComm, outPath, opt)
	}

	nc := c.NodeCommunity()

	maxDeg := 1
	for _, id := range g.NodeIDs() {
		if d := g.Degree(id); d > maxDeg {
			maxDeg = d
		}
	}

	maxMC := 1
	for _, v := range opt.MemberCounts {
		if v > maxMC {
			maxMC = v
		}
	}

	label := func(cid int) string {
		if l, ok := opt.Labels[cid]; ok {
			return l
		}

		return fmt.Sprintf("Community %d", cid)
	}

	nodes := make([]visNode, 0, g.NumNodes())

	for _, n := range g.Nodes() {
		cid := nc[n.ID]
		color := CommunityColors[cid%len(CommunityColors)]
		lbl := SanitizeLabel(n.Label)
		deg := g.Degree(n.ID)

		var size float64

		font := 0

		if opt.MemberCounts != nil {
			size = 10 + 30*float64(opt.MemberCounts[cid])/float64(maxMC)
			font = 12
		} else {
			size = 10 + 30*float64(deg)/float64(maxDeg)
			if float64(deg) >= float64(maxDeg)*0.15 {
				font = 12
			}
		}

		nodes = append(nodes, visNode{
			ID: n.ID, Label: lbl,
			Color: map[string]any{"background": color, "border": color,
				"highlight": map[string]any{"background": "#ffffff", "border": color}},
			Size:  math.Round(size*10) / 10,
			Font:  map[string]any{"size": font, "color": "#ffffff"},
			Title: lbl, Community: cid, CommunityName: SanitizeLabel(label(cid)),
			SourceFile: SanitizeLabel(n.SourceFile), FileType: n.FileType, Degree: deg,
		})
	}

	edges := make([]visEdge, 0)

	for _, e := range g.Edges() {
		conf := e.Confidence
		if conf == "" {
			conf = model.Extracted
		}

		w, op := 1, 0.35
		if conf == model.Extracted {
			w, op = 2, 0.7
		}

		edges = append(edges, visEdge{
			From: e.Source, To: e.Target, Label: e.Relation,
			Title:  SanitizeLabel(fmt.Sprintf("%s [%s]", e.Relation, conf)),
			Dashes: conf != model.Extracted, Width: w,
			Color: map[string]any{"opacity": op}, Confidence: conf,
		})
	}

	var cids []int
	for cid := range opt.Labels {
		cids = append(cids, cid)
	}

	sort.Ints(cids)

	legend := make([]legendEntry, 0, len(cids))

	for _, cid := range cids {
		n := len(c[cid])
		if opt.MemberCounts != nil {
			if v, ok := opt.MemberCounts[cid]; ok {
				n = v
			}
		}

		legend = append(legend, legendEntry{
			CID: cid, Color: CommunityColors[cid%len(CommunityColors)],
			Label: html.EscapeString(SanitizeLabel(label(cid))), Count: n,
		})
	}

	hyper := make([]map[string]any, 0, len(g.Hyperedges))
	for _, h := range g.Hyperedges {
		hyper = append(hyper, map[string]any{"id": h.ID, "label": h.Label, "nodes": h.Nodes})
	}

	title := opt.Title
	if title == "" {
		title = htmlTitle(outPath)
	}

	var buf bytes.Buffer
	if err := htmlTmpl.Execute(&buf, map[string]string{
		"Title":      html.EscapeString(SanitizeLabel(title)),
		"Stats":      fmt.Sprintf("%d nodes &middot; %d edges &middot; %d communities", g.NumNodes(), g.NumEdges(), len(c)),
		"Nodes":      jsSafe(nodes),
		"Edges":      jsSafe(edges),
		"Legend":     jsSafe(legend),
		"Hyperedges": jsSafe(hyper),
	}); err != nil {
		return err
	}

	return graph.WriteFileAtomic(outPath, buf.Bytes())
}

func htmlTitle(out string) string {
	parts := strings.Split(filepath.ToSlash(out), "/")
	for i, p := range parts {
		if strings.HasPrefix(p, "graphify-out") || p == "bag-out" {
			return strings.Join(parts[i:], "/")
		}
	}

	return filepath.Base(out)
}

func aggregate(g *graph.Graph, c graph.Communities, labels map[int]string) (*graph.Graph, map[int]int, bool) {
	nc := c.NodeCommunity()
	meta := graph.New()

	var cids []int
	for cid := range c {
		cids = append(cids, cid)
	}

	sort.Ints(cids)

	for _, cid := range cids {
		l, ok := labels[cid]
		if !ok {
			l = fmt.Sprintf("Community %d", cid)
		}

		meta.AddNode(&model.Node{ID: fmt.Sprint(cid), Label: l, FileType: "concept"})
	}

	counts := map[[2]int]int{}

	for _, e := range g.Edges() {
		cu, oku := nc[e.Source]
		cv, okv := nc[e.Target]

		if !oku || !okv || cu == cv {
			continue
		}

		if cu > cv {
			cu, cv = cv, cu
		}

		counts[[2]int{cu, cv}]++
	}

	for k, w := range counts {
		meta.SetEdge(&model.Edge{
			Source: fmt.Sprint(k[0]), Target: fmt.Sprint(k[1]), Weight: float64(w),
			Relation: fmt.Sprintf("%d cross-community edges", w), Confidence: "AGGREGATED",
		})
	}

	if meta.NumNodes() <= 1 {
		return nil, nil, false
	}

	mc := map[int]int{}
	for cid, ms := range c {
		mc[cid] = len(ms)
	}

	return meta, mc, true
}
