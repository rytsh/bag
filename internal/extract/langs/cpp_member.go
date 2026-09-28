package langs

import (
	"strings"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/model"
)

// resolveCppMemberCalls binds C++ member calls to the receiver type's
// definition: `Foo::bar()` and `this->bar()` (EXTRACTED) and `f.bar()` /
// `f->bar()` typed through the file's local table (INFERRED), only when the
// type has exactly one definition. Calls into types declared nowhere are
// parked on the caller.
//
// Adapted from Graphify's _resolve_cpp_member_calls (Apache-2.0).
func resolveCppMemberCalls(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	var calls []*model.RawCall

	tables := map[string]map[string]string{}

	for _, fr := range per {
		if fr.Ex == nil {
			continue
		}

		for _, rc := range fr.Ex.RawCalls {
			if rc.Language == "cpp" && rc.IsMemberCall {
				calls = append(calls, rc)

				if fr.Ex.TypeTable != nil {
					tables[fr.Path] = fr.Ex.TypeTable
				}
			}
		}
	}

	if len(calls) == 0 {
		return
	}

	nodes, edges := *nodesP, *edgesP

	contained := map[string]bool{}
	for _, e := range edges {
		if e.Relation == "contains" {
			contained[e.Target] = true
		}
	}

	byID := make(map[string]*model.Node, len(nodes))
	typeDefs := map[string][]string{}
	qualified := map[string][]string{}

	for _, n := range nodes {
		byID[n.ID] = n

		if n.SourceFile != "" && contained[n.ID] && isTypeLikeDef(n) {
			k := memberKey(n.Label)
			typeDefs[k] = append(typeDefs[k], n.ID)
		}

		if n.SourceFile != "" && strings.HasSuffix(n.Label, "()") && strings.Contains(n.Label, "::") {
			qualified[n.Label] = append(qualified[n.Label], n.ID)
		}
	}

	methods := map[[2]string]string{}
	enclosing := map[string]string{}

	for _, rel := range []string{"defines", "method"} {
		for _, e := range edges {
			if e.Relation != rel {
				continue
			}

			t := byID[e.Target]
			if t == nil {
				continue
			}

			if _, ok := enclosing[e.Target]; !ok {
				enclosing[e.Target] = e.Source
			}

			methods[[2]string{e.Source, memberKey(t.Label)}] = e.Target
		}
	}

	existing := map[[2]string]bool{}
	for _, e := range edges {
		existing[[2]string{e.Source, e.Target}] = true
	}

	for _, rc := range calls {
		recv, callee, caller := rc.Receiver, rc.Callee, rc.CallerID
		if recv == "" || callee == "" || caller == "" {
			continue
		}

		var (
			typeID, fallback string
			exact            bool
		)

		switch {
		case recv == "this":
			if typeID = enclosing[caller]; typeID == "" {
				continue
			}

			exact = true
		case upperStart(recv):
			fallback = recv + "::" + callee + "()"

			defs := typeDefs[memberKey(recv)]
			if len(defs) == 0 {
				parkUnresolvedMemberCall(byID[caller], callee, recv, "cpp", rc)

				continue
			}

			if len(defs) != 1 {
				continue
			}

			typeID, exact = defs[0], true
		default:
			tname := tables[rc.SourceFile][recv]
			if tname == "" {
				continue
			}

			defs := typeDefs[memberKey(tname)]
			if len(defs) == 0 {
				parkUnresolvedMemberCall(byID[caller], callee, tname, "cpp", rc)

				continue
			}

			if len(defs) != 1 {
				continue
			}

			typeID = defs[0]
		}

		method := methods[[2]string{typeID, memberKey(callee)}]
		if method == "" && fallback != "" {
			if c := qualified[fallback]; len(c) == 1 {
				method = c[0]
			}
		}

		target, rel := typeID, "references"
		if method != "" {
			target, rel = method, "calls"
		}

		if target == caller || existing[[2]string{caller, target}] {
			continue
		}

		existing[[2]string{caller, target}] = true

		conf, score := model.Inferred, 0.8
		if exact {
			conf, score = model.Extracted, 1.0
		}

		edges = append(edges, &model.Edge{
			Source: caller, Target: target, Relation: rel, Context: "call",
			Confidence: conf, ConfidenceScore: model.Score(score),
			SourceFile: rc.SourceFile, SourceLocation: rc.SourceLocation, Weight: 1,
		})
	}

	*nodesP, *edgesP = nodes, edges
}
