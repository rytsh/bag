package langs

import (
	"maps"
	"slices"
	"strings"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/model"
)

func isCSharpNode(n *model.Node) bool {
	return n != nil && n.SourceFile != "" && strings.HasSuffix(n.SourceFile, ".cs")
}

// csharpMethodName returns a method node's bare, case-preserved name.
func csharpMethodName(n *model.Node) string {
	label := strings.TrimPrefix(strings.TrimSpace(n.Label), ".")
	name, _, _ := strings.Cut(label, "(")

	return name
}

// resolveCSharpInterfaceDispatch links each single-implementer interface
// method to its implementation with an INFERRED dispatches_to edge.
//
// Adapted from Graphify's resolve_csharp_interface_dispatch (Apache-2.0).
func resolveCSharpInterfaceDispatch(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, _ []extract.FileResult) {
	nodes, edges := *nodesP, *edgesP

	hasCS := false
	byID := make(map[string]*model.Node, len(nodes))

	for _, n := range nodes {
		byID[n.ID] = n
		if !hasCS && strings.HasSuffix(n.SourceFile, ".cs") {
			hasCS = true
		}
	}

	if !hasCS {
		return
	}

	implementers := map[string]map[string]bool{}
	var implOrder []string
	methodsOf := map[string]map[string][]string{}

	for _, e := range edges {
		switch e.Relation {
		case "implements":
			if implementers[e.Target] == nil {
				implementers[e.Target] = map[string]bool{}
				implOrder = append(implOrder, e.Target)
			}

			implementers[e.Target][e.Source] = true
		case "method":
			m := byID[e.Target]
			if !isCSharpNode(m) {
				continue
			}

			name := csharpMethodName(m)
			if name == "" {
				continue
			}

			if methodsOf[e.Source] == nil {
				methodsOf[e.Source] = map[string][]string{}
			}

			if !slices.Contains(methodsOf[e.Source][name], e.Target) {
				methodsOf[e.Source][name] = append(methodsOf[e.Source][name], e.Target)
			}
		}
	}

	if len(implementers) == 0 || len(methodsOf) == 0 {
		return
	}

	existing := map[[2]string]bool{}

	for _, e := range edges {
		if e.Relation == "dispatches_to" {
			existing[[2]string{e.Source, e.Target}] = true
		}
	}

	for _, iface := range implOrder {
		impls := implementers[iface]
		if len(impls) != 1 {
			continue
		}

		var impl string
		for k := range impls {
			impl = k
		}

		ifaceNode, implNode := byID[iface], byID[impl]
		if !isCSharpNode(ifaceNode) || !isCSharpNode(implNode) {
			continue
		}

		implMethods := methodsOf[impl]

		for _, name := range slices.Sorted(maps.Keys(methodsOf[iface])) {
			declared := methodsOf[iface][name]
			if len(declared) != 1 {
				continue
			}

			cands := implMethods[name]
			if len(cands) != 1 {
				continue
			}

			src, tgt := declared[0], cands[0]
			if src == tgt || existing[[2]string{src, tgt}] {
				continue
			}

			existing[[2]string{src, tgt}] = true
			edges = append(edges, &model.Edge{
				Source: src, Target: tgt, Relation: "dispatches_to", Context: "call",
				Confidence: model.Inferred, ConfidenceScore: model.Score(0.9),
				SourceFile: implNode.SourceFile, SourceLocation: implNode.SourceLocation, Weight: 1,
			})
		}
	}

	*edgesP = edges
}
