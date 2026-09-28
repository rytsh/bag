package langs

import (
	"strings"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/model"
)

// resolveRustSelfMemberCalls binds `self.method()` inside `impl Foo` to Foo's
// method, pooling methods across every impl block of Foo (split impls across
// files) and emitting only when exactly one method in the pool matches.
// Pooling is skipped when two unrelated types share the bare name; simple
// generic impls use their owner/arity key instead.
//
// Adapted from Graphify's _resolve_rust_self_member_calls (Apache-2.0).
func resolveRustSelfMemberCalls(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	var calls []*model.RawCall

	for _, fr := range per {
		if fr.Ex == nil {
			continue
		}

		for _, rc := range fr.Ex.RawCalls {
			if rc.RustSelfType != "" && rc.Callee != "" && rc.CallerID != "" {
				calls = append(calls, rc)
			}
		}
	}

	if len(calls) == 0 {
		return
	}

	nodes, edges := *nodesP, *edgesP

	byID := make(map[string]*model.Node, len(nodes))
	byLabel := map[string][]string{}
	byImplKey := map[string][]string{}

	for _, n := range nodes {
		byID[n.ID] = n

		if strings.HasSuffix(n.SourceFile, ".rs") {
			byLabel[n.Label] = append(byLabel[n.Label], n.ID)

			if n.RustImplKey != "" {
				byImplKey[n.RustImplKey] = append(byImplKey[n.RustImplKey], n.ID)
			}
		}
	}

	contained := map[string]bool{}
	for _, e := range edges {
		if e.Relation == "contains" {
			contained[e.Target] = true
		}
	}

	declared := map[string]int{}
	genericDeclared := map[string]int{}

	for label, ids := range byLabel {
		for _, id := range ids {
			if contained[id] {
				declared[label]++
			}

			if c := byID[id].RustDeclCount; c > 0 {
				genericDeclared[label] += c
			}
		}
	}

	methods := map[[2]string]map[string]bool{}

	for _, e := range edges {
		if e.Relation != "method" {
			continue
		}

		t := byID[e.Target]
		if t == nil {
			continue
		}

		k := [2]string{e.Source, strings.TrimLeft(strings.Trim(t.Label, "()"), ".")}
		if methods[k] == nil {
			methods[k] = map[string]bool{}
		}

		methods[k][e.Target] = true
	}

	existing := map[[2]string]bool{}

	for _, e := range edges {
		if e.Relation == "calls" {
			existing[[2]string{e.Source, e.Target}] = true
		}
	}

	for _, rc := range calls {
		var owners []string

		if rc.RustSelfImplKey != "" {
			if genericDeclared[rc.RustSelfType] != 1 {
				continue
			}

			owners = byImplKey[rc.RustSelfImplKey]
		} else {
			if declared[rc.RustSelfType] >= 2 {
				continue
			}

			owners = byLabel[rc.RustSelfType]
		}

		cands := map[string]bool{}

		for _, o := range owners {
			for m := range methods[[2]string{o, rc.Callee}] {
				cands[m] = true
			}
		}

		if len(cands) != 1 {
			continue
		}

		var tgt string
		for m := range cands {
			tgt = m
		}

		if tgt == rc.CallerID || existing[[2]string{rc.CallerID, tgt}] {
			continue
		}

		existing[[2]string{rc.CallerID, tgt}] = true
		edges = append(edges, &model.Edge{
			Source: rc.CallerID, Target: tgt, Relation: "calls", Context: "call",
			Confidence: model.Extracted, ConfidenceScore: model.Score(1.0),
			SourceFile: rc.SourceFile, SourceLocation: rc.SourceLocation, Weight: 1,
		})
	}

	*nodesP, *edgesP = nodes, edges
}
