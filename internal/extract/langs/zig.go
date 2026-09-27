package langs

import (
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// ExtractZig extracts a Zig file.
//
// Adapted from Graphify's graphify/extractors/zig.py (Apache-2.0).
func ExtractZig(path, _ string, src []byte) *model.Extraction {
	tree, err := tsx.Parse("zig", src)
	if err != nil {
		return &model.Extraction{Error: err.Error()}
	}
	defer tree.Release()

	b := base.NewBuilder(path)
	b.AddFileNode()

	var bodies []rustBody

	var extractImport func(n *tsx.Node, line int) bool
	extractImport = func(n *tsx.Node, line int) bool {
		for _, c := range n.Children() {
			switch c.Type() {
			case "builtin_function":
				bi, args := "", (*tsx.Node)(nil)

				for _, s := range c.Children() {
					switch s.Type() {
					case "builtin_identifier":
						bi = s.Text()
					case "arguments":
						args = s
					}
				}

				if (bi == "@import" || bi == "@cImport") && args != nil {
					for _, a := range args.Children() {
						if a.Type() == "string_literal" || a.Type() == "string" {
							raw := strings.Trim(a.Text(), `"`)
							if mod := strings.SplitN(lastSeg(raw, "/"), ".", 2)[0]; mod != "" {
								b.AddEdge(b.FileID, ids.MakeID(mod), "imports_from", line)
							}

							return true
						}
					}
				}
			case "field_expression":
				extractImport(c, line)

				return true
			}
		}

		return false
	}

	var walk func(n *tsx.Node, parent string)
	walk = func(n *tsx.Node, parent string) {
		switch n.Type() {
		case "function_declaration":
			nn := n.Field("name")
			if nn == nil {
				return
			}

			name, line := nn.Text(), n.Line()

			var fid string

			if parent != "" {
				fid = ids.MakeID(parent, name)
				b.AddNode(fid, "."+name+"()", line)
				b.AddEdge(parent, fid, "method", line)
			} else {
				fid = ids.MakeID(b.Stem, name)
				b.AddNode(fid, name+"()", line)
				b.AddEdge(b.FileID, fid, "contains", line)
			}

			if bd := n.Field("body"); bd != nil {
				bodies = append(bodies, rustBody{fid, bd})
			}

			return
		case "variable_declaration":
			var nameNode, value *tsx.Node

			for _, c := range n.Children() {
				switch c.Type() {
				case "identifier":
					nameNode = c
				case "struct_declaration", "enum_declaration", "union_declaration", "builtin_function", "field_expression":
					value = c
				}
			}

			if value == nil {
				return
			}

			switch value.Type() {
			case "struct_declaration", "enum_declaration", "union_declaration":
				if nameNode != nil {
					id := ids.MakeID(b.Stem, nameNode.Text())
					b.AddNode(id, nameNode.Text(), n.Line())
					b.AddEdge(b.FileID, id, "contains", n.Line())

					for _, c := range value.Children() {
						walk(c, id)
					}
				}
			default:
				extractImport(n, n.Line())
			}

			return
		}

		for _, c := range n.Children() {
			walk(c, parent)
		}
	}

	walk(tree.Root, "")

	seen := map[[2]string]bool{}

	var wc func(n *tsx.Node, caller string)
	wc = func(n *tsx.Node, caller string) {
		if n.Type() == "function_declaration" {
			return
		}

		if n.Type() == "call_expression" {
			if fn := n.Field("function"); fn != nil {
				text := fn.Text()
				callee := lastSeg(text, ".")
				member := strings.Contains(text, ".")

				tgt := ""

				for _, nd := range b.Nodes {
					if nd.Label == callee+"()" || nd.Label == "."+callee+"()" {
						tgt = nd.ID

						break
					}
				}

				switch {
				case tgt != "" && tgt != caller:
					pair := [2]string{caller, tgt}
					if !seen[pair] {
						seen[pair] = true
						b.AddEdge(caller, tgt, "calls", n.Line())
					}
				case tgt == "" && callee != "":
					b.RawCalls = append(b.RawCalls, &model.RawCall{
						CallerID: caller, Callee: callee, IsMemberCall: member, Language: "zig",
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

	return b.Result()
}
