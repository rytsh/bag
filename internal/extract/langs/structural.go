package langs

import (
	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/model"
)

// structuralBuilder preserves Graphify's bespoke Solidity/VB.NET metadata and
// edge deduplication. Their signature-based IDs differ from generic.Config.
type structuralBuilder struct {
	*base.Builder
	language string
	edges    map[[3]string]bool
}

func newStructuralBuilder(path, language string) *structuralBuilder {
	return &structuralBuilder{Builder: base.NewBuilder(path), language: language, edges: map[[3]string]bool{}}
}

func (b *structuralBuilder) node(id, label, kind string, n *tsx.Node, sourced, callable bool) string {
	if b.Has(id) {
		return id
	}
	item := b.AddNode(id, label, n.Line())
	item.Metadata = map[string]any{"language": b.language, "kind": kind}
	item.MetaOrder = []string{"language", "kind"}
	item.Callable = callable
	if !sourced {
		item.SourceFile = ""
	}
	return id
}

func (b *structuralBuilder) edge(from, to, relation string, n *tsx.Node) *model.Edge {
	key := [3]string{from, to, relation}
	if from == "" || to == "" || from == to || b.edges[key] {
		return nil
	}
	b.edges[key] = true
	return b.AddEdge(from, to, relation, n.Line())
}

func namedOfType(n *tsx.Node, kinds ...string) []*tsx.Node {
	var out []*tsx.Node
	if n == nil {
		return out
	}
	for _, c := range n.NamedChildren() {
		for _, kind := range kinds {
			if c.Type() == kind {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

func walkStructural(n *tsx.Node, visit func(*tsx.Node) bool) {
	if n == nil || !visit(n) {
		return
	}
	for _, c := range n.NamedChildren() {
		walkStructural(c, visit)
	}
}
