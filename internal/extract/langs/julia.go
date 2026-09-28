package langs

import (
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// ExtractJulia extracts a Julia file.
//
// Adapted from Graphify's graphify/extractors/julia.py (Apache-2.0).
func ExtractJulia(path, _ string, src []byte) *model.Extraction {
	tree, err := tsx.Parse("julia", src)
	if err != nil {
		return &model.Extraction{Error: err.Error()}
	}
	defer tree.Release()

	b := base.NewBuilder(path)
	b.AddFileNode()

	ensure := func(name string) string { return b.EnsureNamed(b.Stem, name) }

	var bodies []rustBody

	typeHead := func(th *tsx.Node) (string, string) {
		if be := th.ChildOfType("binary_expression"); be != nil {
			var idents []*tsx.Node

			for _, c := range be.Children() {
				if c.Type() == "identifier" {
					idents = append(idents, c)
				}
			}

			if len(idents) == 0 {
				return "", ""
			}

			sup := ""
			if len(idents) >= 2 {
				sup = idents[len(idents)-1].Text()
			}

			return idents[0].Text(), sup
		}

		if id := th.ChildOfType("identifier"); id != nil {
			return id.Text(), ""
		}

		return "", ""
	}

	sigName := func(sig *tsx.Node) string {
		for _, c := range sig.Children() {
			if c.Type() == "call_expression" {
				if ch := c.Children(); len(ch) > 0 && ch[0].Type() == "identifier" {
					return ch[0].Text()
				}
			}
		}

		return ""
	}

	var wc func(n *tsx.Node, fid string)
	wc = func(n *tsx.Node, fid string) {
		if n == nil {
			return
		}

		t := n.Type()
		if t == "function_definition" || t == "short_function_definition" {
			return
		}

		if t == "call_expression" {
			if ch := n.Children(); len(ch) > 0 {
				callee := ch[0]

				switch {
				case callee.Type() == "identifier":
					b.AddEdgeCtx(fid, ids.MakeID(b.Stem, callee.Text()), "calls", n.Line(), "call")
				case callee.Type() == "field_expression" && len(callee.Children()) >= 3:
					cc := callee.Children()
					b.AddEdgeCtx(fid, ids.MakeID(b.Stem, cc[len(cc)-1].Text()), "calls", n.Line(), "call")
				}
			}
		}

		for _, c := range n.Children() {
			wc(c, fid)
		}
	}

	var walk func(n *tsx.Node, scope string)
	walk = func(n *tsx.Node, scope string) {
		line := n.Line()

		switch n.Type() {
		case "module_definition":
			if id := n.ChildOfType("identifier"); id != nil {
				mid := ids.MakeID(b.Stem, id.Text())
				b.AddNode(mid, id.Text(), line)
				b.AddEdge(b.FileID, mid, "defines", line)

				for _, c := range n.Children() {
					walk(c, mid)
				}
			}

			return
		case "struct_definition":
			th := n.ChildOfType("type_head")
			if th == nil {
				return
			}

			name, sup := typeHead(th)
			if name == "" {
				return
			}

			sid := ids.MakeID(b.Stem, name)
			b.AddNode(sid, name, line)
			b.AddEdge(scope, sid, "defines", line)

			if sup != "" {
				b.AddEdge(sid, ensure(sup), "inherits", line)
			}

			fields := n.Children()
			if blk := n.ChildOfType("block"); blk != nil {
				fields = append(fields, blk.Children()...)
			}

			for _, c := range fields {
				if c.Type() != "typed_expression" {
					continue
				}

				var tids []*tsx.Node

				for _, s := range c.Children() {
					if s.Type() == "identifier" {
						tids = append(tids, s)
					}
				}

				if len(tids) >= 2 {
					b.AddEdgeCtx(sid, ensure(tids[len(tids)-1].Text()), "references", c.Line(), "field")
				}
			}

			return
		case "abstract_definition":
			if th := n.ChildOfType("type_head"); th != nil {
				name, sup := typeHead(th)
				if name != "" {
					aid := ids.MakeID(b.Stem, name)
					b.AddNode(aid, name, line)
					b.AddEdge(scope, aid, "defines", line)

					if sup != "" {
						b.AddEdge(aid, ensure(sup), "inherits", line)
					}
				}
			}

			return
		case "function_definition", "macro_definition":
			sig := n.ChildOfType("signature")
			if sig == nil {
				return
			}

			name := sigName(sig)
			if name == "" {
				return
			}

			var fid, label string

			if n.Type() == "macro_definition" {
				fid, label = ids.MakeID(b.Stem, "@"+name), "@"+name
			} else {
				fid, label = ids.MakeID(b.Stem, name), name+"()"
			}

			b.AddNode(fid, label, line)
			b.AddEdge(scope, fid, "defines", line)
			bodies = append(bodies, rustBody{id: fid, node: n})

			return
		case "macrocall_expression":
			mname := ""
			if mi := n.ChildOfType("macro_identifier"); mi != nil {
				if id := mi.ChildOfType("identifier"); id != nil {
					mname = id.Text()
				}
			}

			if mname != "enum" {
				for _, c := range n.Children() {
					walk(c, scope)
				}

				return
			}

			var args []*tsx.Node

			if al := n.ChildOfType("macro_argument_list"); al != nil {
				for _, c := range al.Children() {
					switch c.Type() {
					case "identifier", "typed_expression", "assignment":
						args = append(args, c)
					case "compound_statement":
						for _, cc := range c.Children() {
							if cc.Type() == "identifier" || cc.Type() == "assignment" {
								args = append(args, cc)
							}
						}
					}
				}
			}

			ident := func(x *tsx.Node) *tsx.Node {
				if x.Type() == "identifier" {
					return x
				}

				return x.ChildOfType("identifier")
			}

			if len(args) == 0 {
				return
			}

			tn := ident(args[0])
			if tn == nil {
				return
			}

			eid := ids.MakeID(b.Stem, tn.Text())
			b.AddNode(eid, tn.Text(), line)
			b.AddEdge(scope, eid, "defines", line)

			for _, m := range args[1:] {
				mn := ident(m)
				if mn == nil {
					continue
				}

				mid := ids.MakeID(b.Stem, tn.Text(), mn.Text())
				b.AddNode(mid, mn.Text(), mn.Line())
				b.AddEdge(eid, mid, "case_of", mn.Line())
			}

			return
		case "assignment":
			ch := n.Children()
			if len(ch) == 0 || ch[0].Type() != "call_expression" {
				return
			}

			lc := ch[0].Children()
			if len(lc) == 0 || lc[0].Type() != "identifier" {
				return
			}

			name := lc[0].Text()
			fid := ids.MakeID(b.Stem, name)
			b.AddNode(fid, name+"()", line)
			b.AddEdge(scope, fid, "defines", line)

			if len(ch) >= 3 {
				bodies = append(bodies, rustBody{id: fid, node: ch[len(ch)-1]})
			}

			return
		case "using_statement", "import_statement":
			modName := func(c *tsx.Node) string {
				switch c.Type() {
				case "import_path":
					var last string

					for _, s := range c.Children() {
						if s.Type() == "identifier" {
							last = s.Text()
						}
					}

					if !strings.HasPrefix(c.Text(), ".") {
						return c.Text()
					}

					return last
				case "identifier", "scoped_identifier":
					return c.Text()
				}

				return ""
			}

			emit := func(name string) {
				if name == "" {
					return
				}

				iid := ids.MakeID(name)
				b.AddNode(iid, name, line)
				b.AddEdgeCtx(scope, iid, "imports", line, "import")
			}

			for _, c := range n.Children() {
				switch c.Type() {
				case "identifier", "scoped_identifier", "import_path":
					emit(modName(c))
				case "selected_import":
					if p := c.ChildOfType("identifier", "scoped_identifier", "import_path"); p != nil {
						emit(modName(p))
					}
				}
			}

			return
		}

		for _, c := range n.Children() {
			walk(c, scope)
		}
	}

	walk(tree.Root, b.FileID)

	for _, bd := range bodies {
		if bd.node.Type() == "function_definition" || bd.node.Type() == "macro_definition" {
			for _, c := range bd.node.Children() {
				if c.Type() != "signature" {
					wc(c, bd.id)
				}
			}
		} else {
			wc(bd.node, bd.id)
		}
	}

	return b.ResultUnfiltered()
}
