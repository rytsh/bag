package langs

import (
	"strings"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/model"
)

// Swift receiver typing: a per-file name -> type table from property types,
// constructor initializers, `Type.shared` accesses, parameter types and
// local bindings, plus pending `x = Factory.make()` bindings resolved
// against the factory's plain return type.
//
// Adapted from Graphify's Swift type table, _swift_local_var_types and
// _resolve_swift_member_calls (Apache-2.0).

type swiftTables struct {
	table   map[string]string
	factory map[string][2]string
}

func swiftTablesOf(x *generic.Ctx) *swiftTables {
	t, _ := x.Data["swift_tables"].(*swiftTables)
	if t == nil {
		t = &swiftTables{table: map[string]string{}, factory: map[string][2]string{}}
		x.Data["swift_tables"] = t
	}

	return t
}

func swiftHeadIdent(n *tsx.Node) *tsx.Node {
	if n == nil {
		return nil
	}

	switch n.Type() {
	case "simple_identifier":
		return n
	case "user_type":
		if len(n.NamedChildren()) == 1 {
			if c := n.ChildOfType("type_identifier"); c != nil {
				return c
			}
		}
	}

	return nil
}

// swiftConstructorType returns Type for `Type()`.
func swiftConstructorType(call *tsx.Node) string {
	ch := call.Children()
	if len(ch) == 0 {
		return ""
	}

	if h := swiftHeadIdent(ch[0]); h != nil && upperStart(h.Text()) {
		return h.Text()
	}

	return ""
}

// swiftFactoryCall returns (Factory, method) for `Factory.make()`.
func swiftFactoryCall(call *tsx.Node) (string, string, bool) {
	ch := call.Children()
	if len(ch) == 0 || ch[0].Type() != "navigation_expression" {
		return "", "", false
	}

	named := ch[0].NamedChildren()
	if len(named) != 2 || named[1].Type() != "navigation_suffix" {
		return "", "", false
	}

	h := swiftHeadIdent(named[0])
	if h == nil || !upperStart(h.Text()) {
		return "", "", false
	}

	m := named[1].ChildOfType("simple_identifier")
	if m == nil || m.Text() == "" {
		return "", "", false
	}

	return h.Text(), m.Text(), true
}

func swiftPropertyName(n *tsx.Node) string {
	for _, c := range n.Children() {
		if c.Type() == "pattern" {
			if s := c.ChildOfType("simple_identifier"); s != nil {
				return s.Text()
			}
		}

		if c.Type() == "simple_identifier" {
			return c.Text()
		}
	}

	return ""
}

func swiftNavHeadUpper(n *tsx.Node) string {
	if n.Type() != "navigation_expression" {
		return ""
	}

	ch := n.Children()
	if len(ch) == 0 {
		return ""
	}

	if h := swiftHeadIdent(ch[0]); h != nil && upperStart(h.Text()) {
		return h.Text()
	}

	return ""
}

func swiftAttributeTypeName(prop *tsx.Node) string {
	for _, c := range prop.Children() {
		if c.Type() != "modifiers" {
			continue
		}

		for _, a := range c.Children() {
			if a.Type() != "attribute" {
				continue
			}

			head := a.ChildOfType("user_type")
			if head == nil || head.Text() != "Environment" {
				continue
			}

			arg := a.ChildOfType("navigation_expression")
			if arg == nil {
				continue
			}

			named := arg.NamedChildren()
			if len(named) != 2 || named[1].Type() != "navigation_suffix" || named[1].Text() != ".self" {
				continue
			}

			if h := swiftHeadIdent(named[0]); h != nil && upperStart(h.Text()) {
				return h.Text()
			}
		}
	}

	return ""
}

// swiftRecordProperty records a class-level property's type (annotation,
// constructor, `Type.x` access, or @Environment) or a pending factory.
func swiftRecordProperty(x *generic.Ctx, n *tsx.Node, annotated string) {
	st := swiftTablesOf(x)
	typ := annotated

	var (
		fac    [2]string
		hasFac bool
	)

	for _, c := range n.Children() {
		if x.Cfg.CallTypes.Has(c.Type()) {
			if typ == "" {
				if ct := swiftConstructorType(c); ct != "" {
					typ = ct
				} else if f, m, ok := swiftFactoryCall(c); ok {
					fac, hasFac = [2]string{f, m}, true
				}
			}
		} else if c.Type() == "navigation_expression" && typ == "" {
			typ = swiftNavHeadUpper(c)
		}
	}

	if typ == "" {
		typ = swiftAttributeTypeName(n)
	}

	name := swiftPropertyName(n)

	switch {
	case name != "" && typ != "":
		st.table[name] = typ
	case name != "" && hasFac:
		if _, ok := st.factory[name]; !ok {
			st.factory[name] = fac
		}
	}
}

func swiftLocalVarTypes(body *tsx.Node, st *swiftTables) {
	stack := []*tsx.Node{body}

	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if n.Type() == "function_declaration" && !n.Same(body) {
			continue
		}

		if n.Type() == "property_declaration" {
			typ := ""

			var (
				fac    [2]string
				hasFac bool
			)

			for _, c := range n.Children() {
				if c.Type() == "call_expression" {
					if typ = swiftConstructorType(c); typ == "" {
						fac[0], fac[1], hasFac = swiftFactoryCall(c)
					}

					break
				}

				if c.Type() == "navigation_expression" {
					typ = swiftNavHeadUpper(c)

					break
				}
			}

			name := swiftPropertyName(n)
			if name != "" {
				_, inTable := st.table[name]
				_, inFac := st.factory[name]

				switch {
				case typ != "" && !inTable:
					st.table[name] = typ
				case hasFac && !inTable && !inFac:
					st.factory[name] = fac
				}
			}
		}

		stack = append(stack, n.Children()...)
	}
}

