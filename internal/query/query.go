// Package query answers questions against a built graph: scored node
// search, BFS/DFS subgraph expansion, shortest path and node explanations.
//
// Adapted from Graphify's graphify/serve.py (Apache-2.0).
package query

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/rytsh/bag/internal/graph"
	"github.com/rytsh/bag/internal/model"
)

const (
	exactBonus     = 1000.0
	prefixBonus    = 100.0
	substringBonus = 1.0
	sourceBonus    = 0.5
	rationaleBonus = 0.75
)

var tokenRe = regexp.MustCompile(`[^\W_]+`)

// StripDiacritics removes combining marks after NFKD decomposition.
func StripDiacritics(s string) string {
	var b strings.Builder

	for _, r := range norm.NFKD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}

		b.WriteRune(r)
	}

	return b.String()
}

// Tokens splits text into lower-cased word tokens (underscore separates).
func Tokens(s string) []string {
	return tokenRe.FindAllString(strings.ToLower(StripDiacritics(s)), -1)
}

// Engine answers queries over one graph.
type Engine struct {
	G           *graph.Graph
	Communities graph.Communities
	Labels      map[int]string

	nc    map[string]int
	idf   map[string]float64
	idfMu sync.RWMutex
	norms map[string]string
	toks  map[string]string
}

// New builds an engine.
func New(g *graph.Graph, c graph.Communities, labels map[int]string) *Engine {
	e := &Engine{G: g, Communities: c, Labels: labels, nc: c.NodeCommunity(),
		idf: map[string]float64{}, norms: map[string]string{}, toks: map[string]string{}}

	for _, n := range g.Nodes() {
		e.norms[n.ID] = strings.ToLower(StripDiacritics(n.Label))
		e.toks[n.ID] = strings.Join(Tokens(n.Label), " ")
	}

	return e
}

func (e *Engine) idfOf(t string) float64 {
	e.idfMu.RLock()
	v, ok := e.idf[t]
	e.idfMu.RUnlock()
	if ok {
		return v
	}

	n := float64(e.G.NumNodes())
	if n == 0 {
		n = 1
	}

	df := 0.0

	for id, l := range e.norms {
		if strings.Contains(l, t) || strings.Contains(e.toks[id], t) {
			df++
		}
	}

	v = math.Log((n+1)/(df+1)) + 1
	e.idfMu.Lock()
	e.idf[t] = v
	e.idfMu.Unlock()

	return v
}

// Scored is a ranked node.
type Scored struct {
	ID    string
	Score float64
}

// Score ranks nodes against the query terms.
func (e *Engine) Score(terms []string) []Scored {
	var norms []string

	seen := map[string]bool{}

	for _, t := range terms {
		for _, tok := range Tokens(t) {
			if !seen[tok] {
				seen[tok] = true
				norms = append(norms, tok)
			}
		}
	}

	if len(norms) == 0 {
		return nil
	}

	joined := strings.Join(norms, " ")
	joinedW := 1.0

	for _, t := range norms {
		joinedW = math.Max(joinedW, e.idfOf(t))
	}

	var out []Scored

	for _, n := range e.G.Nodes() {
		nl := e.norms[n.ID]
		bare := strings.TrimRight(nl, "()")
		lt := e.toks[n.ID]
		src := strings.ToLower(n.SourceFile)
		rat := ""

		if r, ok := n.Extra["rationale"].(string); ok {
			rat = strings.ToLower(r)
		}

		idl := strings.ToLower(n.ID)
		score := 0.0

		switch {
		case joined == nl || joined == bare || joined == lt || joined == idl:
			score += exactBonus * 10 * joinedW
		case strings.HasPrefix(nl, joined) || strings.HasPrefix(bare, joined) || strings.HasPrefix(lt, joined):
			score += prefixBonus * 10 * joinedW
		}

		matched := 0
		tiered := 0.0

		for _, t := range norms {
			w := e.idfOf(t)

			switch {
			case t == nl || t == bare:
				tiered += exactBonus * w
				matched++
			case strings.HasPrefix(nl, t) || strings.HasPrefix(bare, t):
				tiered += prefixBonus * w
				matched++
			case strings.Contains(nl, t):
				score += substringBonus * w
				matched++
			}

			if strings.Contains(src, t) {
				score += sourceBonus * w
			}

			if rat != "" && strings.Contains(rat, t) {
				score += rationaleBonus * w
			}
		}

		if matched > 0 {
			score += tiered * float64(matched) / float64(len(norms))
		}

		if score > 0 {
			out = append(out, Scored{n.ID, score})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}

		di, dj := e.G.Degree(out[i].ID), e.G.Degree(out[j].ID)
		if di != dj {
			return di > dj
		}

		return out[i].ID < out[j].ID
	})

	return out
}

