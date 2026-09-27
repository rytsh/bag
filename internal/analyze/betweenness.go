package analyze

import (
	"math/rand/v2"
	"sort"

	"github.com/rytsh/bag/internal/graph"
	"github.com/rytsh/bag/internal/model"
)

// Betweenness computes normalized node betweenness centrality (Brandes).
// When k > 0, k pivot nodes are sampled with a fixed seed.
func Betweenness(g *graph.Graph, k int) map[string]float64 {
	ids := g.NodeIDs()
	sort.Strings(ids)

	n := len(ids)
	cb := make(map[string]float64, n)

	for _, id := range ids {
		cb[id] = 0
	}

	sources := ids
	if k > 0 && k < n {
		rng := rand.New(rand.NewPCG(42, 42)) //nolint:gosec // deterministic sampling
		perm := rng.Perm(n)
		sources = make([]string, k)

		for i := range k {
			sources[i] = ids[perm[i]]
		}
	}

	for _, s := range sources {
		brandes(g, s, func(v string, delta float64) { cb[v] += delta }, nil)
	}

	// undirected: divide by 2, then normalize by (n-1)(n-2)/2
	scale := 1.0
	if n > 2 {
		scale = 1.0 / (float64(n-1) * float64(n-2))
	}

	if k > 0 && k < n {
		scale *= float64(n) / float64(k)
	}

	for id := range cb {
		cb[id] *= scale
	}

	return cb
}

// EdgeBetweenness computes normalized edge betweenness centrality.
func EdgeBetweenness(g *graph.Graph) map[*model.Edge]float64 {
	ids := g.NodeIDs()
	sort.Strings(ids)

	n := len(ids)
	out := map[*model.Edge]float64{}

	for _, e := range g.Edges() {
		out[e] = 0
	}

	for _, s := range ids {
		brandes(g, s, nil, func(e *model.Edge, delta float64) { out[e] += delta })
	}

	scale := 1.0
	if n > 1 {
		scale = 1.0 / (float64(n) * float64(n-1))
	}

	for e := range out {
		out[e] *= scale
	}

	return out
}

func brandes(g *graph.Graph, s string, nodeAcc func(string, float64), edgeAcc func(*model.Edge, float64)) {
	var stack []string

	pred := map[string][]string{}
	sigma := map[string]float64{s: 1}
	dist := map[string]int{s: 0}
	queue := []string{s}

	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		stack = append(stack, v)

		for _, w := range g.Neighbors(v) {
			if w == v {
				continue
			}

			if _, ok := dist[w]; !ok {
				dist[w] = dist[v] + 1
				queue = append(queue, w)
			}

			if dist[w] == dist[v]+1 {
				sigma[w] += sigma[v]
				pred[w] = append(pred[w], v)
			}
		}
	}

	delta := map[string]float64{}

	for i := len(stack) - 1; i >= 0; i-- {
		w := stack[i]

		for _, v := range pred[w] {
			c := sigma[v] / sigma[w] * (1 + delta[w])
			if edgeAcc != nil {
				edgeAcc(g.Edge(v, w), c)
			}

			delta[v] += c
		}

		if w != s && nodeAcc != nil {
			nodeAcc(w, delta[w])
		}
	}
}
