// Package cluster runs community detection on the knowledge graph.
//
// Adapted from Graphify's graphify/cluster.py (Apache-2.0). Graphify uses
// Leiden (graspologic) with a Louvain fallback; bag implements Leiden
// (Louvain local moving + refinement + aggregation) natively in Go with a
// fixed seed so results are deterministic.
package cluster

import (
	"maps"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"

	"github.com/rytsh/bag/internal/graph"
)

const (
	maxCommunityFraction   = 0.25
	minSplitSize           = 10
	cohesionSplitThreshold = 0.05
	cohesionSplitMinSize   = 50
)

// Options tune clustering.
type Options struct {
	Resolution         float64
	ExcludeHubsPercent float64 // 0 disables
}

// Cluster partitions g into communities: {community_id: sorted node ids}.
// Community 0 is the largest.
func Cluster(g *graph.Graph, opt Options) graph.Communities {
	if opt.Resolution <= 0 {
		opt.Resolution = 1.0
	}

	if g.NumNodes() == 0 {
		return graph.Communities{}
	}

	ids := g.NodeIDs()
	sort.Strings(ids)

	if g.NumEdges() == 0 {
		out := graph.Communities{}
		for i, n := range ids {
			out[i] = []string{n}
		}

		return out
	}

	hubs := map[string]bool{}

	if opt.ExcludeHubsPercent > 0 {
		degs := make([]int, 0, len(ids))
		for _, n := range ids {
			degs = append(degs, g.Degree(n))
		}

		sort.Ints(degs)

		idx := int(float64(len(degs))*opt.ExcludeHubsPercent/100) - 1
		if idx < 0 {
			idx = 0
		}

		th := degs[idx]
		for _, n := range ids {
			if g.Degree(n) > th {
				hubs[n] = true
			}
		}
	}

	var isolates, connected, stranded []string

	for _, n := range ids {
		if hubs[n] {
			continue
		}

		if g.Degree(n) == 0 {
			isolates = append(isolates, n)
		} else {
			// Adapted from Graphify's cluster (Apache-2.0): neighbours
			// stranded by hub exclusion follow their hubs, not singleton
			// communities. Self-loops are not neighbours for this test.
			if len(hubs) > 0 {
				onlyHubs, hasNeighbour := true, false
				for _, nb := range g.Neighbors(n) {
					if nb == n {
						continue
					}
					hasNeighbour = true
					if !hubs[nb] {
						onlyHubs = false
						break
					}
				}
				if hasNeighbour && onlyHubs {
					stranded = append(stranded, n)
					continue
				}
			}
			connected = append(connected, n)
		}
	}

	raw := map[int][]string{}

	if len(connected) > 0 {
		part := partition(g.Subgraph(connected), opt.Resolution)
		for n, c := range part {
			raw[c] = append(raw[c], n)
		}
	}

	next := 0
	for c := range raw {
		if c >= next {
			next = c + 1
		}
	}

	for _, n := range isolates {
		raw[next] = []string{n}
		next++
	}

	if len(hubs) > 0 {
		nc := map[string]int{}

		for c, ms := range raw {
			for _, m := range ms {
				nc[m] = c
			}
		}

		var hs []string
		for h := range hubs {
			hs = append(hs, h)
		}

		sort.Strings(hs)

		for _, h := range hs {
			votes := map[int]int{}

			for _, nb := range g.Neighbors(h) {
				if c, ok := nc[nb]; ok {
					votes[c]++
				}
			}

			if len(votes) == 0 {
				raw[next] = []string{h}
				nc[h] = next
				next++

				continue
			}

			best, bestV := -1, -1
			for c, v := range votes {
				if v > bestV || (v == bestV && c < best) {
					best, bestV = c, v
				}
			}

			raw[best] = append(raw[best], h)
			nc[h] = best
		}
		for _, n := range stranded {
			votes := map[int]int{}
			for _, nb := range g.Neighbors(n) {
				if nb != n {
					votes[nc[nb]]++
				}
			}
			best, bestV := -1, -1
			for c, v := range votes {
				if v > bestV || (v == bestV && c < best) {
					best, bestV = c, v
				}
			}
			raw[best] = append(raw[best], n)
			nc[n] = best
		}
	}

	maxSize := int(float64(g.NumNodes()) * maxCommunityFraction)
	if maxSize < minSplitSize {
		maxSize = minSplitSize
	}

	var final [][]string

	for _, c := range slices.Sorted(maps.Keys(raw)) {
		ms := raw[c]
		if len(ms) > maxSize {
			final = append(final, splitCommunity(g, ms)...)
		} else {
			final = append(final, ms)
		}
	}

	var second [][]string

	for _, ms := range final {
		if len(ms) >= cohesionSplitMinSize && CohesionScore(g, ms) < cohesionSplitThreshold {
			sp := splitCommunity(g, ms)
			if len(sp) > 1 {
				second = append(second, sp...)

				continue
			}
		}

		second = append(second, ms)
	}

	for _, ms := range second {
		sort.Strings(ms)
	}

	sort.SliceStable(second, func(i, j int) bool {
		if len(second[i]) != len(second[j]) {
			return len(second[i]) > len(second[j])
		}

		return strings.Join(second[i], "\x00") < strings.Join(second[j], "\x00")
	})

	out := graph.Communities{}
	for i, ms := range second {
		out[i] = ms
	}

	return out
}

