// Package analyze computes structural insights on the graph: god nodes,
// surprising connections, suggested questions, import cycles and diffs.
//
// Adapted from Graphify's graphify/analyze.py (Apache-2.0).
package analyze

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rytsh/bag/internal/cluster"
	"github.com/rytsh/bag/internal/detect"
	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/graph"
	"github.com/rytsh/bag/internal/model"
)

var builtinNoiseLabels = base.NewSet(
	"str", "int", "float", "bool", "bytes", "bytearray", "complex", "object",
	"True", "False",
	"MagicMock", "Mock", "AsyncMock", "NonCallableMock",
	"NonCallableMagicMock", "PropertyMock", "patch", "sentinel",
	"Path", "Any", "Optional", "List", "Dict", "Set", "Tuple", "Union",
	"Callable", "Type", "ClassVar", "Final", "Literal", "Protocol",
	"Counter", "defaultdict", "OrderedDict", "datetime", "Enum",
	"os", "sys", "re", "json", "io", "abc", "typing",
	"Foundation", "SwiftUI", "UIKit", "AppKit", "Combine",
	"String", "Int", "Double", "Float", "Bool", "Data", "URL", "Date", "UUID",
	"Sendable", "Codable", "Decodable", "Encodable", "Equatable", "Hashable",
	"Identifiable", "Comparable", "AnyObject", "Error", "LocalizedError",
	"NSObject", "NSString", "NSError", "NSLock",
	"View", "Color", "Font", "DispatchQueue",
)

var jsonNoiseLabels = base.NewSet(
	"start", "end", "name", "id", "type", "properties",
	"value", "key", "data", "items", "title", "description", "version",
	"dependencies", "devdependencies", "peerdependencies",
	"optionaldependencies", "bundleddependencies", "bundledependencies",
)

var langFamily = func() map[string]string {
	m := map[string]string{}
	add := func(fam string, exts ...string) {
		for _, e := range exts {
			m[e] = fam
		}
	}

	add("python", ".py", ".pyw")
	add("js", ".js", ".jsx", ".mjs", ".cjs", ".ejs", ".ts", ".tsx", ".mts", ".cts", ".vue", ".svelte")
	add("go", ".go")
	add("rust", ".rs")
	add("jvm", ".java", ".kt", ".kts", ".scala")
	add("c", ".c", ".h", ".cpp", ".cc", ".cxx", ".hpp")
	add("ruby", ".rb", ".rake")
	add("swift", ".swift")
	add("dotnet", ".cs", ".vb")
	add("php", ".php")
	add("r", ".r")
	add("cobol", ".cbl", ".cob", ".cobol", ".cpy")
	add("solidity", ".sol")
	add("erlang", ".erl", ".hrl", ".escript")

	return m
}()

func crossLanguage(a, b string) bool {
	fa, oka := langFamily[strings.ToLower(base.Suffix(a))]
	fb, okb := langFamily[strings.ToLower(base.Suffix(b))]

	return oka && okb && fa != fb
}

// IsFileNode reports whether id is a file hub or a synthetic method stub.
func IsFileNode(g *graph.Graph, id string) bool {
	n := g.Node(id)
	if n == nil || n.Label == "" {
		return false
	}

	if n.SourceFile != "" && graph.IsFileNodeLabel(n.Label, n.SourceFile) {
		return true
	}

	if strings.HasPrefix(n.Label, ".") && strings.HasSuffix(n.Label, "()") {
		return true
	}

	return strings.HasSuffix(n.Label, "()") && g.Degree(id) <= 1
}

// IsConceptNode reports whether id is a sourceless or pathless concept.
func IsConceptNode(g *graph.Graph, id string) bool {
	n := g.Node(id)
	if n == nil || n.SourceFile == "" {
		return true
	}

	parts := strings.Split(n.SourceFile, "/")

	return !strings.Contains(parts[len(parts)-1], ".")
}

func isJSONKeyNode(g *graph.Graph, id string) bool {
	n := g.Node(id)
	if !strings.HasSuffix(strings.ToLower(n.SourceFile), ".json") {
		return false
	}

	return jsonNoiseLabels.Has(strings.ToLower(strings.TrimSpace(n.Label)))
}

