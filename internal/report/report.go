// Package report renders GRAPH_REPORT.md.
//
// Adapted from Graphify's graphify/report.py (Apache-2.0).
package report

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rytsh/bag/internal/analyze"
	"github.com/rytsh/bag/internal/graph"
	"github.com/rytsh/bag/internal/model"
)

// Input bundles everything the report needs.
type Input struct {
	Graph       *graph.Graph
	Communities graph.Communities
	Cohesion    map[int]float64
	Labels      map[int]string
	Gods        []analyze.GodNode
	Surprises   []analyze.Surprise
	Questions   []analyze.Question
	Root        string
	TotalFiles  int
	TotalWords  int
	Warning     string
	Commit      string
	InputTokens int
	OutTokens   int
	MinSize     int
	Date        time.Time
}

func pyRound(f float64) int { return int(math.RoundToEven(f)) }

func thousands(n int) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return "-" + thousands(-n)
	}

	var out []byte

	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}

		out = append(out, c)
	}

	return string(out)
}

// Generate renders the markdown report.
func Generate(in Input) string {
	g := in.Graph
	if in.MinSize == 0 {
		in.MinSize = 3
	}

	if in.Date.IsZero() {
		in.Date = time.Now()
	}

	label := func(cid int) string {
		if l, ok := in.Labels[cid]; ok {
			return l
		}

		return fmt.Sprintf("Community %d", cid)
	}

	edges := g.Edges()

	total := len(edges)
	if total == 0 {
		total = 1
	}

	counts := map[string]int{}

	var infScores []float64

	for _, e := range edges {
		c := e.Confidence
		if c == "" {
			c = model.Extracted
		}

		counts[c]++

		if c == model.Inferred {
			s := 0.5
			if e.ConfidenceScore != nil {
				s = *e.ConfidenceScore
			}

			infScores = append(infScores, s)
		}
	}

	pct := func(k string) int { return pyRound(float64(counts[k]) / float64(total) * 100) }
	extPct, infPct, ambPct := pct(model.Extracted), pct(model.Inferred), pct(model.Ambiguous)

	rootLabel := filepath.Base(filepath.ToSlash(in.Root))

	var L []string

	add := func(s ...string) { L = append(L, s...) }

	add(fmt.Sprintf("# Graph Report - %s  (%s)", rootLabel, in.Date.Format("2006-01-02")), "", "## Corpus Check")

	if in.Warning != "" {
		add("- " + in.Warning)
	} else {
		add(fmt.Sprintf("- %d files · ~%s words", in.TotalFiles, thousands(in.TotalWords)),
			"- Verdict: corpus is large enough that graph structure adds value.")
	}

	cids := sortedCIDs(in.Communities)

	realCount := func(ms []string) int {
		n := 0

		for _, m := range ms {
			if !analyze.IsFileNode(g, m) {
				n++
			}
		}

		return n
	}

	thin, shown := 0, 0

	for _, cid := range cids {
		if realCount(in.Communities[cid]) < in.MinSize {
			thin++
		} else {
			shown++
		}
	}

	summary := fmt.Sprintf("- %d nodes · %d edges · %d communities", g.NumNodes(), len(edges), len(in.Communities))
	if thin > 0 {
		summary += fmt.Sprintf(" (%d shown, %d thin omitted)", shown, thin)
	}

	ext := fmt.Sprintf("- Extraction: %d%% EXTRACTED · %d%% INFERRED · %d%% AMBIGUOUS", extPct, infPct, ambPct)

	if len(infScores) > 0 {
		sum := 0.0
		for _, s := range infScores {
			sum += s
		}

		avg := math.Round(sum/float64(len(infScores))*100) / 100
		ext += fmt.Sprintf(" · INFERRED: %d edges (avg confidence: %s)", len(infScores), trimFloat(avg))
	}

	add("", "## Summary", summary, ext,
		fmt.Sprintf("- Token cost: %s input · %s output", thousands(in.InputTokens), thousands(in.OutTokens)))

	if in.Commit != "" {
		c := in.Commit
		if len(c) > 8 {
			c = c[:8]
		}

		add("", "## Graph Freshness",
			fmt.Sprintf("- Built from commit: `%s`", c),
			"- Run `git rev-parse HEAD` and compare to check if the graph is stale.",
			"- Run `bag update .` after code changes (no API cost).")
	}

	var nonEmpty []int

	for _, cid := range cids {
		if realCount(in.Communities[cid]) > 0 {
			nonEmpty = append(nonEmpty, cid)
		}
	}

	if len(nonEmpty) > 0 {
		add("", "## Community Hubs (Navigation)")

		for _, cid := range nonEmpty {
			add("- " + label(cid))
		}
	}

	add("", "## God Nodes (most connected - your core abstractions)")

	for i, n := range in.Gods {
		add(fmt.Sprintf("%d. `%s` - %d edges", i+1, n.Label, n.Degree))
	}

	add("", "## Surprising Connections (you probably didn't know these)")

	if len(in.Surprises) == 0 {
		add("- None detected - all connections are within the same source files.")
	}

	for _, s := range in.Surprises {
		rel := s.Relation
		if rel == "" {
			rel = "related_to"
		}

		sem := ""
		if rel == "semantically_similar_to" {
			sem = " [semantically similar]"
		}

		files := s.SourceFiles
		for len(files) < 2 {
			files = append(files, "")
		}

		line2 := fmt.Sprintf("  %s → %s", files[0], files[1])
		if s.Note != "" {
			line2 += "  _" + s.Note + "_"
		}

		add(fmt.Sprintf("- `%s` --%s--> `%s`  [%s]%s", s.Source, rel, s.Target, s.Confidence, sem), line2)
	}

	hasCode := false

	for _, n := range g.Nodes() {
		if n.FileType == model.FileTypeCode {
			hasCode = true

			break
		}
	}

	if hasCode {
		add("", "## Import Cycles")

		cycles := analyze.FindImportCycles(g, 5, 20)
		if len(cycles) == 0 {
			add("- None detected.")
		}

		for _, c := range cycles {
			p := strings.Join(append(append([]string(nil), c.Cycle...), c.Cycle[0]), " -> ")
			add(fmt.Sprintf("- %d-file cycle: `%s`", c.Length, p))
		}
	}

	if len(g.Hyperedges) > 0 {
		add("", "## Hyperedges (group relationships)")

		for _, h := range g.Hyperedges {
			l := h.Label
			if l == "" {
				l = h.ID
			}

			conf, _ := h.Extra["confidence"].(string)
			if conf == "" {
				conf = model.Inferred
			}

			add(fmt.Sprintf("- **%s** — %s [%s]", l, strings.Join(h.Nodes, ", "), conf))
		}
	}

	add("", fmt.Sprintf("## Communities (%d total, %d thin omitted)", len(in.Communities), thin))

	for _, cid := range cids {
		var real []string

		for _, m := range in.Communities[cid] {
			if !analyze.IsFileNode(g, m) {
				real = append(real, m)
			}
		}

		if len(real) == 0 || len(real) < in.MinSize {
			continue
		}

		var disp []string

		for i := 0; i < len(real) && i < 8; i++ {
			disp = append(disp, g.Node(real[i]).Label)
		}

		suffix := ""
		if len(real) > 8 {
			suffix = fmt.Sprintf(" (+%d more)", len(real)-8)
		}

		add("", fmt.Sprintf("### Community %d - \"%s\"", cid, label(cid)),
			fmt.Sprintf("Cohesion: %.2f", in.Cohesion[cid]),
			fmt.Sprintf("Nodes (%d): %s%s", len(real), strings.Join(disp, ", "), suffix))
	}

	var amb []*model.Edge

	for _, e := range edges {
		if e.Confidence == model.Ambiguous {
			amb = append(amb, e)
		}
	}

	if len(amb) > 0 {
		add("", "## Ambiguous Edges - Review These")

		for _, e := range amb {
			rel := e.Relation
			if rel == "" {
				rel = "unknown"
			}

			add(fmt.Sprintf("- `%s` → `%s`  [AMBIGUOUS]", g.Node(e.Source).Label, g.Node(e.Target).Label),
				fmt.Sprintf("  %s · relation: %s", e.SourceFile, rel))
		}
	}

	var isolated []string

	rawIsolated := 0

	for _, id := range g.NodeIDs() {
		if g.Degree(id) <= 1 {
			rawIsolated++

			if !analyze.IsFileNode(g, id) && !analyze.IsConceptNode(g, id) && g.Node(id).FileType != model.FileTypeRationale {
				isolated = append(isolated, id)
			}
		}
	}

	thinComms := 0

	for _, cid := range cids {
		if realCount(in.Communities[cid]) < in.MinSize {
			thinComms++
		}
	}

	if len(isolated)+thinComms > 0 || ambPct > 20 {
		add("", "## Knowledge Gaps")

		if len(isolated) > 0 {
			var ls []string
			for i := 0; i < len(isolated) && i < 5; i++ {
				ls = append(ls, "`"+g.Node(isolated[i]).Label+"`")
			}

			suffix := ""
			if len(isolated) > 5 {
				suffix = fmt.Sprintf(" (+%d more)", len(isolated)-5)
			}

			semantic := false

			for _, n := range g.Nodes() {
				if n.FileType == model.FileTypeDocument || n.FileType == model.FileTypePaper || n.FileType == model.FileTypeImage {
					semantic = true

					break
				}
			}

			reason := "possible missing edges"
			if semantic {
				reason = "possible missing edges or undocumented components"
			}

			add(fmt.Sprintf("- **%d isolated node(s):** %s%s", len(isolated), strings.Join(ls, ", "), suffix),
				fmt.Sprintf("  These have ≤1 connection - %s. (Counts symbols only; %d node(s) total have ≤1 connection when file, concept and rationale nodes are included.)", reason, rawIsolated))
		}

		if thinComms > 0 {
			add(fmt.Sprintf("- **%d thin communities (<%d nodes) omitted from report** — run `bag query` to explore isolated nodes.", thinComms, in.MinSize))
		}

		if ambPct > 20 {
			add(fmt.Sprintf("- **High ambiguity: %d%% of edges are AMBIGUOUS.** Review the Ambiguous Edges section above.", ambPct))
		}
	}

	if len(in.Questions) > 0 {
		add("", "## Suggested Questions")

		if len(in.Questions) == 1 && in.Questions[0].Type == "no_signal" {
			add("_" + in.Questions[0].Why + "_")
		} else {
			add("_Questions this graph is uniquely positioned to answer:_", "")

			for _, q := range in.Questions {
				if q.Question != "" {
					add("- **"+q.Question+"**", "  _"+q.Why+"_")
				}
			}
		}
	}

	return strings.Join(L, "\n")
}

func sortedCIDs(c graph.Communities) []int {
	out := make([]int, 0, len(c))
	for k := range c {
		out = append(out, k)
	}

	sort.Ints(out)

	return out
}

func trimFloat(f float64) string {
	s := fmt.Sprintf("%v", f)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}

	return s
}