func splitCommunity(g *graph.Graph, nodes []string) [][]string {
	sub := g.Subgraph(nodes)
	if sub.NumEdges() == 0 {
		out := make([][]string, 0, len(nodes))

		s := append([]string(nil), nodes...)
		sort.Strings(s)

		for _, n := range s {
			out = append(out, []string{n})
		}

		return out
	}

	part := partition(sub, 1.0)
	groups := map[int][]string{}

	for n, c := range part {
		groups[c] = append(groups[c], n)
	}

	if len(groups) <= 1 {
		s := append([]string(nil), nodes...)
		sort.Strings(s)

		return [][]string{s}
	}

	var out [][]string

	for _, c := range slices.Sorted(maps.Keys(groups)) {
		ms := groups[c]
		sort.Strings(ms)
		out = append(out, ms)
	}

	return out
}

// CohesionScore is the ratio of intra-community edges to possible pairs
// (self-loops excluded).
func CohesionScore(g *graph.Graph, members []string) float64 {
	n := len(members)
	if n <= 1 {
		return 1.0
	}

	in := map[string]bool{}
	for _, m := range members {
		in[m] = true
	}

	actual := 0

	for _, e := range g.Edges() {
		if e.Source != e.Target && in[e.Source] && in[e.Target] {
			actual++
		}
	}

	possible := float64(n*(n-1)) / 2

	return float64(actual) / possible
}

// ScoreAll computes cohesion for every community.
func ScoreAll(g *graph.Graph, c graph.Communities) map[int]float64 {
	out := map[int]float64{}
	for cid, ms := range c {
		out[cid] = CohesionScore(g, ms)
	}

	return out
}

// LabelByHub names each community after its highest-degree member.
func LabelByHub(g *graph.Graph, c graph.Communities) map[int]string {
	out := map[int]string{}

	for cid, ms := range c {
		hub, best := "", -1

		for _, m := range ms {
			if !g.Has(m) {
				continue
			}

			d := g.Degree(m)
			if d > best || (d == best && m < hub) {
				hub, best = m, d
			}
		}

		if hub == "" {
			out[cid] = communityName(cid)

			continue
		}

		name := strings.TrimSpace(g.Node(hub).Label)
		if name == "" {
			name = hub
		}

		name = strings.TrimSuffix(name, "()")
		if name == "" {
			name = communityName(cid)
		}

		out[cid] = name
	}

	return out
}

func communityName(cid int) string {
	return "Community " + itoa(cid)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}

	neg := i < 0
	if neg {
		i = -i
	}

	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}

	if neg {
		b = append([]byte{'-'}, b...)
	}

	return string(b)
}

// ---- Leiden ----

type wgraph struct {
	n      int
	adj    [][]wedge
	deg    []float64 // weighted degree (self-loops counted twice)
	total  float64   // 2m
	weight []float64 // node weights (sizes) for aggregation
}

type wedge struct {
	to int
	w  float64
}