// GodNode is a highly connected entity.
type GodNode struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Degree int    `json:"degree"`
}

// GodNodes returns the top-N most connected real entities.
func GodNodes(g *graph.Graph, topN int, excludeHubsPercent float64) []GodNode {
	ids := g.NodeIDs()

	threshold := -1
	if excludeHubsPercent > 0 {
		degs := make([]int, 0, len(ids))
		for _, id := range ids {
			degs = append(degs, g.Degree(id))
		}

		sort.Ints(degs)

		if len(degs) > 0 {
			idx := int(float64(len(degs))*excludeHubsPercent/100) - 1
			if idx < 0 {
				idx = 0
			}

			threshold = degs[idx]
		}
	}

	sort.SliceStable(ids, func(i, j int) bool { return g.Degree(ids[i]) > g.Degree(ids[j]) })

	var out []GodNode

	for _, id := range ids {
		d := g.Degree(id)
		if threshold >= 0 && d > threshold {
			continue
		}

		if IsFileNode(g, id) || IsConceptNode(g, id) || isJSONKeyNode(g, id) {
			continue
		}

		if builtinNoiseLabels.Has(g.Node(id).Label) {
			continue
		}

		out = append(out, GodNode{ID: id, Label: g.Node(id).Label, Degree: d})
		if len(out) >= topN {
			break
		}
	}

	return out
}

// Surprise is an unexpected connection.
type Surprise struct {
	Source      string   `json:"source"`
	Target      string   `json:"target"`
	SourceFiles []string `json:"source_files"`
	Confidence  string   `json:"confidence"`
	Relation    string   `json:"relation"`
	Why         string   `json:"why,omitempty"`
	Note        string   `json:"note,omitempty"`

	score int
	pair  [2]int
}

// SurprisingConnections finds non-obvious cross-file or cross-community edges.
func SurprisingConnections(g *graph.Graph, c graph.Communities, topN int) []Surprise {
	files := map[string]bool{}

	for _, n := range g.Nodes() {
		if n.SourceFile != "" {
			files[n.SourceFile] = true
		}
	}

	if len(files) > 1 {
		return crossFileSurprises(g, c, topN)
	}

	return crossCommunitySurprises(g, c, topN)
}

func fileCategory(p string) string {
	ext := ""
	if i := strings.LastIndexByte(p, '.'); i >= 0 {
		ext = strings.ToLower(p[i:])
	}

	switch {
	case extract.IsCode(p):
		return "code"
	case detect.PaperExtensions[ext]:
		return "paper"
	case detect.ImageExtensions[ext]:
		return "image"
	}

	return "doc"
}

func topLevelDir(p string) string {
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}

	return p
}

var structural = base.NewSet("imports", "imports_from", "contains", "method")

func surpriseScore(g *graph.Graph, e *model.Edge, nc map[string]int) (int, []string) {
	u, v := e.Source, e.Target
	us, vs := g.Node(u).SourceFile, g.Node(v).SourceFile

	var reasons []string

	conf := e.Confidence
	if conf == "" {
		conf = model.Extracted
	}

	bonus := map[string]int{model.Ambiguous: 3, model.Inferred: 2, model.Extracted: 1}[conf]
	if bonus == 0 {
		bonus = 1
	}

	cu, cv := fileCategory(us), fileCategory(vs)
	suppress := conf == model.Inferred && (e.Relation == "calls" || e.Relation == "uses") &&
		(crossLanguage(us, vs) || (cu == "code" && cv == "doc") || (cu == "doc" && cv == "code"))

	if suppress {
		bonus = 0
	}

	score := bonus
	if conf == model.Ambiguous || conf == model.Inferred {
		reasons = append(reasons, strings.ToLower(conf)+" connection - not explicitly stated in source")
	}

	if cu != cv && !suppress {
		score += 2
		reasons = append(reasons, fmt.Sprintf("crosses file types (%s ↔ %s)", cu, cv))
	}

	if topLevelDir(us) != topLevelDir(vs) && !suppress {
		score += 2
		reasons = append(reasons, "connects across different repos/directories")
	}

	cidU, okU := nc[u]
	cidV, okV := nc[v]

	if okU && okV && cidU != cidV && !suppress {
		score++
		reasons = append(reasons, "bridges separate communities")
	}

	if e.Relation == "semantically_similar_to" {
		score = int(float64(score) * 1.5)
		reasons = append(reasons, "semantically similar concepts with no structural link")
	}

	du, dv := g.Degree(u), g.Degree(v)
	if minInt(du, dv) <= 2 && maxInt(du, dv) >= 5 {
		score++

		per, hub := g.Node(v).Label, g.Node(u).Label
		if du <= 2 {
			per, hub = g.Node(u).Label, g.Node(v).Label
		}

		reasons = append(reasons, fmt.Sprintf("peripheral node `%s` unexpectedly reaches hub `%s`", per, hub))
	}

	return score, reasons
}

