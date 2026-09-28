package langs

import (
	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/model"
)

// C# receiver typing for member calls: class field/property/primary-ctor
// types plus method-scoped parameter/local bindings resolved by byte offset.
//
// Adapted from Graphify's _csharp_method_receiver_types /
// _csharp_scoped_receiver_type (Apache-2.0).

type csBinding struct {
	start, end uint32
	typ        string
}

type csScopedTable struct {
	bindings map[string][]csBinding
	base     map[string]string
}

func csReceiverTypeName(n *tsx.Node) string {
	name, _, _, ok := csReadTypeName(n)
	if !ok || !upperStart(name) {
		return ""
	}

	return name
}

func csFieldTypes(x *generic.Ctx) map[string]map[string]string {
	t, _ := x.Data["cs_field_types"].(map[string]map[string]string)
	if t == nil {
		t = map[string]map[string]string{}
		x.Data["cs_field_types"] = t
	}

	return t
}

func csRecordField(x *generic.Ctx, classID, name, typ string) {
	if classID == "" || name == "" || typ == "" {
		return
	}

	t := csFieldTypes(x)
	if t[classID] == nil {
		t[classID] = map[string]string{}
	}

	t[classID][name] = typ
}

type csMethodScope struct {
	method  *tsx.Node
	classID string
}

func csRecordMethodScope(x *generic.Ctx, n *tsx.Node, funcID string) {
	classID := x.ParentOf(funcID)
	if classID == "" {
		return
	}

	bd := n.Field("body")
	if bd == nil {
		return
	}

	scopes, _ := x.Data["cs_method_scopes"].(map[uint32]csMethodScope)
	if scopes == nil {
		scopes = map[uint32]csMethodScope{}
		x.Data["cs_method_scopes"] = scopes
	}

	scopes[bd.StartByte()] = csMethodScope{method: n, classID: classID}
}

func csMethodReceiverTypes(method *tsx.Node, fields map[string]string) *csScopedTable {
	bindings := map[string][]csBinding{}
	poisoned := map[string]bool{}

	bind := func(name, typ string, scope *tsx.Node) {
		if name == "" || scope == nil {
			return
		}

		if ft, ok := fields[name]; ok && ft != typ {
			poisoned[name] = true
		}

		bindings[name] = append(bindings[name], csBinding{scope.StartByte(), scope.EndByte(), typ})
	}

	bindParam := func(p, scope *tsx.Node) {
		if nn := p.Field("name"); nn != nil {
			bind(nn.Text(), csReceiverTypeName(p.Field("type")), scope)
		}
	}

	body := method.Field("body")
	paramScope := body
	if paramScope == nil {
		paramScope = method
	}

	if params := method.Field("parameters"); params != nil {
		for _, p := range params.Children() {
			if p.Type() == "parameter" {
				bindParam(p, paramScope)
			}
		}
	}

	type item struct{ n, scope *tsx.Node }

	var stack []item

	if body != nil {
		for _, c := range body.Children() {
			stack = append(stack, item{c, paramScope})
		}
	}

	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n, scope := it.n, it.scope

		switch n.Type() {
		case "class_declaration", "struct_declaration", "interface_declaration", "record_declaration", "enum_declaration":
			continue
		case "lambda_expression":
			if lp := n.Field("parameters"); lp != nil {
				if lp.Type() == "implicit_parameter" {
					bind(lp.Text(), "", n)
				} else {
					for _, p := range lp.Children() {
						switch p.Type() {
						case "parameter":
							bindParam(p, n)
						case "implicit_parameter":
							bind(p.Text(), "", n)
						}
					}
				}
			}
		case "local_function_statement":
			if lp := n.Field("parameters"); lp != nil {
				for _, p := range lp.Children() {
					if p.Type() == "parameter" {
						bindParam(p, n)
					}
				}
			}
		case "local_declaration_statement":
			if vd := n.ChildOfType("variable_declaration"); vd != nil {
				declared := csReceiverTypeName(vd.Field("type"))

				for _, d := range vd.Children() {
					if d.Type() != "variable_declarator" {
						continue
					}

					nn := d.Field("name")
					if nn == nil {
						nn = d.ChildOfType("identifier")
					}

					if nn == nil {
						continue
					}

					typ := declared
					if typ == "" {
						if oc := d.ChildOfType("object_creation_expression"); oc != nil {
							typ = csReceiverTypeName(oc.Field("type"))
						}
					}

					bind(nn.Text(), typ, scope)
				}
			}
		case "declaration_expression", "declaration_pattern":
			if nn := n.Field("name"); nn != nil && nn.Type() == "identifier" {
				bind(nn.Text(), csReceiverTypeName(n.Field("type")), scope)
			}
		}

		childScope := scope
		switch n.Type() {
		case "block", "lambda_expression", "local_function_statement":
			childScope = n
		}

		for _, c := range n.Children() {
			stack = append(stack, item{c, childScope})
		}
	}

	base := map[string]string{}

	for name, t := range fields {
		if !poisoned[name] {
			base[name] = t
		}
	}

	for name := range poisoned {
		delete(bindings, name)
	}

	return &csScopedTable{bindings: bindings, base: base}
}

