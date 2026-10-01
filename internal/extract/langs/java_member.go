package langs

import (
	"strings"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/model"
)

// Java receiver typing: class field types plus method-scoped parameter and
// local bindings, stamped on member raw calls, then resolved corpus-wide.
//
// Adapted from Graphify's _java_method_receiver_types and
// _resolve_java_member_calls (Apache-2.0).

type javaMethodScope struct {
	method  *tsx.Node
	classID string
}

// javaReceiverTypeName returns the concrete declared type of a type node
// usable for receiver resolution, or "".
func javaReceiverTypeName(n *tsx.Node) string {
	if n == nil {
		return ""
	}

	var name string

	switch n.Type() {
	case "type_identifier":
		name = n.Text()
	case "scoped_type_identifier":
		name = lastSeg(n.Text(), ".")
	case "generic_type":
		return javaReceiverTypeName(n.ChildOfType("type_identifier", "scoped_type_identifier"))
	default:
		return ""
	}

	if name == "" || javaBuiltinTypes.Has(name) || javaTypeParams(n).Has(name) {
		return ""
	}

	return name
}

func javaDeclaratorNames(decl *tsx.Node) []string {
	var out []string

	for _, c := range decl.Children() {
		if c.Type() != "variable_declarator" {
			continue
		}

		if nn := c.Field("name"); nn != nil && nn.Text() != "" {
			out = append(out, nn.Text())
		}
	}

	return out
}

type javaBinding struct {
	name, typ string
}

func javaLambdaParams(l *tsx.Node) []javaBinding {
	ps := l.Field("parameters")
	if ps == nil {
		return nil
	}

	switch ps.Type() {
	case "identifier":
		return []javaBinding{{ps.Text(), ""}}
	case "inferred_parameters":
		var out []javaBinding

		for _, c := range ps.Children() {
			if c.Type() == "identifier" {
				out = append(out, javaBinding{c.Text(), ""})
			}
		}

		return out
	}

	var out []javaBinding

	for _, p := range ps.Children() {
		if p.Type() != "formal_parameter" && p.Type() != "spread_parameter" {
			continue
		}

		if nn := p.Field("name"); nn != nil {
			out = append(out, javaBinding{nn.Text(), javaReceiverTypeName(p.Field("type"))})
		}
	}

	return out
}

var javaNestedTypeDecls = map[string]bool{
	"class_declaration": true, "class_body": true, "interface_declaration": true,
	"record_declaration": true, "enum_declaration": true, "annotation_type_declaration": true,
}

// javaMethodReceiverTypes builds the receiver table visible to one method:
// fields as the base scope, parameters and unambiguous locals on top, and
// `this.<field>` entries.
func javaMethodReceiverTypes(method *tsx.Node, fields map[string]string) map[string]string {
	types := map[string]string{}
	ambiguous := map[string]bool{}

	bind := func(name, typ string) {
		if name == "" || typ == "" || ambiguous[name] {
			return
		}

		if prev, ok := types[name]; ok && prev != typ {
			delete(types, name)
			ambiguous[name] = true
		} else {
			types[name] = typ
		}
	}

	conflict := func(name, typ string) bool {
		f, ok := fields[name]

		return ok && f != typ
	}

	if ps := method.Field("parameters"); ps != nil {
		for _, p := range ps.Children() {
			if p.Type() != "formal_parameter" && p.Type() != "spread_parameter" {
				continue
			}

			if nn := p.Field("name"); nn != nil {
				bind(nn.Text(), javaReceiverTypeName(p.Field("type")))
			}
		}
	}

	var stack []*tsx.Node
	if bd := method.Field("body"); bd != nil {
		stack = append(stack, bd.Children()...)
	}

	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if javaNestedTypeDecls[n.Type()] {
			continue
		}

		switch n.Type() {
		case "lambda_expression":
			for _, b := range javaLambdaParams(n) {
				if b.typ == "" || conflict(b.name, b.typ) {
					delete(types, b.name)
					ambiguous[b.name] = true
				} else {
					bind(b.name, b.typ)
				}
			}
		case "local_variable_declaration":
			typ := javaReceiverTypeName(n.Field("type"))
			for _, name := range javaDeclaratorNames(n) {
				if conflict(name, typ) {
					delete(types, name)
					ambiguous[name] = true
				} else {
					bind(name, typ)
				}
			}
		}

		stack = append(stack, n.Children()...)
	}

	table := make(map[string]string, len(fields)*2+len(types))
	for k, v := range fields {
		table[k] = v
	}

	for k, v := range types {
		table[k] = v
	}

	for k := range ambiguous {
		delete(table, k)
	}

	for k, v := range fields {
		table["this."+k] = v
	}

	return table
}