func crossFileSurprises(g *graph.Graph, c graph.Communities, topN int) []Surprise {
	nc := c.NodeCommunity()

	var cands []Surprise

	for _, e := range g.Edges() {
		if structural.Has(e.Relation) {
			continue
		}

		u, v := e.Source, e.Target
		if IsConceptNode(g, u) || IsConceptNode(g, v) || IsFileNode(g, u) || IsFileNode(g, v) {
			continue
		}

		us, vs := g.Node(u).SourceFile, g.Node(v).SourceFile
		if us == "" || vs == "" || us == vs {
			continue
		}

		score, reasons := surpriseScore(g, e, nc)

		why := "cross-file semantic connection"
		if len(reasons) > 0 {
			why = strings.Join(reasons, "; ")
		}

		cands = append(cands, Surprise{
			Source: g.Node(u).Label, Target: g.Node(v).Label,
			SourceFiles: []string{us, vs}, Confidence: orDefault(e.Confidence, model.Extracted),
			Relation: e.Relation, Why: why, score: score,
		})
	}

	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })

	if len(cands) > 0 {
		if len(cands) > topN {
			cands = cands[:topN]
		}

		return cands
	}

	return crossCommunitySurprises(g, c, topN)
}

func crossCommunitySurprises(g *graph.Graph, c graph.Communities, topN int) []Surprise {
	if len(c) == 0 {
		if g.NumEdges() == 0 || g.NumNodes() > 5000 {
			return nil
		}

		bt := EdgeBetweenness(g)

		type eb struct {
			e *model.Edge
			s float64
		}

		var list []eb
		for e, s := range bt {
			list = append(list, eb{e, s})
		}

		sort.SliceStable(list, func(i, j int) bool {
			if list[i].s != list[j].s {
				return list[i].s > list[j].s
			}

			return list[i].e.Source+list[i].e.Target < list[j].e.Source+list[j].e.Target
		})

		var out []Surprise

		for i := 0; i < len(list) && i < topN; i++ {
			e := list[i].e
			out = append(out, Surprise{
				Source: g.Node(e.Source).Label, Target: g.Node(e.Target).Label,
				SourceFiles: []string{g.Node(e.Source).SourceFile, g.Node(e.Target).SourceFile},
				Confidence:  orDefault(e.Confidence, model.Extracted), Relation: e.Relation,
				Note: fmt.Sprintf("Bridges graph structure (betweenness=%.3f)", list[i].s),
			})
		}

		return out
	}

	nc := c.NodeCommunity()

	var out []Surprise

	for _, e := range g.Edges() {
		cu, oku := nc[e.Source]
		cv, okv := nc[e.Target]

		if !oku || !okv || cu == cv {
			continue
		}

		if IsFileNode(g, e.Source) || IsFileNode(g, e.Target) || structural.Has(e.Relation) {
			continue
		}

		p := [2]int{minInt(cu, cv), maxInt(cu, cv)}
		out = append(out, Surprise{
			Source: g.Node(e.Source).Label, Target: g.Node(e.Target).Label,
			SourceFiles: []string{g.Node(e.Source).SourceFile, g.Node(e.Target).SourceFile},
			Confidence:  orDefault(e.Confidence, model.Extracted), Relation: e.Relation,
			Note: fmt.Sprintf("Bridges community %d → community %d", cu, cv), pair: p,
		})
	}

	order := map[string]int{model.Ambiguous: 0, model.Inferred: 1, model.Extracted: 2}
	sort.SliceStable(out, func(i, j int) bool { return rank(order, out[i].Confidence) < rank(order, out[j].Confidence) })

	seen := map[[2]int]bool{}

	var dedup []Surprise

	for _, s := range out {
		if !seen[s.pair] {
			seen[s.pair] = true
			dedup = append(dedup, s)
		}
	}

	if len(dedup) > topN {
		dedup = dedup[:topN]
	}

	return dedup
}

