// Package graph is bag's in-memory knowledge graph.
//
// It stores an undirected simple graph (one edge per unordered node pair)
// while remembering the true source/target direction of each edge, exactly
// like Graphify's NetworkX-based build.
package graph

import (
	"sort"

	"github.com/rytsh/bag/internal/model"
)

// Graph is an undirected simple graph with attributed nodes and edges.
type Graph struct {
	nodes map[string]*model.Node
	order []string
	adj   map[string]map[string]*model.Edge

	Hyperedges []*model.Hyperedge
	Meta       map[string]any
}

// New returns an empty graph.
func New() *Graph {
	return &Graph{
		nodes: map[string]*model.Node{},
		adj:   map[string]map[string]*model.Edge{},
		Meta:  map[string]any{},
	}
}

// AddNode inserts or replaces a node.
func (g *Graph) AddNode(n *model.Node) {
	if _, ok := g.nodes[n.ID]; !ok {
		g.order = append(g.order, n.ID)
		g.adj[n.ID] = map[string]*model.Edge{}
	}

	g.nodes[n.ID] = n
}

// RemoveNode deletes a node and its incident edges.
func (g *Graph) RemoveNode(id string) {
	if _, ok := g.nodes[id]; !ok {
		return
	}

	for nb := range g.adj[id] {
		delete(g.adj[nb], id)
	}

	delete(g.adj, id)
	delete(g.nodes, id)

	for i, o := range g.order {
		if o == id {
			g.order = append(g.order[:i], g.order[i+1:]...)

			break
		}
	}
}

// Node returns a node by id.
func (g *Graph) Node(id string) *model.Node { return g.nodes[id] }

// Has reports node membership.
func (g *Graph) Has(id string) bool {
	_, ok := g.nodes[id]

	return ok
}

// Nodes returns nodes in insertion order.
func (g *Graph) Nodes() []*model.Node {
	out := make([]*model.Node, 0, len(g.order))
	for _, id := range g.order {
		out = append(out, g.nodes[id])
	}

	return out
}

// NodeIDs returns node ids in insertion order.
func (g *Graph) NodeIDs() []string { return append([]string(nil), g.order...) }

// NumNodes returns the node count.
func (g *Graph) NumNodes() int { return len(g.nodes) }

// SetEdge sets (replaces) the edge between e.Source and e.Target.
func (g *Graph) SetEdge(e *model.Edge) {
	g.adj[e.Source][e.Target] = e
	g.adj[e.Target][e.Source] = e
}

// Edge returns the edge between u and v (either direction).
func (g *Graph) Edge(u, v string) *model.Edge {
	if m, ok := g.adj[u]; ok {
		return m[v]
	}

	return nil
}

// HasEdge reports whether u and v are adjacent.
func (g *Graph) HasEdge(u, v string) bool { return g.Edge(u, v) != nil }

// Neighbors returns the sorted neighbor ids of id.
func (g *Graph) Neighbors(id string) []string {
	m := g.adj[id]
	out := make([]string, 0, len(m))

	for nb := range m {
		out = append(out, nb)
	}

	sort.Strings(out)

	return out
}

// Degree returns the node degree (a self-loop counts twice, as in NetworkX).
func (g *Graph) Degree(id string) int {
	d := len(g.adj[id])
	if _, self := g.adj[id][id]; self {
		d++
	}

	return d
}

// Edges returns every edge once.
func (g *Graph) Edges() []*model.Edge {
	seen := map[*model.Edge]bool{}

	var out []*model.Edge

	for _, u := range g.order {
		for _, e := range g.adj[u] {
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
	}

	return out
}

// NumEdges returns the edge count.
func (g *Graph) NumEdges() int { return len(g.Edges()) }

// InEdges returns edges whose true target is id.
func (g *Graph) InEdges(id string) []*model.Edge {
	var out []*model.Edge

	for _, e := range g.adj[id] {
		if e.Target == id {
			out = append(out, e)
		}
	}

	return out
}

// OutEdges returns edges whose true source is id.
func (g *Graph) OutEdges(id string) []*model.Edge {
	var out []*model.Edge

	for _, e := range g.adj[id] {
		if e.Source == id {
			out = append(out, e)
		}
	}

	return out
}

// Subgraph returns a new graph induced by ids.
func (g *Graph) Subgraph(ids []string) *Graph {
	sub := New()
	keep := map[string]bool{}

	for _, id := range ids {
		if n, ok := g.nodes[id]; ok {
			sub.AddNode(n)
			keep[id] = true
		}
	}

	for _, e := range g.Edges() {
		if keep[e.Source] && keep[e.Target] {
			sub.SetEdge(e)
		}
	}

	return sub
}
