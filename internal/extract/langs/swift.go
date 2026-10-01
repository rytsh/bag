package langs

import (
	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

func swiftKeyword(n *tsx.Node) string {
	for _, c := range n.Children() {
		if !c.IsNamed() {
			switch c.Type() {
			case "class", "struct", "enum", "extension", "actor":
				return c.Type()
			}
		}
	}

	return ""
}

func swiftTypeRefs(n *tsx.Node, generic bool, out *[]typeRef) {
	if n == nil {
		return
	}

	switch n.Type() {
	case "type_annotation":
		for _, c := range n.NamedChildren() {
			swiftTypeRefs(c, generic, out)
		}

		return
	case "user_type":
		if c := n.ChildOfType("type_identifier"); c != nil && c.Text() != "" {
			*out = append(*out, typeRef{c.Text(), roleOf(generic)})
		}

		for _, c := range n.Children() {
			if c.Type() == "type_arguments" {
				for _, a := range c.NamedChildren() {
					swiftTypeRefs(a, true, out)
				}
			}
		}

		return
	case "type_identifier":
		if n.Text() != "" {
			*out = append(*out, typeRef{n.Text(), roleOf(generic)})
		}

		return
	}

	for _, c := range n.NamedChildren() {
		swiftTypeRefs(c, generic, out)
	}
}

func swiftImport(x *generic.Ctx, n *tsx.Node) [][2]string {
	c := n.ChildOfType("identifier")
	if c == nil {
		return nil
	}

	raw := c.Text()
	id := ids.MakeID(raw)
	x.B.AddEdgeCtx(x.B.FileID, id, "imports", n.Line(), "import")

	return [][2]string{{id, raw}}
}

func swiftPreScan(x *generic.Ctx) {
	protos, classes := base.Set{}, base.Set{}

	x.Tree.Root.Walk(func(n *tsx.Node) bool {
		switch n.Type() {
		case "protocol_declaration":
			nn := n.Field("name")
			if nn == nil {
				nn = n.ChildOfType("type_identifier")
			}

			if nn != nil && nn.Text() != "" {
				protos[nn.Text()] = struct{}{}
			}
		case "class_declaration":
			switch swiftKeyword(n) {
			case "class", "struct", "enum", "actor":
				if nn := n.Field("name"); nn != nil && nn.Text() != "" {
					classes[nn.Text()] = struct{}{}
				}
			}
		}

		return true
	})

	x.Data["swift_protos"] = protos
	x.Data["swift_classes"] = classes
}

func swiftClassHook(x *generic.Ctx, n *tsx.Node, classID string, line int) {
	protos, _ := x.Data["swift_protos"].(base.Set)
	classes, _ := x.Data["swift_classes"].(base.Set)

	kind := "protocol"
	if n.Type() == "class_declaration" {
		kind = swiftKeyword(n)
	}

	seen := false

	for _, c := range n.Children() {
		if c.Type() != "inheritance_specifier" {
			continue
		}

		var (
			name string
			ut   *tsx.Node
		)

		for _, s := range c.Children() {
			if s.Type() == "user_type" {
				ut = s
				if ti := s.ChildOfType("type_identifier"); ti != nil {
					name = ti.Text()
				}

				break
			}

			if s.Type() == "type_identifier" {
				name = s.Text()

				break
			}
		}

		if name == "" {
			continue
		}

		rel := "inherits"

		if n.Type() != "protocol_declaration" {
			switch {
			case protos.Has(name):
				rel = "implements"
			case classes.Has(name):
				rel = "inherits"
			case kind == "struct" || kind == "enum" || kind == "extension" || kind == "actor":
				rel = "implements"
			case seen:
				rel = "implements"
			}
		}

		seen = true

		x.B.AddEdge(classID, x.EnsureBase(name), rel, line)

		if ut != nil {
			for _, ta := range ut.Children() {
				if ta.Type() != "type_arguments" {
					continue
				}

				for _, a := range ta.NamedChildren() {
					var refs []typeRef
					swiftTypeRefs(a, true, &refs)

					for _, r := range refs {
						x.B.AddEdgeCtx(classID, x.EnsureNamed(r.name), "references", line, "generic_arg")
					}
				}
			}
		}
	}
}

// swiftTypeChild returns the type node of a parameter/declaration. The
// Python grammar exposes it via a "type"/"return_type" field; gotreesitter's
// Swift grammar tags it as an unnamed-field type node after the name.
func swiftTypeChild(n *tsx.Node, field string) *tsx.Node {
	if t := n.Field(field); t != nil {
		return t
	}

	return nil
}

var swiftTypeNodeTypes = base.NewSet("user_type", "array_type", "dictionary_type", "optional_type",
	"implicitly_unwrapped_optional_type", "tuple_type", "function_type", "type_identifier")

func swiftParamType(p *tsx.Node) *tsx.Node {
	if t := swiftTypeChild(p, "type"); t != nil {
		return t
	}

	for _, c := range p.NamedChildren() {
		if swiftTypeNodeTypes.Has(c.Type()) {
			return c
		}
	}

	return nil
}

func swiftReturnType(fn *tsx.Node) *tsx.Node {
	if t := swiftTypeChild(fn, "return_type"); t != nil {
		return t
	}

	arrow := false

	for _, c := range fn.Children() {
		if c.Type() == "->" {
			arrow = true

			continue
		}

		if arrow && c.IsNamed() {
			if swiftTypeNodeTypes.Has(c.Type()) {
				return c
			}

			return nil
		}
	}

	return nil
}

func swiftFunctionHook(x *generic.Ctx, n *tsx.Node, funcID string, line int) {
	for _, p := range n.Children() {
		if p.Type() != "parameter" {
			continue
		}

		var refs []typeRef
		swiftTypeRefs(swiftParamType(p), false, &refs)
		emitRefs(x, funcID, line, refs, "parameter_type")

		for _, r := range refs {
			if r.role == "type" {
				if nn := p.ChildOfType("simple_identifier"); nn != nil {
					swiftTablesOf(x).table[nn.Text()] = r.name
				}

				break
			}
		}
	}

	if rt := swiftReturnType(n); rt != nil {
		var refs []typeRef
		swiftTypeRefs(rt, false, &refs)

		types := 0

		for _, r := range refs {
			if r.role == "type" {
				types++
			}
		}

		plain := rt.Type() == "user_type" && types == 1

		for _, r := range refs {
			ctx := "return_type"
			if r.role == "generic_arg" {
				ctx = "generic_arg"
			}

			tgt := x.EnsureNamed(r.name)
			if tgt == funcID {
				continue
			}

			e := x.B.AddEdgeCtx(funcID, tgt, "references", line, ctx)
			if plain && r.role == "type" {
				e.Metadata = map[string]any{"swift_plain_return": true}
			}
		}
	}
}

func swiftExtraWalk(x *generic.Ctx, n *tsx.Node, parentClass string) bool {
	if parentClass == "" {
		return false
	}

	switch n.Type() {
	case "enum_entry":
		for _, c := range n.Children() {
			if c.Type() == "simple_identifier" {
				id := ids.MakeID(parentClass, c.Text())
				x.B.AddNode(id, c.Text(), n.Line())
				x.B.AddEdge(parentClass, id, "case_of", n.Line())
			}
		}

		for _, c := range n.Children() {
			if c.Type() != "enum_type_parameters" {
				continue
			}

			for _, g := range c.NamedChildren() {
				var refs []typeRef
				swiftTypeRefs(g, false, &refs)

				for _, r := range refs {
					ctx := "type"
					if r.role == "generic_arg" {
						ctx = "generic_arg"
					}

					x.Ref(parentClass, x.EnsureNamed(r.name), n.Line(), ctx)
				}
			}
		}

		return true
	case "property_declaration":
		line := n.Line()
		annotated := ""

		if ta := n.ChildOfType("type_annotation"); ta != nil {
			var refs []typeRef
			swiftTypeRefs(ta, false, &refs)

			for _, r := range refs {
				ctx := "field"
				if r.role == "generic_arg" {
					ctx = "generic_arg"
				}

				x.Ref(parentClass, x.EnsureNamed(r.name), line, ctx)

				if annotated == "" && r.role == "type" {
					annotated = r.name
				}
			}
		}

		swiftRecordProperty(x, n, annotated)

		for _, c := range n.Children() {
			if x.Cfg.CallTypes.Has(c.Type()) {
				x.AddInitializer(parentClass, c)
			}
		}

		name := ""

		for _, c := range n.Children() {
			if c.Type() == "pattern" {
				if s := c.ChildOfType("simple_identifier"); s != nil {
					name = s.Text()

					break
				}
			}

			if c.Type() == "simple_identifier" {
				name = c.Text()

				break
			}
		}

		var comp []*tsx.Node

		for _, c := range n.Children() {
			if c.Type() == "computed_property" || c.Type() == "willset_didset_block" {
				comp = append(comp, c)
			}
		}

		if len(comp) > 0 && name != "" {
			pid := ids.MakeID(parentClass, name)
			x.B.AddNode(pid, "."+name, line)
			x.B.AddEdge(parentClass, pid, "method", line)

			for _, c := range comp {
				x.AddBody(pid, c)
			}
		}

		return true
	}

	return false
}

func swiftCallName(_ *generic.Ctx, n *tsx.Node) (string, bool, string) {
	ch := n.Children()
	if len(ch) == 0 {
		return "", false, ""
	}

	first := ch[0]

	switch first.Type() {
	case "simple_identifier":
		return first.Text(), false, ""
	case "navigation_expression":
		callee := ""

		for _, c := range first.Children() {
			if c.Type() == "navigation_suffix" {
				for _, s := range c.Children() {
					if s.Type() == "simple_identifier" {
						callee = s.Text()
					}
				}
			}
		}

		recv := ""

		if fc := first.Children(); len(fc) > 0 {
			switch fc[0].Type() {
			case "simple_identifier":
				recv = fc[0].Text()
			case "self_expression":
				recv = "self"
			}
		}

		return callee, true, recv
	}

	return "", false, ""
}

var swiftConfig = &generic.Config{
	Lang:       "swift",
	Grammar:    "swift",
	ClassTypes: base.NewSet("class_declaration", "protocol_declaration"),
	FunctionTypes: base.NewSet("function_declaration", "protocol_function_declaration", "init_declaration",
		"deinit_declaration", "subscript_declaration"),
	ImportTypes:  base.NewSet("import_declaration"),
	CallTypes:    base.NewSet("call_expression"),
	NameFallback: []string{"simple_identifier", "type_identifier", "user_type"},
	BodyFallback: []string{"class_body", "protocol_body", "function_body", "enum_class_body"},
	FunctionBoundary: base.NewSet("function_declaration", "protocol_function_declaration", "init_declaration",
		"deinit_declaration", "subscript_declaration"),
	ImportHandler:   swiftImport,
	ClassHook:       swiftClassHook,
	FunctionHook:    swiftFunctionHook,
	ExtraWalk:       swiftExtraWalk,
	CallName:        swiftCallName,
	PreScan:         swiftPreScan,
	PostProcess:     swiftPostProcess,
	DecorateRawCall: swiftDecorateRawCall,
}

// ExtractSwift extracts a Swift file.
func ExtractSwift(path, root string, src []byte) *model.Extraction {
	return generic.Extract(swiftConfig, path, root, src)
}