func rank(m map[string]int, k string) int {
	if v, ok := m[k]; ok {
		return v
	}

	return 3
}

// Question is a suggested question.
type Question struct {
	Type     string `json:"type"`
	Question string `json:"question,omitempty"`
	Why      string `json:"why"`
}

// SuggestQuestions generates questions the graph can answer.
func SuggestQuestions(g *graph.Graph, c graph.Communities, labels map[int]string, topN int) []Question {
	var qs []Question

	nc := c.NodeCommunity()
	label := func(cid int) string {
		if l, ok := labels[cid]; ok {
			return l
		}

		return fmt.Sprintf("Community %d", cid)
	}

	for _, e := range g.Edges() {
		if e.Confidence == model.Ambiguous {
			rel := e.Relation
			if rel == "" {
				rel = "related to"
			}

			qs = append(qs, Question{
				Type:     "ambiguous_edge",
				Question: fmt.Sprintf("What is the exact relationship between `%s` and `%s`?", g.Node(e.Source).Label, g.Node(e.Target).Label),
				Why:      fmt.Sprintf("Edge tagged AMBIGUOUS (relation: %s) - confidence is low.", rel),
			})
		}
	}

	if g.NumEdges() > 0 {
		k := 0
		if g.NumNodes() > 1000 {
			k = 100
		}

		bt := Betweenness(g, k)

		type ns struct {
			id string
			s  float64
		}

		var br []ns

		for id, s := range bt {
			if s > 0 && !IsFileNode(g, id) && !IsConceptNode(g, id) {
				br = append(br, ns{id, s})
			}
		}

		sort.SliceStable(br, func(i, j int) bool {
			if br[i].s != br[j].s {
				return br[i].s > br[j].s
			}

			return br[i].id < br[j].id
		})

		if len(br) > 3 {
			br = br[:3]
		}

		for _, b := range br {
			cid, ok := nc[b.id]
			comm := "unknown"

			if ok {
				comm = label(cid)
			}

			others := map[int]bool{}

			for _, nb := range g.Neighbors(b.id) {
				if c2, ok2 := nc[nb]; ok2 && (!ok || c2 != cid) {
					others[c2] = true
				}
			}

			if len(others) == 0 {
				continue
			}

			var ol []string
			for _, oc := range sortedInts(others) {
				ol = append(ol, "`"+label(oc)+"`")
			}

			qs = append(qs, Question{
				Type:     "bridge_node",
				Question: fmt.Sprintf("Why does `%s` connect `%s` to %s?", g.Node(b.id).Label, comm, strings.Join(ol, ", ")),
				Why:      fmt.Sprintf("High betweenness centrality (%.3f) - this node is a cross-community bridge.", b.s),
			})
		}
	}

	ids := g.NodeIDs()

	var top []string

	for _, id := range ids {
		if !IsFileNode(g, id) {
			top = append(top, id)
		}
	}

	sort.SliceStable(top, func(i, j int) bool { return g.Degree(top[i]) > g.Degree(top[j]) })

	if len(top) > 5 {
		top = top[:5]
	}

	for _, id := range top {
		var inferred []*model.Edge

		for _, nb := range g.Neighbors(id) {
			if e := g.Edge(id, nb); e != nil && e.Confidence == model.Inferred {
				inferred = append(inferred, e)
			}
		}

		if len(inferred) < 2 {
			continue
		}

		var others []string

		for _, e := range inferred[:2] {
			other := e.Target
			if e.Target == id {
				other = e.Source
			}

			others = append(others, g.Node(other).Label)
		}

		l := g.Node(id).Label
		qs = append(qs, Question{
			Type:     "verify_inferred",
			Question: fmt.Sprintf("Are the %d inferred relationships involving `%s` (e.g. with `%s` and `%s`) actually correct?", len(inferred), l, others[0], others[1]),
			Why:      fmt.Sprintf("`%s` has %d INFERRED edges - model-reasoned connections that need verification.", l, len(inferred)),
		})
	}

	var isolated []string

	for _, id := range ids {
		if g.Degree(id) <= 1 && !IsFileNode(g, id) && !IsConceptNode(g, id) && g.Node(id).FileType != model.FileTypeRationale {
			isolated = append(isolated, id)
		}
	}

	if len(isolated) > 0 {
		var ls []string
		for i := 0; i < len(isolated) && i < 3; i++ {
			ls = append(ls, "`"+g.Node(isolated[i]).Label+"`")
		}

		qs = append(qs, Question{
			Type:     "isolated_nodes",
			Question: fmt.Sprintf("What connects %s to the rest of the system?", strings.Join(ls, ", ")),
			Why:      fmt.Sprintf("%d weakly-connected nodes found - possible documentation gaps or missing edges.", len(isolated)),
		})
	}

	for _, cid := range sortedInts(keysOf(c)) {
		ms := c[cid]
		s := cluster.CohesionScore(g, ms)

		if s < 0.15 && len(ms) >= 5 {
			qs = append(qs, Question{
				Type:     "low_cohesion",
				Question: fmt.Sprintf("Should `%s` be split into smaller, more focused modules?", label(cid)),
				Why:      fmt.Sprintf("Cohesion score %s - nodes in this community are weakly interconnected.", pyFloatStr(s)),
			})
		}
	}

	if len(qs) == 0 {
		return []Question{{
			Type: "no_signal",
			Why: "Not enough signal to generate questions. " +
				"This usually means the corpus has no AMBIGUOUS edges, no bridge nodes, " +
				"no INFERRED relationships, and all communities are tightly cohesive. " +
				"Add more files or run with --mode deep to extract richer edges.",
		}}
	}

	if len(qs) > topN {
		qs = qs[:topN]
	}

	return qs
}