func toWGraph(g *graph.Graph) (*wgraph, []string) {
	ids := g.NodeIDs()
	sort.Strings(ids)

	idx := make(map[string]int, len(ids))
	for i, id := range ids {
		idx[id] = i
	}

	wg := &wgraph{n: len(ids), adj: make([][]wedge, len(ids)), deg: make([]float64, len(ids)), weight: make([]float64, len(ids))}

	type pair struct{ a, b int }

	edges := g.Edges()
	ps := make([]pair, 0, len(edges))

	for _, e := range edges {
		a, b := idx[e.Source], idx[e.Target]
		if a > b {
			a, b = b, a
		}

		ps = append(ps, pair{a, b})
	}

	sort.Slice(ps, func(i, j int) bool {
		if ps[i].a != ps[j].a {
			return ps[i].a < ps[j].a
		}

		return ps[i].b < ps[j].b
	})

	for _, p := range ps {
		wg.addEdge(p.a, p.b, 1)
	}

	for i := range wg.weight {
		wg.weight[i] = 1
	}

	return wg, ids
}

func (w *wgraph) addEdge(a, b int, weight float64) {
	if a == b {
		w.adj[a] = append(w.adj[a], wedge{b, weight})
		w.deg[a] += 2 * weight
		w.total += 2 * weight

		return
	}

	w.adj[a] = append(w.adj[a], wedge{b, weight})
	w.adj[b] = append(w.adj[b], wedge{a, weight})
	w.deg[a] += weight
	w.deg[b] += weight
	w.total += 2 * weight
}

func partition(g *graph.Graph, resolution float64) map[string]int {
	wg, ids := toWGraph(g)
	rng := rand.New(rand.NewPCG(42, 42)) //nolint:gosec // deterministic clustering

	memb := leiden(wg, resolution, rng)

	out := make(map[string]int, len(ids))
	for i, id := range ids {
		out[id] = memb[i]
	}

	return out
}

// leiden returns a community index per node.
func leiden(g *wgraph, gamma float64, rng *rand.Rand) []int {
	memb := make([]int, g.n)
	for i := range memb {
		memb[i] = i
	}

	if g.total == 0 {
		return memb
	}

	level := g
	mapping := make([][]int, g.n) // aggregated node -> original nodes

	for i := range mapping {
		mapping[i] = []int{i}
	}

	part := make([]int, g.n)
	for i := range part {
		part[i] = i
	}

	for range 32 {
		part = localMovingFrom(level, gamma, rng, part)

		for agg, origs := range mapping {
			for _, o := range origs {
				memb[o] = part[agg]
			}
		}

		if countDistinct(part) == level.n {
			break
		}

		refined := refine(level, part, gamma, rng)

		agg, aggMap, aggInit := aggregate(level, refined, part)

		newMapping := make([][]int, agg.n)
		for oldNode, origs := range mapping {
			a := aggMap[oldNode]
			newMapping[a] = append(newMapping[a], origs...)
		}

		mapping = newMapping
		level = agg
		part = aggInit
	}

	return renumber(memb)
}

func countDistinct(a []int) int {
	s := map[int]bool{}
	for _, x := range a {
		s[x] = true
	}

	return len(s)
}

func renumber(a []int) []int {
	m := map[int]int{}
	out := make([]int, len(a))

	for i, x := range a {
		v, ok := m[x]
		if !ok {
			v = len(m)
			m[x] = v
		}

		out[i] = v
	}

	return out
}

// localMovingFrom is the fast local moving phase (queue based).
func localMovingFrom(g *wgraph, gamma float64, rng *rand.Rand, init []int) []int {
	comm := append([]int(nil), init...)
	commDeg := make([]float64, g.n)

	for i := range comm {
		commDeg[comm[i]] += g.deg[i]
	}

	order := rng.Perm(g.n)
	queue := append([]int(nil), order...)
	inQueue := make([]bool, g.n)

	for _, v := range queue {
		inQueue[v] = true
	}

	m2 := g.total
	neighW := make([]float64, g.n)

	var touched []int

	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		inQueue[v] = false

		cur := comm[v]
		touched = touched[:0]

		for _, e := range g.adj[v] {
			if e.to == v {
				continue
			}

			c := comm[e.to]
			if neighW[c] == 0 {
				touched = append(touched, c)
			}

			neighW[c] += e.w
		}

		commDeg[cur] -= g.deg[v]

		best := cur
		bestGain := neighW[cur] - gamma*g.deg[v]*commDeg[cur]/m2

		for _, c := range touched {
			gain := neighW[c] - gamma*g.deg[v]*commDeg[c]/m2
			if gain > bestGain+1e-12 || (abs(gain-bestGain) <= 1e-12 && c < best) {
				best, bestGain = c, gain
			}
		}

		commDeg[best] += g.deg[v]

		for _, c := range touched {
			neighW[c] = 0
		}

		neighW[cur] = 0

		if best != cur {
			comm[v] = best

			for _, e := range g.adj[v] {
				u := e.to
				if u != v && comm[u] != best && !inQueue[u] {
					queue = append(queue, u)
					inQueue[u] = true
				}
			}
		}
	}

	return comm
}