func javaFieldTypes(x *generic.Ctx) map[string]map[string]string {
	t, _ := x.Data["java_field_types"].(map[string]map[string]string)
	if t == nil {
		t = map[string]map[string]string{}
		x.Data["java_field_types"] = t
	}

	return t
}

func javaRecordFields(x *generic.Ctx, classID string, n *tsx.Node) {
	typ := javaReceiverTypeName(n.Field("type"))
	if typ == "" {
		return
	}

	t := javaFieldTypes(x)
	if t[classID] == nil {
		t[classID] = map[string]string{}
	}

	for _, name := range javaDeclaratorNames(n) {
		t[classID][name] = typ
	}
}

func javaRecordMethodScope(x *generic.Ctx, n *tsx.Node, funcID string) {
	classID := x.ParentOf(funcID)
	bd := n.Field("body")

	if classID == "" || bd == nil {
		return
	}

	scopes, _ := x.Data["java_method_scopes"].(map[uint32]javaMethodScope)
	if scopes == nil {
		scopes = map[uint32]javaMethodScope{}
		x.Data["java_method_scopes"] = scopes
	}

	scopes[bd.StartByte()] = javaMethodScope{method: n, classID: classID}
}

func javaReceiverTables(x *generic.Ctx) map[uint32]map[string]string {
	if t, ok := x.Data["java_receiver_tables"].(map[uint32]map[string]string); ok {
		return t
	}

	bases := map[string][]string{}

	for _, e := range x.B.Edges {
		if e.Relation == "inherits" {
			bases[e.Source] = append(bases[e.Source], e.Target)
		}
	}

	fields := javaFieldTypes(x)
	scopes, _ := x.Data["java_method_scopes"].(map[uint32]javaMethodScope)
	out := make(map[uint32]map[string]string, len(scopes))

	for k, s := range scopes {
		out[k] = javaMethodReceiverTypes(s.method, csFieldsUpChain(fields, bases, s.classID))
	}

	x.Data["java_receiver_tables"] = out

	return out
}

func javaEnclosingBody(n *tsx.Node) *tsx.Node {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Type() == "method_declaration" || p.Type() == "constructor_declaration" {
			return p.Field("body")
		}
	}

	return nil
}

func javaDecorateRawCall(x *generic.Ctx, n *tsx.Node, rc *model.RawCall) {
	if !rc.IsMemberCall || rc.Receiver == "" {
		return
	}

	bd := javaEnclosingBody(n)
	if bd == nil {
		return
	}

	if t := javaReceiverTables(x)[bd.StartByte()]; t != nil {
		rc.ReceiverType = t[rc.Receiver]
	}
}

// javaPostProcess exports field tables keyed by class label.
func javaPostProcess(x *generic.Ctx, res *model.Extraction) {
	fields := javaFieldTypes(x)
	if len(fields) == 0 {
		return
	}

	out := map[string]map[string]string{}

	for cid, tbl := range fields {
		if len(tbl) == 0 {
			continue
		}

		n := x.B.Get(cid)
		if n == nil || n.Label == "" {
			continue
		}

		m := out[n.Label]
		if m == nil {
			m = map[string]string{}
			out[n.Label] = m
		}

		for k, v := range tbl {
			m[k] = v
		}
	}

	if len(out) > 0 {
		res.FieldTables = out
	}
}