// Cycle is an import cycle.
type Cycle struct {
	Cycle  []string `json:"cycle"`
	Length int      `json:"length"`
	Why    string   `json:"why"`
}

// FindImportCycles detects circular file-level imports.
func FindImportCycles(g *graph.Graph, maxLen, topN int) []Cycle {
	adj := map[string]map[string]bool{}

	for _, e := range g.Edges() {
		if e.Relation != "imports_from" && e.Relation != "re_exports" {
			continue
		}

		if e.Extra["deferred"] == true || e.Extra["type_only"] == true {
			continue
		}

		src := e.SourceFile
		if src == "" {
			continue
		}

		uf, vf := g.Node(e.Source).SourceFile, g.Node(e.Target).SourceFile

		var tgt string

		switch {
		case uf == src:
			tgt = vf
		case vf == src:
			tgt = uf
		case vf != "" && vf != src:
			tgt = vf
		default:
			tgt = uf
		}

		if tgt == "" {
			continue
		}

		if adj[src] == nil {
			adj[src] = map[string]bool{}
		}

		adj[src][tgt] = true
	}

	if len(adj) == 0 {
		return nil
	}

	cycles := simpleCycles(adj, maxLen, topN*10)
	sort.SliceStable(cycles, func(i, j int) bool { return len(cycles[i]) < len(cycles[j]) })

	seen := map[string]bool{}

	var out []Cycle

	for _, cy := range cycles {
		mi := 0
		for i, x := range cy {
			if x < cy[mi] {
				mi = i
			}
		}

		norm := append(append([]string(nil), cy[mi:]...), cy[:mi]...)
		k := strings.Join(norm, "\x00")

		if seen[k] {
			continue
		}

		seen[k] = true
		out = append(out, Cycle{Cycle: norm, Length: len(norm), Why: "circular dependency"})

		if len(out) >= topN {
			break
		}
	}

	return out
}

