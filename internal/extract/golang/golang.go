// Package golang extracts Go source files.
//
// Adapted from Graphify's graphify/extractors/go.py (Apache-2.0).
package golang

import (
	"crypto/sha1" //nolint:gosec // non-cryptographic salt, mirrors Graphify
	"encoding/hex"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

var predeclaredTypes = base.NewSet(
	"bool", "byte", "complex64", "complex128", "error", "float32", "float64",
	"int", "int8", "int16", "int32", "int64", "rune", "string",
	"uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "any", "comparable",
)

// PredeclaredFuncs are Go builtin functions filtered for bare-identifier calls.
var PredeclaredFuncs = base.NewSet(
	"append", "cap", "clear", "close", "complex", "copy", "delete", "imag",
	"len", "make", "max", "min", "new", "panic", "print", "println", "real",
	"recover",
)

type typeRef struct {
	name string
	role string
}

func collectTypeRefs(n *tsx.Node, generic bool, out *[]typeRef) {
	if n == nil {
		return
	}

	role := "type"
	if generic {
		role = "generic_arg"
	}

	switch n.Type() {
	case "type_identifier":
		if t := n.Text(); t != "" && !predeclaredTypes.Has(t) {
			*out = append(*out, typeRef{t, role})
		}

		return
	case "qualified_type":
		if t := n.Text(); t != "" {
			*out = append(*out, typeRef{t, role})
		}

		return
	case "generic_type":
		if tf := n.Field("type"); tf != nil {
			collectTypeRefs(tf, generic, out)
		}

		for _, c := range n.Children() {
			if c.Type() == "type_arguments" {
				for _, a := range c.Children() {
					if a.IsNamed() {
						collectTypeRefs(a, true, out)
					}
				}
			}
		}

		return
	case "pointer_type", "slice_type", "array_type", "map_type", "channel_type", "parenthesized_type":
		for _, c := range n.NamedChildren() {
			collectTypeRefs(c, generic, out)
		}

		return
	}

	if n.IsNamed() {
		for _, c := range n.NamedChildren() {
			collectTypeRefs(c, generic, out)
		}
	}
}

// Extract parses a Go file.
func Extract(path, _ string, src []byte) *model.Extraction {
	tree, err := tsx.Parse("go", src)
	if err != nil {
		return &model.Extraction{Error: err.Error()}
	}
	defer tree.Release()

	return extract(path, tree)
}

type fnBody struct {
	id   string
	body *tsx.Node
}

func extract(path string, tree *tsx.Tree) *model.Extraction {
	root := tree.Root
	b := base.NewBuilder(path)

	pkgScope := filepath.Base(filepath.Dir(path))
	if pkgScope == "." || pkgScope == "/" || pkgScope == "" {
		pkgScope = b.Stem
	}

	var bodies []fnBody

	imported := map[string]string{}

	b.AddFileNode()

	ensureNamed := func(name string) string { return b.EnsureNamed(pkgScope, name) }

	emitMethodRefs := func(fn *tsx.Node, fnID string, line int) {
		emit := func(refs []typeRef, def string) {
			for _, r := range refs {
				ctx := def
				if r.role == "generic_arg" {
					ctx = "generic_arg"
				}

				tgt := ensureNamed(r.name)
				if tgt != fnID {
					b.AddEdgeCtx(fnID, tgt, "references", line, ctx)
				}
			}
		}

		if params := fn.Field("parameters"); params != nil {
			for _, p := range params.Children() {
				if p.Type() != "parameter_declaration" {
					continue
				}

				var refs []typeRef
				collectTypeRefs(p.Field("type"), false, &refs)
				emit(refs, "parameter_type")
			}
		}

		result := fn.Field("result")
		if result == nil {
			return
		}

		if result.Type() == "parameter_list" {
			for _, p := range result.Children() {
				if p.Type() != "parameter_declaration" {
					continue
				}

				tn := p.Field("type")
				if tn == nil {
					if nc := p.NamedChildren(); len(nc) > 0 {
						tn = nc[0]
					}
				}

				var refs []typeRef
				collectTypeRefs(tn, false, &refs)
				emit(refs, "return_type")
			}

			return
		}

		var refs []typeRef
		collectTypeRefs(result, false, &refs)
		emit(refs, "return_type")
	}

	receiverType := func(n *tsx.Node) string {
		recv := n.Field("receiver")
		if recv == nil {
			return ""
		}

		for _, p := range recv.Children() {
			if p.Type() == "parameter_declaration" {
				if tn := p.Field("type"); tn != nil {
					return strings.TrimSpace(strings.TrimLeft(tn.Text(), "*"))
				}

				break
			}
		}

		return ""
	}

	// Case-only collisions (Run vs run) would share a casefolded id; salt the
	// non-canonical member so both survive.
	caseGroups := map[string]map[string]struct{}{}
	addCase := func(plain, name string) {
		g := caseGroups[plain]
		if g == nil {
			g = map[string]struct{}{}
			caseGroups[plain] = g
		}

		g[name] = struct{}{}
	}

	var scan func(n *tsx.Node)
	scan = func(n *tsx.Node) {
		switch n.Type() {
		case "type_spec":
			if nameNode := n.Field("name"); nameNode != nil {
				owner := ids.MakeID(pkgScope, nameNode.Text())
				for _, body := range n.Children() {
					if body.Type() != "interface_type" {
						continue
					}

					for _, elem := range body.Children() {
						if elem.Type() != "method_elem" {
							continue
						}

						if mn := elem.Field("name"); mn != nil {
							addCase(ids.MakeID(owner, mn.Text()), mn.Text())
						}
					}
				}
			}
		case "function_declaration", "method_declaration":
			if nameNode := n.Field("name"); nameNode != nil {
				name := nameNode.Text()
				baseID := b.Stem

				if n.Type() == "method_declaration" {
					if rt := receiverType(n); rt != "" {
						baseID = ids.MakeID(pkgScope, rt)
					}
				}

				addCase(ids.MakeID(baseID, name), name)
			}

			return
		}

		for _, c := range n.Children() {
			scan(c)
		}
	}

	symbolID := func(plain, name string) string {
		names := caseGroups[plain]
		if len(names) < 2 {
			return plain
		}

		var exported []string

		for n := range names {
			if r, _ := utf8.DecodeRuneInString(n); unicode.IsUpper(r) {
				exported = append(exported, n)
			}
		}

		if len(exported) == 1 && name == exported[0] {
			return plain
		}

		sum := sha1.Sum([]byte(name)) //nolint:gosec // id salt

		return ids.MakeID(plain, hex.EncodeToString(sum[:])[:6])
	}

	addImport := func(spec *tsx.Node) {
		pathNode := spec.Field("path")
		if pathNode == nil {
			return
		}

		raw := strings.Trim(pathNode.Text(), `"`)
		tgt := ids.MakeID("go", "pkg", raw)
		b.AddEdgeCtx(b.FileID, tgt, "imports_from", spec.Line(), "import")

		local := raw[strings.LastIndexByte(raw, '/')+1:]
		if alias := spec.Field("name"); alias != nil {
			local = alias.Text()
		}

		if local != "" && local != "_" && local != "." {
			imported[local] = raw
		}
	}

	var walk func(n *tsx.Node)
	walk = func(n *tsx.Node) {
		switch n.Type() {
		case "function_declaration":
			nameNode := n.Field("name")
			if nameNode == nil {
				return
			}

			name := nameNode.Text()
			line := n.Line()
			id := symbolID(ids.MakeID(b.Stem, name), name)
			b.AddNode(id, name+"()", line)
			b.AddEdge(b.FileID, id, "contains", line)
			emitMethodRefs(n, id, line)

			if body := n.Field("body"); body != nil {
				bodies = append(bodies, fnBody{id, body})
			}

			return
		case "method_declaration":
			rt := receiverType(n)

			nameNode := n.Field("name")
			if nameNode == nil {
				return
			}

			name := nameNode.Text()
			line := n.Line()

			var id string

			if rt != "" {
				parent := ids.MakeID(pkgScope, rt)
				b.AddNode(parent, rt, line)
				id = symbolID(ids.MakeID(parent, name), name)
				b.AddNode(id, "."+name+"()", line)
				b.AddEdge(parent, id, "method", line)
			} else {
				id = symbolID(ids.MakeID(b.Stem, name), name)
				b.AddNode(id, name+"()", line)
				b.AddEdge(b.FileID, id, "contains", line)
			}

			emitMethodRefs(n, id, line)

			if body := n.Field("body"); body != nil {
				bodies = append(bodies, fnBody{id, body})
			}

			return
		case "type_declaration":
			for _, spec := range n.Children() {
				if spec.Type() == "type_spec" {
					walkTypeSpec(b, spec, pkgScope, ensureNamed, symbolID, emitMethodRefs)
				}
			}

			return
		case "import_declaration":
			for _, c := range n.Children() {
				switch c.Type() {
				case "import_spec_list":
					for _, s := range c.Children() {
						if s.Type() == "import_spec" {
							addImport(s)
						}
					}
				case "import_spec":
					addImport(c)
				}
			}

			return
		}

		for _, c := range n.Children() {
			walk(c)
		}
	}

	scan(root)
	walk(root)

	labelToID := map[string]string{}
	for _, n := range b.Nodes {
		labelToID[strings.TrimLeft(strings.Trim(n.Label, "()"), ".")] = n.ID
	}

	seenPairs := map[[2]string]bool{}

	var walkCalls func(n *tsx.Node, caller string)
	walkCalls = func(n *tsx.Node, caller string) {
		t := n.Type()
		if t == "function_declaration" || t == "method_declaration" {
			return
		}

		if t == "type_conversion_expression" {
			// gotreesitter's Go grammar may parse `recv.method(func(){...})`
			// as a conversion to a qualified type; treat it as a call.
			if qt := n.Field("type"); qt != nil && qt.Type() == "qualified_type" {
				pkg, name := qt.Field("package"), qt.Field("name")
				if pkg != nil && name != nil {
					recvName, callee := pkg.Text(), name.Text()
					ip, isPkg := imported[recvName]

					if !base.BuiltinGlobals.Has(callee) {
						tgt := ""
						if !isPkg {
							tgt = labelToID[callee]
						}

						if tgt != "" && tgt != caller {
							pair := [2]string{caller, tgt}
							if !seenPairs[pair] {
								seenPairs[pair] = true
								b.AddEdgeCtx(caller, tgt, "calls", n.Line(), "call")
							}
						} else if tgt == "" {
							rc := &model.RawCall{
								CallerID: caller, Callee: callee, IsMemberCall: !isPkg, Language: "go",
								SourceFile: path, SourceLocation: base.Loc(n.Line()),
							}
							if isPkg {
								rc.Receiver, rc.ImportPath = recvName, ip
							}

							b.RawCalls = append(b.RawCalls, rc)
						}
					}
				}
			}
		}

		if t == "call_expression" {
			fn := n.Field("function")
			callee := ""
			isMember := false
			bare := false
			receiver := ""
			importPath := ""

			if fn != nil {
				switch fn.Type() {
				case "identifier":
					bare = true
					callee = fn.Text()
				case "selector_expression":
					field := fn.Field("field")
					operand := fn.Field("operand")

					recvName := ""
					if operand != nil {
						recvName = operand.Text()
					}

					ip, isPkg := imported[recvName]
					isMember = !isPkg

					if isPkg {
						receiver = recvName
						importPath = ip
					}

					if field != nil {
						callee = field.Text()
					}
				}
			}

			if bare && PredeclaredFuncs.Has(callee) {
				callee = ""
			}

			if callee != "" && !base.BuiltinGlobals.Has(callee) {
				tgt := ""
				if importPath == "" {
					tgt = labelToID[callee]
				}

				if tgt != "" && tgt != caller {
					pair := [2]string{caller, tgt}
					if !seenPairs[pair] {
						seenPairs[pair] = true
						b.AddEdgeCtx(caller, tgt, "calls", n.Line(), "call")
					}
				} else if tgt == "" {
					b.RawCalls = append(b.RawCalls, &model.RawCall{
						CallerID:       caller,
						Callee:         callee,
						IsMemberCall:   isMember,
						Language:       "go",
						Receiver:       receiver,
						ImportPath:     importPath,
						SourceFile:     path,
						SourceLocation: base.Loc(n.Line()),
					})
				}
			}
		}

		for _, c := range n.Children() {
			walkCalls(c, caller)
		}
	}

	for _, fb := range bodies {
		walkCalls(fb.body, fb.id)
	}

	res := b.Result()
	res.GoImports = imported

	return res
}

func walkTypeSpec(
	b *base.Builder,
	spec *tsx.Node,
	pkgScope string,
	ensureNamed func(string) string,
	symbolID func(string, string) string,
	emitMethodRefs func(*tsx.Node, string, int),
) {
	nameNode := spec.Field("name")
	if nameNode == nil {
		return
	}

	typeName := nameNode.Text()
	line := spec.Line()
	typeID := ids.MakeID(pkgScope, typeName)
	b.AddNode(typeID, typeName, line)
	b.AddEdge(b.FileID, typeID, "contains", line)

	body := spec.ChildOfType("struct_type", "interface_type")
	if body == nil {
		return
	}

	if body.Type() == "struct_type" {
		for _, fdl := range body.Children() {
			if fdl.Type() != "field_declaration_list" {
				continue
			}

			for _, field := range fdl.Children() {
				if field.Type() != "field_declaration" {
					continue
				}

				hasName := field.ChildOfType("field_identifier") != nil

				tn := field.Field("type")
				if tn == nil {
					for _, fc := range field.NamedChildren() {
						if fc.Type() != "field_identifier" {
							tn = fc

							break
						}
					}
				}

				var refs []typeRef
				collectTypeRefs(tn, false, &refs)

				fl := field.Line()
				for _, r := range refs {
					tgt := ensureNamed(r.name)
					if tgt == typeID {
						continue
					}

					if !hasName && r.role == "type" {
						b.AddEdge(typeID, tgt, "embeds", fl)
					} else {
						ctx := "field"
						if r.role == "generic_arg" {
							ctx = "generic_arg"
						}

						b.AddEdgeCtx(typeID, tgt, "references", fl, ctx)
					}
				}
			}
		}

		return
	}

	for _, elem := range body.Children() {
		switch elem.Type() {
		case "method_elem":
			mn := elem.Field("name")
			if mn == nil {
				mn = elem.ChildOfType("field_identifier")
			}

			if mn == nil {
				continue
			}

			name := mn.Text()
			ml := elem.Line()
			mid := symbolID(ids.MakeID(typeID, name), name)
			b.AddNode(mid, "."+name+"()", ml)
			b.AddEdge(typeID, mid, "method", ml)
			emitMethodRefs(elem, mid, ml)
		case "type_elem":
			isTypeSet := false

			for _, c := range elem.Children() {
				if c.Type() == "|" || c.Type() == "negated_type" {
					isTypeSet = true
				}
			}

			var refs []typeRef

			for _, sub := range elem.NamedChildren() {
				collectTypeRefs(sub, false, &refs)
			}

			el := elem.Line()
			for _, r := range refs {
				tgt := ensureNamed(r.name)
				if tgt == typeID {
					continue
				}

				switch {
				case isTypeSet:
					b.AddEdgeCtx(typeID, tgt, "references", el, "type_constraint")
				case r.role == "type":
					b.AddEdge(typeID, tgt, "embeds", el)
				default:
					b.AddEdgeCtx(typeID, tgt, "references", el, "generic_arg")
				}
			}
		}
	}
}