func (e *Engine) hubThreshold() int {
	ids := e.G.NodeIDs()
	if len(ids) == 0 {
		return 50
	}

	degs := make([]int, len(ids))
	for i, id := range ids {
		degs[i] = e.G.Degree(id)
	}

	sort.Ints(degs)

	th := degs[int(float64(len(degs))*0.99)]
	if th < 50 {
		th = 50
	}

	return th
}

// Expand traverses from seeds up to depth hops (BFS or DFS).
func (e *Engine) Expand(seeds []string, depth int, dfs bool) (map[string]bool, [][2]string) {
	hub := e.hubThreshold()
	seedSet := map[string]bool{}

	for _, s := range seeds {
		seedSet[s] = true
	}

	visited := map[string]bool{}

	var edges [][2]string

	if dfs {
		type item struct {
			n string
			d int
		}

		var stack []item
		for i := len(seeds) - 1; i >= 0; i-- {
			stack = append(stack, item{seeds[i], 0})
		}

		for len(stack) > 0 {
			it := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			if visited[it.n] || it.d > depth {
				continue
			}

			visited[it.n] = true

			if !seedSet[it.n] && e.G.Degree(it.n) >= hub {
				continue
			}

			for _, nb := range e.G.Neighbors(it.n) {
				if !visited[nb] {
					stack = append(stack, item{nb, it.d + 1})
					edges = append(edges, [2]string{it.n, nb})
				}
			}
		}
	} else {
		frontier := append([]string(nil), seeds...)
		for _, s := range seeds {
			visited[s] = true
		}

		for range depth {
			var next []string

			nextSet := map[string]bool{}

			for _, n := range frontier {
				if !seedSet[n] && e.G.Degree(n) >= hub {
					continue
				}

				for _, nb := range e.G.Neighbors(n) {
					if !visited[nb] {
						if !nextSet[nb] {
							nextSet[nb] = true
							next = append(next, nb)
						}

						edges = append(edges, [2]string{n, nb})
					}
				}
			}

			for _, n := range next {
				visited[n] = true
			}

			frontier = next
		}
	}

	have := map[[2]string]bool{}
	for _, ed := range edges {
		have[ed] = true
		have[[2]string{ed[1], ed[0]}] = true
	}

	for n := range visited {
		for _, nb := range e.G.Neighbors(n) {
			if visited[nb] && !have[[2]string{n, nb}] {
				have[[2]string{n, nb}] = true
				have[[2]string{nb, n}] = true
				edges = append(edges, [2]string{n, nb})
			}
		}
	}

	return visited, edges
}

func (e *Engine) communityName(id string) string {
	cid, ok := e.nc[id]
	if !ok {
		if n := e.G.Node(id); n != nil {
			if v, ok := n.Extra["community_name"].(string); ok {
				return v
			}
		}

		return ""
	}

	if l, ok := e.Labels[cid]; ok {
		return l
	}

	return fmt.Sprint(cid)
}