// refine splits each community into well-connected sub-communities
// (Leiden refinement, greedy variant with theta -> 0).
func refine(g *wgraph, comm []int, gamma float64, rng *rand.Rand) []int {
	ref := make([]int, g.n)
	for i := range ref {
		ref[i] = i
	}

	refDeg := append([]float64(nil), g.deg...)
	refSize := make([]int, g.n)

	for i := range refSize {
		refSize[i] = 1
	}

	commDeg := map[int]float64{}
	for i, c := range comm {
		commDeg[c] += g.deg[i]
	}

	// Weight from node to rest of its community.
	m2 := g.total
	order := rng.Perm(g.n)
	neighW := map[int]float64{}

	for _, v := range order {
		// Only singletons move during refinement.
		if refSize[ref[v]] != 1 {
			continue
		}

		// v must be well connected in its community.
		kIn := 0.0

		for _, e := range g.adj[v] {
			if e.to != v && comm[e.to] == comm[v] {
				kIn += e.w
			}
		}

		if kIn < gamma*g.deg[v]*(commDeg[comm[v]]-g.deg[v])/m2 {
			continue
		}

		clear(neighW)

		for _, e := range g.adj[v] {
			if e.to == v || comm[e.to] != comm[v] {
				continue
			}

			neighW[ref[e.to]] += e.w
		}

		best := ref[v]
		bestGain := 0.0

		keys := make([]int, 0, len(neighW))
		for k := range neighW {
			keys = append(keys, k)
		}

		sort.Ints(keys)

		for _, r := range keys {
			if r == ref[v] {
				continue
			}

			gain := neighW[r] - gamma*g.deg[v]*refDeg[r]/m2
			if gain > bestGain+1e-12 {
				best, bestGain = r, gain
			}
		}

		if best != ref[v] {
			refDeg[ref[v]] -= g.deg[v]
			refSize[ref[v]]--
			ref[v] = best
			refDeg[best] += g.deg[v]
			refSize[best]++
		}
	}

	return ref
}

func aggregate(g *wgraph, refined, comm []int) (*wgraph, []int, []int) {
	idx := map[int]int{}
	aggMap := make([]int, g.n)

	for i, r := range refined {
		a, ok := idx[r]
		if !ok {
			a = len(idx)
			idx[r] = a
		}

		aggMap[i] = a
	}

	na := len(idx)
	agg := &wgraph{n: na, adj: make([][]wedge, na), deg: make([]float64, na), weight: make([]float64, na)}

	init := make([]int, na)
	for i := range g.n {
		init[aggMap[i]] = comm[i]
		agg.weight[aggMap[i]] += g.weight[i]
	}

	type pair struct{ a, b int }

	w := map[pair]float64{}

	for v := range g.n {
		for _, e := range g.adj[v] {
			if e.to < v {
				continue
			}

			a, b := aggMap[v], aggMap[e.to]
			if a > b {
				a, b = b, a
			}

			w[pair{a, b}] += e.w
		}
	}

	ps := make([]pair, 0, len(w))
	for p := range w {
		ps = append(ps, p)
	}

	sort.Slice(ps, func(i, j int) bool {
		if ps[i].a != ps[j].a {
			return ps[i].a < ps[j].a
		}

		return ps[i].b < ps[j].b
	})

	for _, p := range ps {
		agg.addEdge(p.a, p.b, w[p])
	}

	return agg, aggMap, renumber(init)
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}

	return f
}
