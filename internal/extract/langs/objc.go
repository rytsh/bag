package langs

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

var objcStemPart = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func objcCategoryBaseStem(stem string) string {
	head, sep, tail := "", "", stem
	if i := strings.LastIndexByte(stem, '/'); i >= 0 {
		head, sep, tail = stem[:i], "/", stem[i+1:]
	}

	b, suf, ok := strings.Cut(tail, "+")
	if !ok || !objcStemPart.MatchString(b) || !objcStemPart.MatchString(suf) {
		return stem
	}

	return head + sep + b
}

func objcIsCategory(n *tsx.Node) bool {
	for _, c := range n.Children() {
		if c.Type() == "(" {
			return true
		}
	}

	return false
}

type objcBody struct {
	id, container string
	node          *tsx.Node
}

// ExtractObjC extracts Objective-C (.m/.mm/.h) files.
//
// Adapted from Graphify's graphify/extractors/objc.py (Apache-2.0).
func ExtractObjC(path, _ string, src []byte) *model.Extraction {
	for _, m := range []string{"NS_ASSUME_NONNULL_BEGIN", "NS_ASSUME_NONNULL_END"} {
		src = []byte(strings.ReplaceAll(string(src), m, strings.Repeat(" ", len(m))))
	}

	tree, err := tsx.Parse("objc", src)
	if err != nil {
		return &model.Extraction{Error: err.Error()}
	}
	defer tree.Release()

	b := base.NewBuilder(path)
	b.AddFileNode()

	ensure := func(name string) string { return b.EnsureNamed(b.Stem, name) }

	var bodies []objcBody

	fieldTypes := map[string]map[string]string{}
	conflicted := map[[2]string]bool{}
	recordField := func(cls, field, typ string) {
		if conflicted[[2]string{cls, field}] {
			return
		}

		t := fieldTypes[cls]
		if t == nil {
			t = map[string]string{}
			fieldTypes[cls] = t
		}

		if prev, ok := t[field]; ok && prev != typ {
			delete(t, field)
			conflicted[[2]string{cls, field}] = true

			return
		}

		t[field] = typ
	}

	collectIvars := func(ivars *tsx.Node, cls string) {
		for _, iv := range ivars.Children() {
			if iv.Type() != "instance_variable" {
				continue
			}

			for _, sd := range iv.Children() {
				if sd.Type() == "struct_declaration" {
					if f, t, ok := objcFieldDeclEntry(sd); ok {
						recordField(cls, f, t)
					}
				}
			}
		}
	}

	var typeIdents func(n *tsx.Node, out *[]*tsx.Node)
	typeIdents = func(n *tsx.Node, out *[]*tsx.Node) {
		if n.Type() == "type_identifier" {
			*out = append(*out, n)

			return
		}

		for _, c := range n.Children() {
			typeIdents(c, out)
		}
	}

	var walk func(n *tsx.Node, parent string)
	walk = func(n *tsx.Node, parent string) {
		t := n.Type()
		line := n.Line()

		switch t {
		case "preproc_include":
			for _, c := range n.Children() {
				switch c.Type() {
				case "system_lib_string":
					raw := strings.Trim(c.Text(), "<>")
					if mod := strings.ReplaceAll(lastSeg(raw, "/"), ".h", ""); mod != "" {
						b.AddEdgeCtx(b.FileID, ids.MakeID(mod), "imports", line, "import")
					}
				case "string_literal":
					for _, s := range c.Children() {
						if s.Type() != "string_content" {
							continue
						}

						raw := s.Text()
						cand := filepath.Clean(filepath.Join(filepath.Dir(path), raw))

						if isFile(cand) {
							b.AddEdgeCtx(b.FileID, ids.MakeID(cand), "imports", line, "import")
						} else if mod := strings.ReplaceAll(lastSeg(raw, "/"), ".h", ""); mod != "" {
							b.AddEdgeCtx(b.FileID, ids.MakeID(mod), "imports", line, "import")
						}
					}
				}
			}

			return
		case "module_import":
			if p := n.Field("path"); p != nil {
				if mod := strings.TrimSpace(strings.SplitN(p.Text(), ".", 2)[0]); mod != "" {
					b.AddEdgeCtx(b.FileID, ids.MakeID(mod), "imports", line, "import")
				}
			}

			return
		case "class_interface":
			var idents []*tsx.Node

			for _, c := range n.Children() {
				if c.Type() == "identifier" {
					idents = append(idents, c)
				}
			}

			if len(idents) == 0 {
				for _, c := range n.Children() {
					walk(c, parent)
				}

				return
			}

			name := idents[0].Text()

			stem := b.Stem
			if objcIsCategory(n) {
				stem = objcCategoryBaseStem(stem)
			}

			cls := ids.MakeID(stem, name)
			b.AddNode(cls, name, line)
			b.AddEdge(b.FileID, cls, "contains", line)

			colon := false

			for _, c := range n.Children() {
				switch {
				case c.Type() == ":":
					colon = true
				case colon && c.Type() == "identifier":
					b.AddEdge(cls, ensure(c.Text()), "inherits", line)
					colon = false
				case c.Type() == "parameterized_arguments":
					for _, s := range c.Children() {
						if s.Type() == "type_name" {
							for _, ti := range s.Children() {
								if ti.Type() == "type_identifier" || ti.Type() == "identifier" {
									b.AddEdge(cls, ensure(ti.Text()), "implements", line)
								}
							}
						}
					}
				case c.Type() == "property_declaration":
					pl := c.Line()

					for _, s := range c.Children() {
						if s.Type() != "struct_declaration" {
							continue
						}

						seen := map[string]bool{}

						for _, sc := range s.Children() {
							if sc.Type() == "struct_declarator" || sc.Type() == ";" {
								continue
							}

							var tis []*tsx.Node
							typeIdents(sc, &tis)

							for _, ti := range tis {
								if seen[ti.Text()] {
									continue
								}

								seen[ti.Text()] = true
								b.AddEdgeCtx(cls, ensure(ti.Text()), "references", pl, "field")
							}
						}

						if f, t, ok := objcFieldDeclEntry(s); ok {
							recordField(cls, f, t)
						}
					}
				case c.Type() == "instance_variables":
					collectIvars(c, cls)
				case c.Type() == "method_declaration":
					walk(c, cls)
				}
			}

			return
		case "class_implementation":
			name := ""
			if c := n.ChildOfType("identifier"); c != nil {
				name = c.Text()
			}

			if name == "" {
				for _, c := range n.Children() {
					walk(c, parent)
				}

				return
			}

			stem := b.Stem
			if objcIsCategory(n) {
				stem = objcCategoryBaseStem(stem)
			}

			impl := ids.MakeID(stem, name)
			if !b.Has(impl) {
				b.AddNode(impl, name, line)
				b.AddEdge(b.FileID, impl, "contains", line)
			}

			for _, c := range n.Children() {
				switch c.Type() {
				case "instance_variables":
					collectIvars(c, impl)
				case "implementation_definition":
					for _, s := range c.Children() {
						walk(s, impl)
					}
				}
			}

			return
		case "protocol_declaration":
			c := n.ChildOfType("identifier")
			if c == nil {
				return
			}

			name := c.Text()
			proto := ids.MakeID(b.Stem, name)
			b.AddNode(proto, "<"+name+">", line)
			b.AddEdge(b.FileID, proto, "contains", line)

			for _, ch := range n.Children() {
				if ch.Type() != "protocol_reference_list" {
					continue
				}

				for _, s := range ch.Children() {
					if s.Type() == "identifier" {
						if bid := ensure(s.Text()); bid != proto {
							b.AddEdge(proto, bid, "implements", line)
						}
					}
				}
			}

			for _, ch := range n.Children() {
				walk(ch, proto)
			}

			return
		case "method_declaration", "method_definition":
			container := parent
			if container == "" {
				container = b.FileID
			}

			prefix := "-"

			for _, c := range n.Children() {
				if c.Type() == "+" || c.Type() == "-" {
					prefix = c.Type()

					break
				}
			}

			var parts []string

			for _, c := range n.Children() {
				if c.Type() == "identifier" {
					parts = append(parts, c.Text())
				}
			}

			if len(parts) == 0 {
				return
			}

			name := strings.Join(parts, "")
			mid := ids.MakeID(container, name)
			b.AddNode(mid, prefix+name, line)
			b.AddEdge(container, mid, "method", line)

			if t == "method_definition" {
				bodies = append(bodies, objcBody{mid, container, n})
			}

			return
		}

		for _, c := range n.Children() {
			walk(c, parent)
		}
	}

	walk(tree.Root, "")

	var allMethods []string

	for _, n := range b.Nodes {
		if n.ID != b.FileID {
			allMethods = append(allMethods, n.ID)
		}
	}

	classMethods := map[string]map[string]bool{}
	for _, bd := range bodies {
		if classMethods[bd.container] == nil {
			classMethods[bd.container] = map[string]bool{}
		}

		classMethods[bd.container][bd.id] = true
	}

	seen := map[[2]string]bool{}

	for _, bd := range bodies {
		caller, siblings := bd.id, classMethods[bd.container]

		var wc func(n *tsx.Node)
		wc = func(n *tsx.Node) {
			switch n.Type() {
			case "message_expression":
				meth, recv := n.Field("method"), n.Field("receiver")
				if meth != nil && meth.Type() == "identifier" && meth.Text() == "alloc" && recv != nil && recv.Type() == "identifier" {
					if tid := ensure(recv.Text()); tid != caller {
						b.AddEdgeCtx(caller, tid, "references", n.Line(), "type")
					}
				}

				var parts []string

				for i, c := range n.Children() {
					if n.FieldNameForChild(i) == "method" && c.Type() == "identifier" {
						parts = append(parts, c.Text())
					}
				}

				name := strings.Join(parts, "")
				if name != "" {
					needle := strings.TrimLeft(ids.MakeID("", name), "_")

					for _, cand := range allMethods {
						if strings.HasSuffix(cand, needle) {
							pair := [2]string{caller, cand}
							if !seen[pair] && caller != cand {
								seen[pair] = true
								b.AddEdgeCtx(caller, cand, "calls", n.Line(), "call")
							}
						}
					}

					if recv != nil && recv.Type() == "identifier" {
						b.RawCalls = append(b.RawCalls, &model.RawCall{
							CallerID: caller, Callee: name, IsMemberCall: true, Receiver: recv.Text(),
							Language: "objc", SourceFile: path, SourceLocation: base.Loc(n.Line()),
						})
					} else if recv != nil && recv.Type() == "field_expression" {
						// Only the exact `self.<field>` shape; the bare field name
						// is typed through the class's property/ivar table.
						if k := recv.Children(); len(k) == 3 && k[0].Type() == "identifier" && k[0].Text() == "self" &&
							k[1].Type() == "." && k[2].Type() == "field_identifier" {
							b.RawCalls = append(b.RawCalls, &model.RawCall{
								CallerID: caller, Callee: name, IsMemberCall: true, Receiver: k[2].Text(),
								ReceiverType: objcSelfField, Language: "objc", SourceFile: path,
								SourceLocation: base.Loc(n.Line()),
							})
						}
					}
				}
			case "field_expression":
				for _, c := range n.Children() {
					if c.Type() != "field_identifier" {
						continue
					}

					tgt := ids.MakeID(bd.container, c.Text())
					if siblings[tgt] && tgt != caller {
						pair := [2]string{caller, tgt}
						if !seen[pair] {
							seen[pair] = true
							b.AddEdge(caller, tgt, "accesses", n.Line())
						}
					}
				}
			case "selector_expression":
				var parts []string

				for _, c := range n.Children() {
					if c.Type() == "identifier" {
						parts = append(parts, c.Text())
					}
				}

				sel := strings.Join(parts, "")
				if sel == "" {
					break
				}

				var matches []string

				ms := map[string]bool{}

				for _, o := range bodies {
					if o.id == ids.MakeID(o.container, sel) && o.id != caller && !ms[o.id] {
						ms[o.id] = true
						matches = append(matches, o.id)
					}
				}

				if len(matches) == 1 {
					pair := [2]string{caller, matches[0]}
					if !seen[pair] {
						seen[pair] = true
						b.AddEdgeCtx(caller, matches[0], "calls", n.Line(), "call")
					}
				}
			}

			for _, c := range n.Children() {
				wc(c)
			}
		}

		wc(bd.node)
	}

	res := b.ResultUnfiltered()

	table := map[string]string{}
	for _, bd := range bodies {
		objcLocalVarTypes(bd.node, table)
	}

	if len(table) > 0 {
		res.TypeTable = table
	}

	for cls, tbl := range fieldTypes {
		n := b.Get(cls)
		if n == nil || len(tbl) == 0 {
			continue
		}

		if res.FieldTables == nil {
			res.FieldTables = map[string]map[string]string{}
		}

		m := res.FieldTables[n.Label]
		if m == nil {
			m = map[string]string{}
			res.FieldTables[n.Label] = m
		}

		for f, t := range tbl {
			m[f] = t
		}
	}

	return res
}