// swiftReceiverName returns the depth-1 receiver of `recv.m()`:
// `x` -> x, `Type.shared.m()` -> Type, `self.svc.m()` -> svc.
func swiftReceiverName(recv *tsx.Node) string {
	if recv == nil {
		return ""
	}

	if h := swiftHeadIdent(recv); h != nil {
		return h.Text()
	}

	if recv.Type() != "navigation_expression" {
		return ""
	}

	ch := recv.Children()
	if len(ch) == 0 {
		return ""
	}

	if h := swiftHeadIdent(ch[0]); h != nil {
		return h.Text()
	}

	if ch[0].Type() == "self_expression" {
		for _, c := range ch {
			if c.Type() == "navigation_suffix" {
				if s := c.ChildOfType("simple_identifier"); s != nil {
					return s.Text()
				}
			}
		}
	}

	return ""
}

func swiftDecorateRawCall(_ *generic.Ctx, n *tsx.Node, rc *model.RawCall) {
	if !rc.IsMemberCall {
		return
	}

	ch := n.Children()
	if len(ch) == 0 || ch[0].Type() != "navigation_expression" {
		return
	}

	if fc := ch[0].Children(); len(fc) > 0 {
		if r := swiftReceiverName(fc[0]); r != "" {
			rc.Receiver = r
		}
	}
}

func swiftPostProcess(x *generic.Ctx, res *model.Extraction) {
	st := swiftTablesOf(x)

	for _, bd := range x.Bodies() {
		swiftLocalVarTypes(bd, st)
	}

	if len(st.table) > 0 {
		res.TypeTable = st.table
	}

	if len(st.factory) > 0 {
		res.Factory = st.factory
	}
}

// resolveSwiftMemberCalls types `recv.m()` receivers through the file's
// table (or an upper-case type receiver) and binds to that type's method,
// or the type itself when it owns no such method. Like Graphify, it walks
// every language's raw calls (Swift raw calls carry no language tag), so
// upper-case receivers elsewhere resolve too once a Swift file is present.
func resolveSwiftMemberCalls(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	tables := map[string]map[string]string{}
	factories := map[string]map[string][2]string{}

	for _, fr := range per {
		if !strings.HasSuffix(fr.Path, ".swift") {
			continue
		}

		if fr.Ex.TypeTable != nil {
			tables[fr.Path] = fr.Ex.TypeTable
		}

		if fr.Ex.Factory != nil {
			factories[fr.Path] = fr.Ex.Factory
		}
	}

	if len(tables) == 0 && len(factories) == 0 {
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

	for _, e := range edges {
		if e.Relation == "method" {
			if t := byID[e.Target]; t != nil {
				methods[[2]string{e.Source, memberKey(t.Label)}] = e.Target
			}
		}
	}

	if len(factories) > 0 {
		returns := map[string]map[string]bool{}

		for _, e := range edges {
			if e.Relation == "references" && e.Context == "return_type" {
				if b, _ := e.Metadata["swift_plain_return"].(bool); b {
					if returns[e.Source] == nil {
						returns[e.Source] = map[string]bool{}
					}

					returns[e.Source][e.Target] = true
				}
			}
		}

		for path, pending := range factories {
			table := map[string]string{}
			for k, v := range tables[path] {
				table[k] = v
			}

			tables[path] = table

			for recv, bind := range pending {
				if base.BuiltinGlobals.Has(bind[0]) {
					continue
				}

				defs := typeDefs[memberKey(bind[0])]
				if len(defs) != 1 {
					continue
				}

				m, ok := methods[[2]string{defs[0], memberKey(bind[1])}]
				if !ok || len(returns[m]) != 1 {
					continue
				}

				var ret string
				for t := range returns[m] {
					if n := byID[t]; n != nil {
						ret = n.Label
					}
				}

				if ret == "" || base.BuiltinGlobals.Has(ret) || len(typeDefs[memberKey(ret)]) != 1 {
					continue
				}

				if _, ok := table[recv]; !ok {
					table[recv] = ret
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

			typeName, qualified := "", false

			if upperStart(rc.Receiver) {
				typeName, qualified = rc.Receiver, true
			} else {
				typeName = tables[rc.SourceFile][rc.Receiver]
			}

			if typeName == "" || base.BuiltinGlobals.Has(typeName) {
				continue
			}

			defs := typeDefs[memberKey(typeName)]
			if len(defs) == 0 && strings.HasSuffix(strings.ToLower(rc.SourceFile), ".swift") {
				parkUnresolvedMemberCall(byID[rc.CallerID], rc.Callee, typeName, "swift", rc)
			}

			if len(defs) != 1 {
				continue
			}

			target, rel := defs[0], "references"
			if m, ok := methods[[2]string{defs[0], memberKey(rc.Callee)}]; ok {
				target, rel = m, "calls"
			}

			if target == rc.CallerID || existing[[2]string{rc.CallerID, target}] {
				continue
			}

			existing[[2]string{rc.CallerID, target}] = true

			conf, score := model.Inferred, 0.8
			if qualified {
				conf, score = model.Extracted, 1.0
			}

			edges = append(edges, &model.Edge{
				Source: rc.CallerID, Target: target, Relation: rel, Context: "call",
				Confidence: conf, ConfidenceScore: model.Score(score),
				SourceFile: rc.SourceFile, SourceLocation: rc.SourceLocation, Weight: 1,
			})
		}
	}

	*edgesP = edges
}
