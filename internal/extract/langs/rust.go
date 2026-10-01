package langs

import (
	"strconv"
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// Adapted from Graphify's graphify/extractors/rust.py (Apache-2.0).

var rustTraitMethodBlocklist = base.NewSet(
	"new", "default", "parse", "from_str", "now", "clone", "into", "from",
	"to_string", "to_owned", "len", "is_empty", "iter", "next", "build",
	"start", "run", "init", "app", "get", "set", "push", "pop", "insert",
	"remove", "contains", "collect", "map", "filter", "unwrap", "expect",
	"ok", "err", "some", "none", "send", "recv", "lock", "read", "write",
)

var rustTypeNodes = base.NewSet("type_identifier", "generic_type", "scoped_type_identifier", "reference_type",
	"primitive_type", "tuple_type", "array_type")

func rustTypeRefs(n *tsx.Node, generic bool, out *[]typeRef) {
	if n == nil {
		return
	}

	switch n.Type() {
	case "primitive_type":
		return
	case "type_identifier":
		if n.Text() != "" {
			*out = append(*out, typeRef{n.Text(), roleOf(generic)})
		}

		return
	case "scoped_type_identifier":
		if t := lastSeg(n.Text(), "::"); t != "" {
			*out = append(*out, typeRef{t, roleOf(generic)})
		}

		return
	case "generic_type":
		nn := n.Field("type")
		if nn == nil {
			nn = n.ChildOfType("type_identifier", "scoped_type_identifier")
		}

		if nn != nil {
			if t := lastSeg(nn.Text(), "::"); t != "" {
				*out = append(*out, typeRef{t, roleOf(generic)})
			}
		}

		for _, c := range n.Children() {
			if c.Type() == "type_arguments" {
				for _, a := range c.NamedChildren() {
					rustTypeRefs(a, true, out)
				}
			}
		}

		return
	}

	for _, c := range n.NamedChildren() {
		rustTypeRefs(c, generic, out)
	}
}

type rustBody struct {
	id       string
	node     *tsx.Node
	selfType string
	implKey  string
}

// rustSimpleGenericImplKey returns "Owner/arity" for an inherent
// `impl<T, U> Owner<T, U>` with plain parameters in declaration order.
//
// Adapted from Graphify's _rust_simple_generic_impl_key (Apache-2.0).
func rustSimpleGenericImplKey(n *tsx.Node) string {
	if n.Field("trait") != nil {
		return ""
	}

	params, owner := n.Field("type_parameters"), n.Field("type")
	if params == nil || owner == nil || owner.Type() != "generic_type" {
		return ""
	}

	for _, c := range n.NamedChildren() {
		if c.Type() == "where_clause" {
			return ""
		}
	}

	var names []string

	seen := map[string]bool{}

	for _, p := range params.NamedChildren() {
		if p.Type() != "type_parameter" {
			return ""
		}

		nc := p.NamedChildren()
		if len(nc) != 1 || nc[0].Type() != "type_identifier" {
			return ""
		}

		name := nc[0].Text()
		if name == "" || seen[name] {
			return ""
		}

		seen[name] = true
		names = append(names, name)
	}

	if len(names) == 0 {
		return ""
	}

	ot := owner.Field("type")
	if ot == nil || ot.Type() != "type_identifier" {
		return ""
	}

	args := owner.ChildOfType("type_arguments")
	if args == nil {
		return ""
	}

	an := args.NamedChildren()
	if len(an) != len(names) {
		return ""
	}

	for i, a := range an {
		if a.Type() != "type_identifier" || a.Text() != names[i] {
			return ""
		}
	}

	if ot.Text() == "" {
		return ""
	}

	return ot.Text() + "/" + strconv.Itoa(len(names))
}

// ExtractRust extracts a Rust file.
func ExtractRust(path, _ string, src []byte) *model.Extraction {
	tree, err := tsx.Parse("rust", src)
	if err != nil {
		return &model.Extraction{Error: err.Error()}
	}
	defer tree.Release()

	b := base.NewBuilder(path)
	b.AddFileNode()

	ensure := func(name string) string { return b.EnsureNamed(b.Stem, name) }

	var bodies []rustBody

	// The enclosing impl block's bare self type and generic key while its
	// body is walked.
	var curSelfType, curImplKey string

	implKeys := map[string]*string{}

	emitRefs := func(fn *tsx.Node, fid string, line int) {
		if params := fn.Field("parameters"); params != nil {
			for _, p := range params.Children() {
				if p.Type() != "parameter" {
					continue
				}

				var refs []typeRef
				rustTypeRefs(p.Field("type"), false, &refs)

				for _, r := range refs {
					ctx := "parameter_type"
					if r.role == "generic_arg" {
						ctx = "generic_arg"
					}

					if t := ensure(r.name); t != fid {
						b.AddEdgeCtx(fid, t, "references", line, ctx)
					}
				}
			}
		}

		if rt := fn.Field("return_type"); rt != nil {
			var refs []typeRef
			rustTypeRefs(rt, false, &refs)

			for _, r := range refs {
				ctx := "return_type"
				if r.role == "generic_arg" {
					ctx = "generic_arg"
				}

				if t := ensure(r.name); t != fid {
					b.AddEdgeCtx(fid, t, "references", line, ctx)
				}
			}
		}
	}

	refField := func(owner string, tn *tsx.Node, line int) {
		var refs []typeRef
		rustTypeRefs(tn, false, &refs)

		for _, r := range refs {
			ctx := "field"
			if r.role == "generic_arg" {
				ctx = "generic_arg"
			}

			if t := ensure(r.name); t != owner {
				b.AddEdgeCtx(owner, t, "references", line, ctx)
			}
		}
	}

	var walk func(n *tsx.Node, impl string)
	walk = func(n *tsx.Node, impl string) {
		t := n.Type()

		switch t {
		case "function_item", "function_signature_item":
			nn := n.Field("name")
			if nn == nil {
				return
			}

			name, line := nn.Text(), n.Line()

			var fid string

			if impl != "" {
				fid = ids.MakeID(impl, name)
				b.AddNode(fid, "."+name+"()", line)
				b.AddEdge(impl, fid, "method", line)
			} else {
				fid = ids.MakeID(b.Stem, name)
				b.AddNode(fid, name+"()", line)
				b.AddEdge(b.FileID, fid, "contains", line)
			}

			emitRefs(n, fid, line)

			if t == "function_item" {
				if bd := n.Field("body"); bd != nil {
					st, ik := "", ""
					if impl != "" {
						st, ik = curSelfType, curImplKey
					}

					bodies = append(bodies, rustBody{id: fid, node: bd, selfType: st, implKey: ik})
				}
			}

			return
		case "struct_item", "enum_item", "trait_item":
			nn := n.Field("name")
			if nn == nil {
				return
			}

			line := n.Line()
			item := ids.MakeID(b.Stem, nn.Text())
			b.AddNode(item, nn.Text(), line).RustDeclCount++
			b.AddEdge(b.FileID, item, "contains", line)

			switch t {
			case "trait_item":
				for _, c := range n.Children() {
					if c.Type() != "trait_bounds" {
						continue
					}

					for _, s := range c.NamedChildren() {
						var refs []typeRef
						rustTypeRefs(s, false, &refs)

						for i, r := range refs {
							tg := ensure(r.name)
							if tg == item {
								continue
							}

							if i == 0 {
								b.AddEdge(item, tg, "inherits", line)
							} else {
								b.AddEdgeCtx(item, tg, "references", line, "generic_arg")
							}
						}
					}
				}
			case "struct_item":
				for _, c := range n.Children() {
					switch c.Type() {
					case "field_declaration_list":
						for _, f := range c.Children() {
							if f.Type() != "field_declaration" {
								continue
							}

							tn := f.Field("type")
							if tn == nil {
								tn = f.ChildOfType("type_identifier", "generic_type", "scoped_type_identifier",
									"reference_type", "primitive_type")
							}

							refField(item, tn, f.Line())
						}
					case "ordered_field_declaration_list":
						for _, tc := range c.Children() {
							if rustTypeNodes.Has(tc.Type()) {
								refField(item, tc, c.Line())
							}
						}
					}
				}
			case "enum_item":
				for _, c := range n.Children() {
					if c.Type() != "enum_variant_list" {
						continue
					}

					for _, v := range c.Children() {
						if v.Type() != "enum_variant" {
							continue
						}
						// Adapted from Graphify's extract_rust (Apache-2.0).
						if nn := v.ChildOfType("identifier"); nn != nil && nn.Text() != "" {
							variant := ids.MakeID(item, nn.Text())
							b.AddNode(variant, nn.Text(), v.Line())
							b.AddEdge(item, variant, "case_of", v.Line())
						}

						for _, vc := range v.Children() {
							switch vc.Type() {
							case "ordered_field_declaration_list":
								for _, tc := range vc.Children() {
									if rustTypeNodes.Has(tc.Type()) {
										refField(item, tc, v.Line())
									}
								}
							case "field_declaration_list":
								for _, f := range vc.Children() {
									if f.Type() == "field_declaration" {
										refField(item, f.Field("type"), f.Line())
									}
								}
							}
						}
					}
				}
			}

			if t == "trait_item" {
				if bd := n.Field("body"); bd != nil {
					for _, c := range bd.Children() {
						walk(c, item)
					}
				}
			}

			return
		case "static_item", "const_item":
			nn := n.Field("name")
			if nn == nil {
				return
			}

			line := n.Line()

			var item string

			if impl != "" {
				item = ids.MakeID(impl, nn.Text())
				b.AddNode(item, "."+nn.Text(), line)
				b.AddEdge(impl, item, "contains", line)
			} else {
				item = ids.MakeID(b.Stem, nn.Text())
				b.AddNode(item, nn.Text(), line)
				b.AddEdge(b.FileID, item, "contains", line)
			}

			if tn := n.Field("type"); tn != nil {
				refField(item, tn, line)
			}

			return
		case "impl_item":
			tn, trait := n.Field("type"), n.Field("trait")
			implID, selfType, implKey := "", "", ""

			if tn != nil {
				typeName := strings.TrimSpace(tn.Text())
				implID = ids.MakeID(b.Stem, typeName)
				b.AddNode(implID, typeName, n.Line())
				implKey = rustSimpleGenericImplKey(n)
				selfType = strings.TrimSpace(strings.SplitN(typeName, "<", 2)[0])
			}

			if trait != nil && implID != "" {
				var refs []typeRef
				rustTypeRefs(trait, false, &refs)

				for i, r := range refs {
					tg := ensure(r.name)
					if tg == implID {
						continue
					}

					if i == 0 {
						b.AddEdge(implID, tg, "implements", n.Line())
					} else {
						b.AddEdgeCtx(implID, tg, "references", n.Line(), "generic_arg")
					}
				}
			}

			if bd := n.Field("body"); bd != nil {
				hasMethods := false

				for _, c := range bd.Children() {
					if c.Type() == "function_item" || c.Type() == "function_signature_item" {
						hasMethods = true

						break
					}
				}

				if implID != "" && hasMethods {
					k := implKey
					if prev, ok := implKeys[implID]; !ok {
						implKeys[implID] = &k
					} else if *prev != implKey {
						empty := ""
						implKeys[implID] = &empty
					}

					if node := b.Get(implID); node != nil {
						node.RustImplKey = *implKeys[implID]
					}
				}

				prevST, prevIK := curSelfType, curImplKey
				curSelfType, curImplKey = selfType, implKey

				for _, c := range bd.Children() {
					walk(c, implID)
				}

				curSelfType, curImplKey = prevST, prevIK
			}

			return
		case "use_declaration":
			if arg := n.Field("argument"); arg != nil {
				clean := strings.SplitN(arg.Text(), "{", 2)[0]
				clean = strings.TrimRight(strings.TrimRight(strings.TrimRight(clean, ":"), "*"), ":")

				if mod := strings.TrimSpace(lastSeg(clean, "::")); mod != "" {
					b.AddEdgeCtx(b.FileID, ids.MakeID(mod), "imports_from", n.Line(), "import")
				}
			}

			return
		}

		for _, c := range n.Children() {
			walk(c, "")
		}
	}

	walk(tree.Root, "")

	labelToID := map[string]string{}
	for _, n := range b.Nodes {
		labelToID[strings.TrimLeft(strings.Trim(n.Label, "()"), ".")] = n.ID
	}

	seen := map[[2]string]bool{}

	var wc func(n *tsx.Node, caller string, bd rustBody)
	wc = func(n *tsx.Node, caller string, bd rustBody) {
		if n.Type() == "function_item" {
			return
		}

		if n.Type() == "call_expression" {
			fn := n.Field("function")
			callee, member, scoped, selfCall := "", false, false, false

			if fn != nil {
				switch fn.Type() {
				case "identifier":
					callee = fn.Text()
				case "field_expression":
					member = true
					if f := fn.Field("field"); f != nil {
						callee = f.Text()
					}

					if v := fn.Field("value"); v != nil && v.Type() == "self" {
						selfCall = true
					}
				case "scoped_identifier":
					scoped = true
					if f := fn.Field("name"); f != nil {
						callee = f.Text()
					}
				}
			}

			if callee != "" && !base.BuiltinGlobals.Has(callee) {
				tgt := labelToID[callee]

				switch {
				case tgt != "" && tgt != caller:
					pair := [2]string{caller, tgt}
					if !seen[pair] {
						seen[pair] = true
						b.AddEdgeCtx(caller, tgt, "calls", n.Line(), "call")
					}
				case tgt == "" && !scoped && !rustTraitMethodBlocklist.Has(strings.ToLower(callee)):
					rc := &model.RawCall{
						CallerID: caller, Callee: callee, IsMemberCall: member, Language: "rust",
						SourceFile: path, SourceLocation: base.Loc(n.Line()),
					}
					if selfCall && bd.selfType != "" {
						rc.RustSelfType, rc.RustSelfImplKey = bd.selfType, bd.implKey
					}

					b.RawCalls = append(b.RawCalls, rc)
				}
			}
		}

		for _, c := range n.Children() {
			wc(c, caller, bd)
		}
	}

	for _, bd := range bodies {
		wc(bd.node, bd.id, bd)
	}

	res := b.Result()
	res.ParseErrors = tree.Root.HasError()

	return res
}
