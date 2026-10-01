package langs

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

var solidityTypes = map[string]string{"contract_declaration": "contract", "interface_declaration": "interface", "library_declaration": "library"}

type solidityCallKey struct {
	name  string
	arity int
}
type solidityBody struct {
	node   *tsx.Node
	caller string
}

func soliditySignature(n *tsx.Node) (int, string) {
	params := namedOfType(n, "parameter", "fallback_receive_parameter")
	var types []string
	for _, p := range params {
		if typ := p.Field("type"); typ != nil {
			types = append(types, typ.Text())
		}
	}
	return len(params), fmt.Sprintf("%d:%s", len(params), strings.Join(types, ","))
}

// ExtractSolidity is adapted from Graphify's extract_solidity (Apache-2.0).
// A dedicated walker preserves callable overload signatures and contract scope.
func ExtractSolidity(path, _ string, src []byte) *model.Extraction {
	tree, err := tsx.Parse("solidity", src)
	if err != nil {
		return &model.Extraction{Error: err.Error()}
	}
	defer tree.Release()
	b := newStructuralBuilder(path, "solidity")
	b.node(b.FileID, filepath.Base(path), "file", tree.Root, true, false)
	stub := func(name string, n *tsx.Node) string {
		name = lastSeg(name, ".")
		return b.node(ids.MakeID("solidity", "type", name), name, "external_type", n, false, false)
	}
	for _, decl := range tree.Root.NamedChildren() {
		if decl.Type() != "import_directive" {
			continue
		}
		if source := decl.Field("source"); source != nil {
			raw := strings.Trim(source.Text(), "\"'")
			if raw == "" || strings.HasPrefix(raw, "http:") || strings.HasPrefix(raw, "https:") {
				continue
			}
			target := filepath.ToSlash(filepath.Join(filepath.Dir(path), raw))
			if e := b.edge(b.FileID, ids.MakeID(target), "imports_from", decl); e != nil {
				e.TargetFile = target
			}
		}
	}
	walkCalls := func(bodies []solidityBody, funcs map[solidityCallKey][]string, refs map[solidityCallKey]string, free bool) {
		for _, body := range bodies {
			walkStructural(body.node, func(n *tsx.Node) bool {
				if n.Type() != "call_expression" {
					return true
				}
				fn := n.Field("function")
				if fn == nil {
					return true
				}
				written := fn.Text()
				callee := lastSeg(written, ".")
				key := solidityCallKey{callee, len(namedOfType(n, "call_argument"))}
				var candidates []string
				if !strings.Contains(written, ".") || (!free && strings.HasPrefix(written, "this.")) {
					candidates = funcs[key]
				}
				if len(candidates) == 1 {
					b.edge(body.caller, candidates[0], "calls", n)
				} else if target := refs[key]; target != "" && !strings.Contains(written, ".") {
					b.edge(body.caller, target, "references", n)
				} else if callee != "" && strings.Contains(written, ".") {
					b.RawCalls = append(b.RawCalls, &model.RawCall{CallerID: body.caller, Callee: callee, IsMemberCall: true, Language: "solidity", SourceFile: path, SourceLocation: fmt.Sprintf("L%d", n.Line())})
				}
				return true
			})
		}
	}
	for _, decl := range tree.Root.NamedChildren() {
		kind := solidityTypes[decl.Type()]
		name, body := decl.Field("name"), decl.Field("body")
		if kind == "" || name == nil || body == nil {
			continue
		}
		typeID := b.node(ids.MakeID(b.Stem, kind, name.Text()), name.Text(), kind, decl, true, kind == "contract")
		b.edge(b.FileID, typeID, "contains", decl)
		for _, inherit := range namedOfType(decl, "inheritance_specifier") {
			if ancestor := inherit.Field("ancestor"); ancestor != nil {
				name := lastSeg(ancestor.Text(), ".")
				if e := b.edge(typeID, stub(name, inherit), "inherits", inherit); e != nil {
					e.Context = "solidity_type:" + name
				}
			}
		}
		funcs, mods, refs := map[solidityCallKey][]string{}, map[string]string{}, map[solidityCallKey]string{}
		var bodies []solidityBody
		type pendingModifier struct {
			caller, name string
			node         *tsx.Node
		}
		var pending []pendingModifier
		memberID := func(n *tsx.Node, name, label, kind, relation, sig string, callable bool) string {
			if sig == "" {
				sig = name
			}
			id := b.node(ids.MakeID(typeID, kind, name, sig), label, kind, n, true, callable)
			b.edge(typeID, id, relation, n)
			return id
		}
		for _, m := range body.NamedChildren() {
			kind := m.Type()
			if kind == "using_directive" {
				if alias := m.ChildOfType("type_alias", "identifier", "user_defined_type"); alias != nil {
					name := lastSeg(alias.Text(), ".")
					if e := b.edge(typeID, stub(name, m), "uses", m); e != nil {
						e.Context = "solidity_type:" + name
					}
				}
				continue
			}
			if kind == "struct_declaration" || kind == "enum_declaration" {
				name := m.Field("name")
				if name == nil {
					continue
				}
				k := "struct"
				if kind == "enum_declaration" {
					k = "enum"
				}
				id := memberID(m, name.Text(), name.Text(), k, "contains", "", false)
				if bd := m.Field("body"); bd != nil {
					for _, value := range bd.NamedChildren() {
						if k == "struct" && value.Type() == "struct_member" {
							if name := value.Field("name"); name != nil {
								fid := b.node(ids.MakeID(id, "field", name.Text()), name.Text(), "field", value, true, false)
								b.edge(id, fid, "contains", value)
							}
						} else if k == "enum" && value.Type() == "enum_value" {
							vid := b.node(ids.MakeID(id, value.Text()), value.Text(), "enum_value", value, true, false)
							b.edge(id, vid, "contains", value)
						}
					}
				}
				continue
			}
			simple := map[string]string{"event_definition": "event", "error_declaration": "error", "state_variable_declaration": "state_variable"}[kind]
			if simple != "" {
				if name := m.Field("name"); name != nil {
					id := memberID(m, name.Text(), name.Text(), simple, "contains", "", false)
					refs[solidityCallKey{name.Text(), len(namedOfType(m, "event_parameter", "parameter"))}] = id
				}
				continue
			}
			name, callableKind := "", "function"
			switch kind {
			case "constructor_definition":
				name, callableKind = "constructor", "constructor"
			case "fallback_receive_definition":
				name = "fallback"
				if strings.HasPrefix(strings.TrimSpace(m.Text()), "receive") {
					name = "receive"
				}
				callableKind = name
			case "function_definition", "modifier_definition":
				if nn := m.Field("name"); nn != nil {
					name = nn.Text()
				}
				if kind == "modifier_definition" {
					callableKind = "modifier"
				}
			default:
				continue
			}
			if name == "" {
				continue
			}
			arity, signature := soliditySignature(m)
			id := memberID(m, name, name+"()", callableKind, "method", signature, true)
			if callableKind == "modifier" {
				mods[name] = id
			} else {
				key := solidityCallKey{name, arity}
				funcs[key] = append(funcs[key], id)
			}
			if bd := m.Field("body"); bd != nil {
				bodies = append(bodies, solidityBody{bd, id})
			}
			if callableKind == "function" {
				for _, inv := range namedOfType(m, "modifier_invocation") {
					if nn := inv.ChildOfType("identifier"); nn != nil {
						pending = append(pending, pendingModifier{id, nn.Text(), inv})
					}
				}
			}
		}
		for _, p := range pending {
			if target := mods[p.name]; target != "" {
				b.edge(p.caller, target, "uses", p.node)
			}
		}
		walkCalls(bodies, funcs, refs, false)
	}
	funcs := map[solidityCallKey][]string{}
	var bodies []solidityBody
	for _, fn := range namedOfType(tree.Root, "function_definition") {
		name := fn.Field("name")
		if name == nil {
			continue
		}
		arity, sig := soliditySignature(fn)
		id := b.node(ids.MakeID(b.Stem, "function", name.Text(), sig), name.Text()+"()", "function", fn, true, true)
		b.edge(b.FileID, id, "contains", fn)
		key := solidityCallKey{name.Text(), arity}
		funcs[key] = append(funcs[key], id)
		if bd := fn.Field("body"); bd != nil {
			bodies = append(bodies, solidityBody{bd, id})
		}
	}
	walkCalls(bodies, funcs, nil, true)
	res := b.Result()
	res.ParseErrors = tree.Root.HasError()
	return res
}