func simpleCycles(adj map[string]map[string]bool, maxLen, limit int) [][]string {
	var nodes []string
	for n := range adj {
		nodes = append(nodes, n)
	}

	sort.Strings(nodes)

	idx := map[string]int{}
	for i, n := range nodes {
		idx[n] = i
	}

	var out [][]string

	var path []string

	onPath := map[string]bool{}

	var dfs func(start, cur string)
	dfs = func(start, cur string) {
		if len(out) >= limit {
			return
		}

		var nbs []string
		for nb := range adj[cur] {
			nbs = append(nbs, nb)
		}

		sort.Strings(nbs)

		for _, nb := range nbs {
			if nb == start {
				out = append(out, append([]string(nil), path...))

				continue
			}

			i, ok := idx[nb]
			if !ok || i < idx[start] || onPath[nb] || len(path) >= maxLen {
				continue
			}

			onPath[nb] = true
			path = append(path, nb)
			dfs(start, nb)
			path = path[:len(path)-1]
			onPath[nb] = false
		}
	}

	for _, s := range nodes {
		path = []string{s}
		onPath = map[string]bool{s: true}
		dfs(s, s)
	}

	return out
}

// Diff describes graph changes.
type Diff struct {
	NewNodes     []map[string]string `json:"new_nodes"`
	RemovedNodes []map[string]string `json:"removed_nodes"`
	NewEdges     []map[string]string `json:"new_edges"`
	RemovedEdges []map[string]string `json:"removed_edges"`
	Summary      string              `json:"summary"`
}

// GraphDiff compares two graphs.
func GraphDiff(old, cur *graph.Graph) Diff {
	d := Diff{}

	for _, n := range cur.Nodes() {
		if !old.Has(n.ID) {
			d.NewNodes = append(d.NewNodes, map[string]string{"id": n.ID, "label": n.Label})
		}
	}

	for _, n := range old.Nodes() {
		if !cur.Has(n.ID) {
			d.RemovedNodes = append(d.RemovedNodes, map[string]string{"id": n.ID, "label": n.Label})
		}
	}

	key := func(e *model.Edge) string {
		a, b := e.Source, e.Target
		if a > b {
			a, b = b, a
		}

		return a + "\x00" + b + "\x00" + e.Relation
	}

	oldK := map[string]bool{}
	for _, e := range old.Edges() {
		oldK[key(e)] = true
	}

	curK := map[string]bool{}
	for _, e := range cur.Edges() {
		curK[key(e)] = true
	}

	em := func(e *model.Edge) map[string]string {
		return map[string]string{"source": e.Source, "target": e.Target, "relation": e.Relation, "confidence": e.Confidence}
	}

	for _, e := range cur.Edges() {
		if !oldK[key(e)] {
			d.NewEdges = append(d.NewEdges, em(e))
		}
	}

	for _, e := range old.Edges() {
		if !curK[key(e)] {
			d.RemovedEdges = append(d.RemovedEdges, em(e))
		}
	}

	var parts []string

	plural := func(n int, word string) string {
		if n == 1 {
			return fmt.Sprintf("%d %s", n, word)
		}

		return fmt.Sprintf("%d %ss", n, word)
	}

	if len(d.NewNodes) > 0 {
		parts = append(parts, plural(len(d.NewNodes), "new node"))
	}

	if len(d.NewEdges) > 0 {
		parts = append(parts, plural(len(d.NewEdges), "new edge"))
	}

	if len(d.RemovedNodes) > 0 {
		parts = append(parts, plural(len(d.RemovedNodes), "node")+" removed")
	}

	if len(d.RemovedEdges) > 0 {
		parts = append(parts, plural(len(d.RemovedEdges), "edge")+" removed")
	}

	d.Summary = "no changes"
	if len(parts) > 0 {
		d.Summary = strings.Join(parts, ", ")
	}

	return d
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}

	return s
}

func minInt(a, b int) int {
	if a < b {
		return a
	}

	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}

	return b
}

func keysOf(c graph.Communities) map[int]bool {
	m := map[int]bool{}
	for k := range c {
		m[k] = true
	}

	return m
}

func sortedInts(m map[int]bool) []int {
	var out []int
	for k := range m {
		out = append(out, k)
	}

	sort.Ints(out)

	return out
}

func pyFloatStr(f float64) string {
	s := fmt.Sprintf("%v", f)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}

	return s
}
