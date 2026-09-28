package langs

import (
	"strings"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/model"
)

// resolveObjCMemberCalls binds Objective-C message sends (`[recv sel]`) to
// the receiver type's method: self/super and capitalized class receivers
// exactly, locals and `self.field`/ivar receivers as INFERRED, only when the
// type has exactly one (non-protocol) definition.
//
// Adapted from Graphify's _resolve_objc_member_calls (Apache-2.0).
func resolveObjCMemberCalls(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	var calls []*model.RawCall

	tables := map[string]map[string]string{}

	for _, fr := range per {
		if fr.Ex == nil {
			continue
		}

		for _, rc := range fr.Ex.RawCalls {
			if rc.Language == "objc" && rc.IsMemberCall {
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

	for _, n := range nodes {
		byID[n.ID] = n

		l := strings.TrimSpace(n.Label)
		isProtocol := strings.HasPrefix(l, "<") && strings.HasSuffix(l, ">")

		if n.SourceFile != "" && contained[n.ID] && isTypeLikeDef(n) && !isProtocol {
			typeDefs[memberKey(n.Label)] = append(typeDefs[memberKey(n.Label)], n.ID)
		}
	}

	methods := map[[2]string]string{}
	enclosing := map[string]string{}
	bases := map[string][]string{}

	for _, e := range edges {
		switch e.Relation {
		case "method":
			if _, ok := enclosing[e.Target]; !ok {
				enclosing[e.Target] = e.Source
			}

			if t := byID[e.Target]; t != nil {
				methods[[2]string{e.Source, memberKey(t.Label)}] = e.Target
			}
		case "inherits":
			bases[e.Source] = append(bases[e.Source], e.Target)
		}
	}

	fields := objcFieldTables(nodes, per)

	fieldUpChain := func(cls, field string) string {
		seen := map[string]bool{}
		queue := []string{cls}

		for len(queue) > 0 {
			c := queue[0]
			queue = queue[1:]

			if c == "" || seen[c] {
				continue
			}

			seen[c] = true

			if t := fields[c][field]; t != "" {
				return t
			}

			queue = append(queue, bases[c]...)
		}

		return ""
	}

	unique := func(name string) string {
		if d := typeDefs[memberKey(name)]; len(d) == 1 {
			return d[0]
		}

		return ""
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
			typeID string
			exact  bool
		)

		switch {
		case rc.ReceiverType == objcSelfField:
			if cls := enclosing[caller]; cls != "" {
				typeID = unique(fieldUpChain(cls, recv))
			}
		case recv == "self" || recv == "super":
			typeID, exact = enclosing[caller], true
		case upperStart(recv):
			typeID, exact = unique(recv), true
		default:
			t := tables[rc.SourceFile][recv]
			if t == "" {
				if cls := enclosing[caller]; cls != "" {
					t = fieldUpChain(cls, recv)
				}
			}

			if t != "" {
				typeID = unique(t)
			}
		}

		if typeID == "" {
			continue
		}

		target, rel := typeID, "references"
		if m := methods[[2]string{typeID, memberKey(callee)}]; m != "" {
			target, rel = m, "calls"
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

// objcFieldTables merges per-file field tables onto class nodes; a
// cross-file conflict on one (class, field) drops the entry.
func objcFieldTables(nodes []*model.Node, per []extract.FileResult) map[string]map[string]string {
	byLabel := map[string][]string{}

	for _, n := range nodes {
		if n.SourceFile != "" && n.Label != "" {
			byLabel[n.Label] = append(byLabel[n.Label], n.ID)
		}
	}

	out := map[string]map[string]string{}
	conflict := map[[2]string]bool{}

	for _, fr := range per {
		if fr.Ex == nil {
			continue
		}

		for label, tbl := range fr.Ex.FieldTables {
			ids := byLabel[label]
			if len(ids) != 1 {
				continue
			}

			m := out[ids[0]]
			if m == nil {
				m = map[string]string{}
				out[ids[0]] = m
			}

			for f, t := range tbl {
				k := [2]string{ids[0], f}
				if conflict[k] {
					continue
				}

				if prev, ok := m[f]; ok && prev != t {
					delete(m, f)
					conflict[k] = true

					continue
				}

				m[f] = t
			}
		}
	}

	return out
}