func (t *csScopedTable) typeAt(name string, at uint32) string {
	if t == nil || name == "" {
		return ""
	}

	var cands []csBinding

	for _, b := range t.bindings[name] {
		if b.start <= at && at < b.end {
			cands = append(cands, b)
		}
	}

	if len(cands) == 0 {
		return t.base[name]
	}

	inner := cands[0].end - cands[0].start
	for _, b := range cands[1:] {
		if w := b.end - b.start; w < inner {
			inner = w
		}
	}

	var winner *csBinding

	for i := range cands {
		if cands[i].end-cands[i].start == inner {
			if winner != nil {
				return ""
			}

			winner = &cands[i]
		}
	}

	return winner.typ
}

// csFieldsUpChain folds a class's field table with its in-file ancestors',
// nearest declaration winning.
func csFieldsUpChain(tables map[string]map[string]string, bases map[string][]string, classID string) map[string]string {
	merged := map[string]string{}
	seen := map[string]bool{}
	queue := []string{classID}

	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]

		if seen[c] {
			continue
		}

		seen[c] = true

		for k, v := range tables[c] {
			if _, ok := merged[k]; !ok {
				merged[k] = v
			}
		}

		queue = append(queue, bases[c]...)
	}

	return merged
}

// csReceiverTables builds the per-method scoped tables once, lazily, after
// the structural walk (so every field and inherits edge is known).
func csReceiverTables(x *generic.Ctx) map[uint32]*csScopedTable {
	if t, ok := x.Data["cs_receiver_tables"].(map[uint32]*csScopedTable); ok {
		return t
	}

	bases := map[string][]string{}

	for _, e := range x.B.Edges {
		if e.Relation == "inherits" {
			bases[e.Source] = append(bases[e.Source], e.Target)
		}
	}

	fields := csFieldTypes(x)
	scopes, _ := x.Data["cs_method_scopes"].(map[uint32]csMethodScope)
	out := make(map[uint32]*csScopedTable, len(scopes))

	for k, s := range scopes {
		out[k] = csMethodReceiverTypes(s.method, csFieldsUpChain(fields, bases, s.classID))
	}

	x.Data["cs_receiver_tables"] = out

	return out
}

func csEnclosingMethodBody(n *tsx.Node) *tsx.Node {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Type() == "method_declaration" {
			return p.Field("body")
		}
	}

	return nil
}

func csDecorateRawCall(x *generic.Ctx, n *tsx.Node, rc *model.RawCall) {
	if n.Type() == "object_creation_expression" {
		if _, q, qual, ok := csReadTypeName(n.Field("type")); ok && q && qual != "" {
			rc.QualifiedPrefix = qual
		}

		return
	}

	if !rc.IsMemberCall || rc.Receiver == "" {
		return
	}

	bd := csEnclosingMethodBody(n)
	if bd == nil {
		return
	}

	if t := csReceiverTables(x)[bd.StartByte()]; t != nil {
		rc.ReceiverType = t.typeAt(rc.Receiver, n.StartByte())
	}
}
