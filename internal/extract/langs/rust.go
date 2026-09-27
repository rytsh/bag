package langs

import (
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
	id   string
	node *tsx.Node
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
					bodies = append(bodies, rustBody{fid, bd})
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
			b.AddNode(item, nn.Text(), line)
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
			implID := ""

			if tn != nil {
				typeName := strings.TrimSpace(tn.Text())
				implID = ids.MakeID(b.Stem, typeName)
				b.AddNode(implID, typeName, n.Line())
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
				for _, c := range bd.Children() {
					walk(c, implID)
				}
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

	var wc func(n *tsx.Node, caller string)
	wc = func(n *tsx.Node, caller string) {
		if n.Type() == "function_item" {
			return
		}

		if n.Type() == "call_expression" {
			fn := n.Field("function")
			callee, member, scoped := "", false, false

			if fn != nil {
				switch fn.Type() {
				case "identifier":
					callee = fn.Text()
				case "field_expression":
					member = true
					if f := fn.Field("field"); f != nil {
						callee = f.Text()
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
					b.RawCalls = append(b.RawCalls, &model.RawCall{
						CallerID: caller, Callee: callee, IsMemberCall: member, Language: "rust",
						SourceFile: path, SourceLocation: base.Loc(n.Line()),
					})
				}
			}
		}

		for _, c := range n.Children() {
			wc(c, caller)
		}
	}

	for _, bd := range bodies {
		wc(bd.node, bd.id)
	}

	res := b.Result()
	res.ParseErrors = tree.Root.HasError()

	return res
}
