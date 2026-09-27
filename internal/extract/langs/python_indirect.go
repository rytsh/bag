package langs

import (
	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
)

func pyDispatchIdents(coll *tsx.Node) []*tsx.Node {
	var out []*tsx.Node

	if coll.Type() == "dictionary" {
		for _, p := range coll.Children() {
			if p.Type() == "pair" {
				if v := p.Field("value"); v != nil && v.Type() == "identifier" {
					out = append(out, v)
				}
			}
		}

		return out
	}

	for _, el := range coll.Children() {
		if el.Type() == "identifier" {
			out = append(out, el)
		}
	}

	return out
}

func pyValueIdents(v *tsx.Node) []*tsx.Node {
	if v == nil {
		return nil
	}

	switch v.Type() {
	case "identifier":
		return []*tsx.Node{v}
	case "expression_list":
		var out []*tsx.Node

		for _, c := range v.Children() {
			if c.Type() == "identifier" {
				out = append(out, c)
			}
		}

		return out
	}

	return nil
}

func pyGetattrRef(call *tsx.Node) *generic.IndirectRef {
	fn := call.Field("function")
	if fn == nil || fn.Type() != "identifier" || fn.Text() != "getattr" {
		return nil
	}

	args := call.Field("arguments")
	if args == nil {
		return nil
	}

	var pos []*tsx.Node

	for _, c := range args.NamedChildren() {
		if c.Type() != "keyword_argument" && c.Type() != "comment" {
			pos = append(pos, c)
		}
	}

	if len(pos) < 2 || pos[1].Type() != "string" || pos[1].ChildOfType("interpolation") != nil {
		return nil
	}

	content := pos[1].ChildOfType("string_content")
	if content == nil {
		return nil
	}

	return &generic.IndirectRef{Ident: pos[1], Name: content.Text(), Context: "getattr", ByName: true}
}

func refsOf(nodes []*tsx.Node, ctx string) []generic.IndirectRef {
	var out []generic.IndirectRef

	for _, n := range nodes {
		out = append(out, generic.IndirectRef{Ident: n, Name: n.Text(), Context: ctx})
	}

	return out
}

func pyIndirectRefs(x *generic.Ctx, n *tsx.Node) []generic.IndirectRef {
	var out []generic.IndirectRef

	switch n.Type() {
	case "call":
		if args := n.Field("arguments"); args != nil {
			for _, a := range args.Children() {
				switch a.Type() {
				case "identifier":
					out = append(out, generic.IndirectRef{Ident: a, Name: a.Text(), Context: "argument"})
				case "keyword_argument":
					if v := a.Field("value"); v != nil && v.Type() == "identifier" {
						out = append(out, generic.IndirectRef{Ident: v, Name: v.Text(), Context: "argument"})
					}
				}
			}
		}

		if r := pyGetattrRef(n); r != nil {
			out = append(out, *r)
		}
	case "dictionary", "list", "set", "tuple":
		out = append(out, refsOf(pyDispatchIdents(n), "collection")...)
	case "assignment":
		out = append(out, refsOf(pyValueIdents(n.Field("right")), "assignment")...)
	case "return_statement":
		if nc := n.NamedChildren(); len(nc) > 0 {
			out = append(out, refsOf(pyValueIdents(nc[0]), "return")...)
		}
	}

	_ = x

	return out
}

func pyModuleBound(root *tsx.Node) map[string]bool {
	out := map[string]bool{}

	var walk func(n *tsx.Node)
	walk = func(n *tsx.Node) {
		for _, c := range n.Children() {
			switch c.Type() {
			case "function_definition", "class_definition", "lambda":
				continue
			case "assignment":
				pyAssignTargets(c.Field("left"), out)
			case "for_statement", "for_in_clause":
				pyAssignTargets(c.Field("left"), out)
			case "named_expression":
				pyAssignTargets(c.Field("name"), out)
			}

			walk(c)
		}
	}

	walk(root)

	return out
}

func pyModuleIndirect(x *generic.Ctx) {
	bound := pyModuleBound(x.Tree.Root)
	file := x.B.FileID

	var scan func(n *tsx.Node)
	scan = func(n *tsx.Node) {
		switch n.Type() {
		case "function_definition", "class_definition":
			return
		case "dictionary", "list", "set", "tuple":
			for _, r := range refsOf(pyDispatchIdents(n), "collection") {
				x.EmitIndirect(r, file, bound)
			}
		case "assignment":
			for _, r := range refsOf(pyValueIdents(n.Field("right")), "assignment") {
				x.EmitIndirect(r, file, bound)
			}
		case "call":
			if r := pyGetattrRef(n); r != nil {
				x.EmitIndirect(*r, file, bound)
			}
		}

		for _, c := range n.Children() {
			scan(c)
		}
	}

	scan(x.Tree.Root)
}