// resolveJavaMemberCalls binds Java member calls to the receiver's declared
// type: `this` and explicit type receivers exactly, fields/params/locals
// (including fields inherited across files for `this.f`) as INFERRED.
func resolveJavaMemberCalls(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	var calls []*model.RawCall

	for _, fr := range per {
		if fr.Ex == nil {
			continue
		}

		for _, rc := range fr.Ex.RawCalls {
			if rc.Language == "java" && rc.IsMemberCall {
				calls = append(calls, rc)
			}
		}
	}

	if len(calls) == 0 {
		return
	}

	nodes, edges := *nodesP, *edgesP

	key := func(l string) string {
		return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(l), "."), "()")
	}

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
			typeDefs[key(n.Label)] = append(typeDefs[key(n.Label)], n.ID)
		}
	}

	methods := map[[2]string]map[string]bool{}
	enclosing := map[string]string{}
	bases := map[string][]string{}

	for _, e := range edges {
		switch e.Relation {
		case "method":
			m := byID[e.Target]
			if m == nil {
				continue
			}

			if _, ok := enclosing[e.Target]; !ok {
				enclosing[e.Target] = e.Source
			}

			k := [2]string{e.Source, key(m.Label)}
			if methods[k] == nil {
				methods[k] = map[string]bool{}
			}

			methods[k][e.Target] = true
		case "inherits":
			bases[e.Source] = append(bases[e.Source], e.Target)
		}
	}

	classFields := bindFieldTables(nodes, per)

	// Adapted from Graphify's _resolve_java_member_calls (Apache-2.0).
	jvmBases := func(typeID string) ([]string, bool) {
		parents := bases[typeID]
		for _, parent := range parents {
			n := byID[parent]
			if parent == typeID || n == nil || base.LangFamily(n.SourceFile) != "jvm" {
				return nil, false
			}
		}
		return parents, true
	}
	methodOnTypeOrBases := func(typeID, callee string) string {
		hits, seen := map[string]bool{}, map[string]bool{}
		frontier := []string{typeID}
		for len(frontier) > 0 {
			n := frontier[len(frontier)-1]
			frontier = frontier[:len(frontier)-1]
			if seen[n] {
				continue
			}
			seen[n] = true
			if declared := methods[[2]string{n, callee}]; len(declared) > 0 {
				for id := range declared {
					hits[id] = true
				}
				continue
			}
			parents, ok := jvmBases(n)
			if !ok {
				return ""
			}
			frontier = append(frontier, parents...)
		}
		if len(hits) == 1 {
			for id := range hits {
				return id
			}
		}
		return ""
	}

	inheritedField := func(classID, field string) string {
		seen := map[string]bool{}
		queue := []string{classID}

		for len(queue) > 0 {
			c := queue[0]
			queue = queue[1:]

			if c == "" || seen[c] {
				continue
			}

			seen[c] = true

			if t := classFields[c][field]; t != "" {
				return t
			}

			queue = append(queue, bases[c]...)
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

		if recv == "this" {
			if typeID = enclosing[caller]; typeID == "" {
				continue
			}

			exact = true
		} else if recv == "super" {
			parents, ok := jvmBases(enclosing[caller])
			if !ok || len(parents) != 1 {
				continue
			}
			typeID, exact = parents[0], true
		} else {
			typeName := rc.ReceiverType
			if typeName == "" && upperStart(recv) {
				typeName, exact = recv, true
			}

			if typeName == "" && strings.HasPrefix(recv, "this.") {
				typeName = inheritedField(enclosing[caller], strings.TrimPrefix(recv, "this."))
			}

			if typeName == "" {
				continue
			}

			defs := typeDefs[key(typeName)]
			if len(defs) == 0 {
				parkUnresolvedMemberCall(byID[caller], callee, typeName, "java", rc)

				continue
			}

			if len(defs) != 1 {
				continue
			}

			typeID = defs[0]
		}

		target := methodOnTypeOrBases(typeID, key(callee))
		if target == "" || target == caller || existing[[2]string{caller, target}] {
			continue
		}

		existing[[2]string{caller, target}] = true

		conf, score := model.Inferred, 0.8
		if exact {
			conf, score = model.Extracted, 1.0
		}

		edges = append(edges, &model.Edge{
			Source: caller, Target: target, Relation: "calls", Context: "call",
			Confidence: conf, ConfidenceScore: model.Score(score),
			SourceFile: rc.SourceFile, SourceLocation: rc.SourceLocation, Weight: 1,
		})
	}

	*nodesP, *edgesP = nodes, edges
}

// bindFieldTables binds per-file field tables (keyed by class label) to the
// one class node carrying that label, preferring the same source file; an
// ambiguous label binds nothing and conflicting fields are dropped.
//
// Adapted from Graphify's _bind_member_field_tables (Apache-2.0).
func bindFieldTables(nodes []*model.Node, per []extract.FileResult) map[string]map[string]string {
	byLabel := map[string][]*model.Node{}

	for _, n := range nodes {
		if n.Label != "" && n.SourceFile != "" {
			byLabel[n.Label] = append(byLabel[n.Label], n)
		}
	}

	bound := map[string]map[string]string{}

	for _, fr := range per {
		if fr.Ex == nil {
			continue
		}

		for label, fields := range fr.Ex.FieldTables {
			cands := byLabel[label]
			if len(cands) > 1 {
				var exact []*model.Node

				for _, n := range cands {
					if n.SourceFile == fr.Path {
						exact = append(exact, n)
					}
				}

				if len(exact) != 1 {
					exact = nil

					for _, n := range cands {
						if baseName(n.SourceFile) == baseName(fr.Path) {
							exact = append(exact, n)
						}
					}
				}

				cands = exact
			}

			if len(cands) != 1 {
				continue
			}

			m := bound[cands[0].ID]
			if m == nil {
				m = map[string]string{}
				bound[cands[0].ID] = m
			}

			for f, t := range fields {
				if prev, ok := m[f]; ok && prev != t {
					delete(m, f)
				} else {
					m[f] = t
				}
			}
		}
	}

	return bound
}