// objcSelfField marks a raw call whose receiver is `self.<field>`.
const objcSelfField = "\x00self_field"

// objcFieldDeclEntry returns (field, Type) of a property/ivar declaration
// with one bare capitalized type and one simple declarator.
//
// Adapted from Graphify's extract_objc._field_decl_entry (Apache-2.0).
func objcFieldDeclEntry(sd *tsx.Node) (string, string, bool) {
	var types, decls []*tsx.Node

	for _, c := range sd.Children() {
		switch c.Type() {
		case "type_identifier":
			types = append(types, c)
		case "struct_declarator":
			decls = append(decls, c)
		}
	}

	if len(types) != 1 || len(decls) != 1 {
		return "", "", false
	}

	typ := strings.TrimSpace(types[0].Text())
	if typ == "" || !upperStart(typ) {
		return "", "", false
	}

	inner := decls[0].Children()
	if len(inner) != 1 {
		return "", "", false
	}

	f := cppDeclaratorName(inner[0])
	if f == "" {
		return "", "", false
	}

	return f, typ, true
}

// objcLocalVarTypes collects `var -> ClassName` from `Foo *f = ...;` locals.
//
// Adapted from Graphify's _objc_local_var_types (Apache-2.0).
func objcLocalVarTypes(body *tsx.Node, table map[string]string) {
	stack := []*tsx.Node{body}

	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if n.Type() == "method_definition" && n != body {
			continue
		}

		if n.Type() == "declaration" {
			tn := n.Field("type")
			if tn == nil {
				tn = n.ChildOfType("type_identifier")
			}

			if tn != nil && tn.Type() == "type_identifier" {
				typ := strings.TrimSpace(tn.Text())

				var decls []*tsx.Node

				for _, c := range n.Children() {
					switch c.Type() {
					case "identifier", "pointer_declarator", "init_declarator":
						decls = append(decls, c)
					}
				}

				if typ != "" && upperStart(typ) && len(decls) == 1 {
					if v := cppDeclaratorName(decls[0]); v != "" {
						if _, ok := table[v]; !ok {
							table[v] = typ
						}
					}
				}
			}
		}

		stack = append(stack, n.Children()...)
	}
}