// Render renders a subgraph as text within a token budget (≈3 chars/token).
func (e *Engine) Render(nodes map[string]bool, edges [][2]string, budget int, seeds []string) string {
	charBudget := budget * 3
	seedSet := map[string]bool{}

	var hits []string

	for _, s := range seeds {
		seedSet[s] = true
		if nodes[s] {
			hits = append(hits, s)
		}
	}

	dist := map[string]int{}
	for _, s := range hits {
		dist[s] = 0
	}

	frontier := hits
	for hop := 1; len(frontier) > 0; hop++ {
		var nxt []string

		for _, n := range frontier {
			for _, nb := range e.G.Neighbors(n) {
				if nodes[nb] {
					if _, ok := dist[nb]; !ok {
						dist[nb] = hop
						nxt = append(nxt, nb)
					}
				}
			}
		}

		frontier = nxt
	}

	var rest []string

	for n := range nodes {
		if !seedSet[n] {
			rest = append(rest, n)
		}
	}

	far := 1 << 30

	sort.Slice(rest, func(i, j int) bool {
		di, ok := dist[rest[i]]
		if !ok {
			di = far
		}

		dj, ok := dist[rest[j]]
		if !ok {
			dj = far
		}

		if di != dj {
			return di < dj
		}

		gi, gj := e.G.Degree(rest[i]), e.G.Degree(rest[j])
		if gi != gj {
			return gi > gj
		}

		return rest[i] < rest[j]
	})

	var lines []string

	for _, id := range append(hits, rest...) {
		n := e.G.Node(id)
		lines = append(lines, fmt.Sprintf("NODE %s [src=%s loc=%s community=%s]",
			n.Label, n.SourceFile, n.SourceLocation, e.communityName(id)))
	}

	for _, ed := range edges {
		if !nodes[ed[0]] || !nodes[ed[1]] {
			continue
		}

		x := e.G.Edge(ed[0], ed[1])
		if x == nil {
			continue
		}

		ctx := ""
		if x.Context != "" {
			ctx = " context=" + x.Context
		}

		at := ""
		if x.SourceLocation != "" {
			at = fmt.Sprintf(" at=%s:%s", x.SourceFile, x.SourceLocation)
		}

		lines = append(lines, fmt.Sprintf("EDGE %s --%s [%s%s%s]--> %s",
			e.G.Node(x.Source).Label, x.Relation, x.Confidence, ctx, at, e.G.Node(x.Target).Label))
	}

	out := strings.Join(lines, "\n")
	if len(out) > charBudget {
		cut := out[:charBudget]
		if i := strings.LastIndexByte(cut, '\n'); i > 0 {
			cut = cut[:i]
		}

		out = cut + fmt.Sprintf("\n... (truncated to ~%d token budget)", budget)
	}

	return out
}

// Options for Query.
type Options struct {
	Depth  int
	DFS    bool
	Budget int
	Seeds  int
}

// Query answers a natural-language question with a scoped subgraph.
func (e *Engine) Query(question string, opt Options) string {
	if opt.Depth == 0 {
		opt.Depth = 2
	}

	if opt.Budget == 0 {
		opt.Budget = 2000
	}

	if opt.Seeds == 0 {
		opt.Seeds = 3
	}

	scored := e.Score(strings.Fields(question))
	if len(scored) == 0 {
		return "No matching nodes found."
	}

	var seeds []string
	for i := 0; i < len(scored) && i < opt.Seeds; i++ {
		seeds = append(seeds, scored[i].ID)
	}

	nodes, edges := e.Expand(seeds, opt.Depth, opt.DFS)

	var hdr []string
	for _, s := range seeds {
		hdr = append(hdr, e.G.Node(s).Label)
	}

	mode := "BFS"
	if opt.DFS {
		mode = "DFS"
	}

	return fmt.Sprintf("Traversal: %s depth=%d | Start: %s | %d nodes found\n\n%s",
		mode, opt.Depth, strings.Join(hdr, ", "), len(nodes), e.Render(nodes, edges, opt.Budget, seeds))
}