// resolveSolidityTypeReferences is adapted from Graphify's
// resolve_solidity_type_references (Apache-2.0).
func resolveSolidityTypeReferences(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, _ []extract.FileResult) {
	byID, sourceByFile := map[string]*model.Node{}, map[string]string{}
	types := map[[2]string][]string{}
	for _, n := range *nodesP {
		byID[n.ID] = n
		if n.SourceFile != "" && n.Label == filepath.Base(n.SourceFile) {
			sourceByFile[n.ID] = n.SourceFile
		}
		kind, _ := n.Metadata["kind"].(string)
		if n.SourceFile != "" && (kind == "contract" || kind == "library" || kind == "interface") {
			key := [2]string{n.SourceFile, n.Label}
			types[key] = append(types[key], n.ID)
		}
	}
	imports := map[string]map[string]bool{}
	for _, e := range *edgesP {
		if e.Relation != "imports_from" {
			continue
		}
		source, target := sourceByFile[e.Source], sourceByFile[e.Target]
		if source != "" && target != "" {
			if imports[source] == nil {
				imports[source] = map[string]bool{}
			}
			imports[source][target] = true
		}
	}
	for _, e := range *edgesP {
		if !strings.HasPrefix(e.Context, "solidity_type:") {
			continue
		}
		n := byID[e.Source]
		if n == nil || n.SourceFile == "" {
			continue
		}
		name := strings.TrimPrefix(e.Context, "solidity_type:")
		candidates := append([]string(nil), types[[2]string{n.SourceFile, name}]...)
		for source := range imports[n.SourceFile] {
			candidates = append(candidates, types[[2]string{source, name}]...)
		}
		if len(candidates) == 1 {
			e.Target = candidates[0]
		}
	}
}
