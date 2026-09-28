package langs

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/model"
)

// tsReceiverTypeTable builds the per-file receiver -> type table:
// constructor parameter properties first, then `x = new Foo()` locals and
// bare type-annotated parameters (first binding wins).
//
// Adapted from Graphify's constructor-injection table and
// _ts_receiver_type_table (Apache-2.0).
func tsReceiverTypeTable(root *tsx.Node) map[string]string {
	table := map[string]string{}

	root.Walk(func(n *tsx.Node) bool {
		if n.Type() != "method_definition" {
			return true
		}

		if nn := n.Field("name"); nn == nil || nn.Text() != "constructor" {
			return true
		}

		params := n.Field("parameters")
		if params == nil {
			return true
		}

		for _, p := range params.Children() {
			if p.Type() != "required_parameter" {
				continue
			}

			if p.ChildOfType("accessibility_modifier", "readonly") == nil {
				continue
			}

			name, typ := p.Field("pattern"), p.Field("type")
			if name == nil || typ == nil {
				continue
			}

			if tc := typ.ChildOfType("type_identifier"); tc != nil && name.Text() != "" && tc.Text() != "" {
				table[name.Text()] = tc.Text()
			}
		}

		return true
	})

	bareType := func(ann *tsx.Node) string {
		var ident *tsx.Node

		for _, c := range ann.Children() {
			if c.Type() == "type_identifier" {
				if ident != nil {
					return ""
				}

				ident = c
			} else if c.IsNamed() {
				return ""
			}
		}

		if ident == nil {
			return ""
		}

		return ident.Text()
	}

	stack := []*tsx.Node{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		switch n.Type() {
		case "variable_declarator":
			nn, v := n.Field("name"), n.Field("value")
			if nn != nil && nn.Type() == "identifier" && v != nil && v.Type() == "new_expression" {
				if ctor := v.Field("constructor"); ctor != nil && (ctor.Type() == "identifier" || ctor.Type() == "type_identifier") {
					if name, t := nn.Text(), ctor.Text(); name != "" && t != "" {
						if _, ok := table[name]; !ok {
							table[name] = t
						}
					}
				}
			}
		case "required_parameter", "optional_parameter":
			pat, ann := n.Field("pattern"), n.Field("type")
			if pat != nil && pat.Type() == "identifier" && ann != nil {
				if t, name := bareType(ann), pat.Text(); t != "" && name != "" {
					if _, ok := table[name]; !ok {
						table[name] = t
					}
				}
			}
		}

		stack = append(stack, n.Children()...)
	}

	return table
}

func tsPostProcess(x *generic.Ctx, res *model.Extraction) {
	jsRescueDynamicImports(x, res)

	if t := tsReceiverTypeTable(x.Tree.Root); len(t) > 0 {
		res.TypeTable = t
	}
}

var memberKeyRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func memberKey(label string) string {
	return strings.ToLower(memberKeyRe.ReplaceAllString(label, ""))
}

func isTypeLikeDef(n *model.Node) bool {
	if n.Type == "namespace" {
		return false
	}

	label := strings.TrimSpace(n.Label)
	if label == "" || strings.HasSuffix(label, ")") || strings.HasPrefix(label, ".") || strings.Contains(label, ".") {
		return false
	}

	return n.FileType == model.FileTypeCode
}

func upperStart(s string) bool {
	for _, r := range s {
		return unicode.IsUpper(r)
	}

	return false
}

func extOf(p string) string { return filepath.Ext(p) }

// resolveTSMemberCalls binds `this.field.m()`, `local.m()`, `param.m()` and
// `Type.m()` member calls to the receiver type's method, gated on the caller's
// file actually seeing that type.
//
// Adapted from Graphify's _resolve_typescript_member_calls (Apache-2.0).
func resolveTSMemberCalls(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	tables := map[string]map[string]string{}

	for _, fr := range per {
		if fr.Ex.TypeTable != nil && jsSymbolSuffixes.Has(extOf(fr.Path)) {
			tables[fr.Path] = fr.Ex.TypeTable
		}
	}

	if len(tables) == 0 {
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
		if n.SourceFile != "" && contained[n.ID] && isTypeLikeDef(n) {
			k := memberKey(n.Label)
			typeDefs[k] = append(typeDefs[k], n.ID)
		}
	}

	methods := map[[2]string]string{}
	fileOf := map[string]string{}
	imported := map[string]map[string]bool{}

	for _, e := range edges {
		switch e.Relation {
		case "method":
			if t := byID[e.Target]; t != nil {
				methods[[2]string{e.Source, memberKey(t.Label)}] = e.Target
			}
		case "contains":
			fileOf[e.Target] = e.Source
		case "imports", "imports_from":
			if imported[e.Source] == nil {
				imported[e.Source] = map[string]bool{}
			}

			imported[e.Source][e.Target] = true
		}
	}

	for _, e := range edges {
		if e.Relation == "method" {
			if f, ok := fileOf[e.Source]; ok {
				if _, has := fileOf[e.Target]; !has {
					fileOf[e.Target] = f
				}
			}
		}
	}

	existing := map[[2]string]bool{}
	for _, e := range edges {
		existing[[2]string{e.Source, e.Target}] = true
	}

	for _, fr := range per {
		for _, rc := range fr.Ex.RawCalls {
			if !rc.IsMemberCall || rc.Receiver == "" || rc.Callee == "" || rc.CallerID == "" {
				continue
			}

			recv := strings.TrimPrefix(rc.Receiver, "this.")
			typeName, qualified := "", false

			if upperStart(recv) {
				typeName, qualified = recv, true
			} else {
				typeName = tables[rc.SourceFile][recv]
			}

			if typeName == "" || base.BuiltinGlobals.Has(typeName) {
				continue
			}

			defs := typeDefs[memberKey(typeName)]
			if len(defs) != 1 {
				continue
			}

			typeID := defs[0]
			callerFile, hasCF := fileOf[rc.CallerID]
			typeFile, hasTF := fileOf[typeID]
			imp := imported[callerFile]

			if !((hasCF && hasTF && callerFile == typeFile) || imp[typeID] || (hasTF && imp[typeFile])) {
				continue
			}

			target, ok := methods[[2]string{typeID, memberKey(rc.Callee)}]
			if !ok || target == rc.CallerID || existing[[2]string{rc.CallerID, target}] {
				continue
			}

			existing[[2]string{rc.CallerID, target}] = true

			conf, score := model.Inferred, 0.8
			if qualified {
				conf, score = model.Extracted, 1.0
			}

			edges = append(edges, &model.Edge{
				Source: rc.CallerID, Target: target, Relation: "calls", Context: "call",
				Confidence: conf, ConfidenceScore: model.Score(score),
				SourceFile: rc.SourceFile, SourceLocation: rc.SourceLocation, Weight: 1,
			})
		}
	}

	*edgesP = edges
}