// Find returns node ids best matching a label (exact first).
func (e *Engine) Find(label string) []string {
	low := strings.ToLower(StripDiacritics(label))

	var exact []string

	for _, n := range e.G.Nodes() {
		nl := e.norms[n.ID]
		if nl == low || strings.TrimRight(nl, "()") == strings.TrimRight(low, "()") || strings.ToLower(n.ID) == low {
			exact = append(exact, n.ID)
		}
	}

	if len(exact) > 0 {
		sort.SliceStable(exact, func(i, j int) bool { return e.G.Degree(exact[i]) > e.G.Degree(exact[j]) })

		return exact
	}

	var out []string
	for _, s := range e.Score([]string{label}) {
		out = append(out, s.ID)
	}

	return out
}

// Path returns the shortest path between two labels as text.
func (e *Engine) Path(from, to string) string {
	a, b := e.Find(from), e.Find(to)
	if len(a) == 0 {
		return fmt.Sprintf("No node matching %q.", from)
	}

	if len(b) == 0 {
		return fmt.Sprintf("No node matching %q.", to)
	}

	p := e.ShortestPath(a[0], b[0])
	if p == nil {
		return fmt.Sprintf("No path between %s and %s.", e.G.Node(a[0]).Label, e.G.Node(b[0]).Label)
	}

	var sb strings.Builder

	fmt.Fprintf(&sb, "Shortest path (%d hops):\n  %s", len(p)-1, e.G.Node(p[0]).Label)

	for i := 1; i < len(p); i++ {
		x := e.G.Edge(p[i-1], p[i])
		if x.Source == p[i-1] {
			fmt.Fprintf(&sb, " --%s--> %s", x.Relation, e.G.Node(p[i]).Label)
		} else {
			fmt.Fprintf(&sb, " <--%s-- %s", x.Relation, e.G.Node(p[i]).Label)
		}
	}

	return sb.String()
}

// ShortestPath is an unweighted BFS path over the undirected graph.
func (e *Engine) ShortestPath(a, b string) []string {
	if a == b {
		return []string{a}
	}

	prev := map[string]string{a: ""}
	q := []string{a}

	for len(q) > 0 {
		n := q[0]
		q = q[1:]

		for _, nb := range e.G.Neighbors(n) {
			if _, ok := prev[nb]; ok {
				continue
			}

			prev[nb] = n
			if nb == b {
				var p []string
				for c := b; c != ""; c = prev[c] {
					p = append([]string{c}, p...)
				}

				return p
			}

			q = append(q, nb)
		}
	}

	return nil
}

// Explain describes one node and its connections.
func (e *Engine) Explain(label string) string {
	ids := e.Find(label)
	if len(ids) == 0 {
		return fmt.Sprintf("No node matching %q.", label)
	}

	id := ids[0]
	n := e.G.Node(id)

	var sb strings.Builder

	fmt.Fprintf(&sb, "Node: %s\n  ID:        %s\n  Source:    %s %s\n  Type:      %s\n  Community: %s\n  Degree:    %d\n",
		n.Label, n.ID, n.SourceFile, n.SourceLocation, n.FileType, e.communityName(id), e.G.Degree(id))

	nbs := e.G.Neighbors(id)
	sort.SliceStable(nbs, func(i, j int) bool { return e.G.Degree(nbs[i]) > e.G.Degree(nbs[j]) })

	fmt.Fprintf(&sb, "\nConnections (%d):\n", len(nbs))

	for _, nb := range nbs {
		x := e.G.Edge(id, nb)

		conf := x.Confidence
		if conf == "" {
			conf = model.Extracted
		}

		arrow := "-->"
		if x.Target == id && x.Source != id {
			arrow = "<--"
		}

		fmt.Fprintf(&sb, "  %s %s [%s] [%s]\n", arrow, e.G.Node(nb).Label, x.Relation, conf)
	}

	return strings.TrimRight(sb.String(), "\n")
}
