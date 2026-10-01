package langs

import (
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

func phpNameText(n *tsx.Node) string {
	if n == nil {
		return ""
	}

	return lastSeg(n.Text(), `\`)
}

func phpTypeRefs(n *tsx.Node, generic bool, out *[]typeRef) {
	if n == nil {
		return
	}

	switch n.Type() {
	case "primitive_type":
		return
	case "named_type":
		if c := n.ChildOfType("name", "qualified_name"); c != nil {
			if s := phpNameText(c); s != "" {
				*out = append(*out, typeRef{s, roleOf(generic)})
			}
		}

		return
	case "name", "qualified_name":
		if s := phpNameText(n); s != "" {
			*out = append(*out, typeRef{s, roleOf(generic)})
		}

		return
	}

	for _, c := range n.NamedChildren() {
		phpTypeRefs(c, generic, out)
	}
}

var phpTypeNodes = base.NewSet("named_type", "primitive_type", "nullable_type", "union_type", "intersection_type", "optional_type")

func phpImport(x *generic.Ctx, n *tsx.Node) [][2]string {
	sawAs := false
	mod := ""

	for _, c := range n.Children() {
		if c.Type() == "as" {
			sawAs = true

			continue
		}

		if c.Type() == "qualified_name" || c.Type() == "name" || c.Type() == "identifier" {
			bare := strings.TrimSpace(lastSeg(c.Text(), `\`))
			if sawAs {
				mod = bare

				break
			}

			if mod == "" {
				mod = bare
			}
		}
	}

	if mod != "" {
		x.B.AddEdgeCtx(x.B.FileID, ids.MakeID(mod), "imports", n.Line(), "import")
	}

	return nil
}

func phpClassHook(x *generic.Ctx, n *tsx.Node, classID string, _ int) {
	emit := func(name, rel string, line int) {
		if name != "" {
			x.B.AddEdge(classID, x.EnsureBase(name), rel, line)
		}
	}

	for _, c := range n.Children() {
		switch c.Type() {
		case "base_clause":
			for _, s := range c.Children() {
				if s.Type() == "name" || s.Type() == "qualified_name" {
					emit(phpNameText(s), "inherits", c.Line())
				}
			}
		case "class_interface_clause":
			for _, s := range c.Children() {
				if s.Type() == "name" || s.Type() == "qualified_name" {
					emit(phpNameText(s), "implements", c.Line())
				}
			}
		}
	}

	bd := n.Field("body")
	if bd == nil {
		bd = n.ChildOfType("declaration_list")
	}

	if bd == nil {
		return
	}

	for _, m := range bd.Children() {
		if m.Type() != "use_declaration" {
			continue
		}

		for _, s := range m.Children() {
			if s.Type() == "name" || s.Type() == "qualified_name" {
				emit(phpNameText(s), "mixes_in", m.Line())
			}
		}
	}
}

func phpFunctionHook(x *generic.Ctx, n *tsx.Node, funcID string, line int) {
	parentClass := x.ParentOf(funcID)

	if params := n.ChildOfType("formal_parameters"); params != nil {
		for _, p := range params.Children() {
			if p.Type() != "simple_parameter" && p.Type() != "property_promotion_parameter" {
				continue
			}

			promoted := p.Type() == "property_promotion_parameter"

			var tn *tsx.Node

			for _, s := range p.Children() {
				if phpTypeNodes.Has(s.Type()) {
					tn = s

					break
				}
			}

			var refs []typeRef
			phpTypeRefs(tn, false, &refs)

			for _, r := range refs {
				ctx := "parameter_type"
				if r.role == "generic_arg" {
					ctx = "generic_arg"
				}

				tgt := x.EnsureNamed(r.name)
				x.Ref(funcID, tgt, line, ctx)

				if promoted && parentClass != "" && tgt != parentClass {
					fctx := "field"
					if r.role == "generic_arg" {
						fctx = "generic_arg"
					}

					x.B.AddEdgeCtx(parentClass, tgt, "references", line, fctx)
				}
			}
		}
	}

	saw := false

	for _, c := range n.Children() {
		if c.Type() == "formal_parameters" {
			saw = true

			continue
		}

		if saw && c.IsNamed() && c.Type() != "compound_statement" && phpTypeNodes.Has(c.Type()) {
			var refs []typeRef
			phpTypeRefs(c, false, &refs)
			emitRefs(x, funcID, line, refs, "return_type")

			break
		}
	}

	if bd := n.Field("body"); bd != nil {
		x.Walk(bd, parentClass)
	}
}

func phpExtraWalk(x *generic.Ctx, n *tsx.Node, parentClass string) bool {
	switch n.Type() {
	case "namespace_definition":
		return false
	case "property_declaration":
		if parentClass == "" {
			return false
		}

		for _, c := range n.Children() {
			if !phpTypeNodes.Has(c.Type()) {
				continue
			}

			var refs []typeRef
			phpTypeRefs(c, false, &refs)

			for _, r := range refs {
				ctx := "field"
				if r.role == "generic_arg" {
					ctx = "generic_arg"
				}

				x.Ref(parentClass, x.EnsureNamed(r.name), n.Line(), ctx)
			}

			break
		}

		return true
	}

	if x.Cfg.FunctionTypes.Has(n.Type()) && (n.Type() == "anonymous_function" || n.Type() == "arrow_function") {
		key := parentClass
		if key == "" {
			key = x.B.Stem
		}

		counts, _ := x.Data["php_closures"].(map[string]int)
		if counts == nil {
			counts = map[string]int{}
			x.Data["php_closures"] = counts
		}

		counts[key]++
		name := "{closure#" + itoa(counts[key]) + "}"
		x.WalkFunctionNamed(n, parentClass, name)

		return true
	}

	return false
}

func phpCallName(_ *generic.Ctx, n *tsx.Node) (string, bool, string) {
	switch n.Type() {
	case "object_creation_expression":
		for _, c := range n.Children() {
			if c.Type() == "name" || c.Type() == "qualified_name" {
				t := lastSeg(c.Text(), `\`)
				if l := strings.ToLower(t); l != "self" && l != "static" && l != "parent" {
					return t, false, ""
				}

				return "", false, ""
			}
		}

		return "", false, ""
	case "function_call_expression":
		if f := n.Field("function"); f != nil {
			return f.Text(), false, ""
		}
	case "scoped_call_expression":
		if s := n.Field("scope"); s != nil {
			return s.Text(), false, ""
		}
	default:
		if nn := n.Field("name"); nn != nil {
			return nn.Text(), true, ""
		}

		return "", true, ""
	}

	return "", false, ""
}

var phpConfig = &generic.Config{
	Lang:    "php",
	Grammar: "php",
	ClassTypes: base.NewSet("class_declaration", "interface_declaration", "enum_declaration",
		"trait_declaration"),
	FunctionTypes: base.NewSet("function_definition", "method_declaration", "anonymous_function", "arrow_function"),
	ImportTypes:   base.NewSet("namespace_use_clause"),
	CallTypes: base.NewSet("function_call_expression", "member_call_expression", "scoped_call_expression",
		"class_constant_access_expression", "object_creation_expression"),
	CallFunctionField: "function",
	CallAccessorTypes: base.NewSet("member_call_expression"),
	CallAccessorField: "name",
	NameFallback:      []string{"name"},
	BodyFallback:      []string{"declaration_list", "compound_statement", "enum_declaration_list"},
	FunctionBoundary:  base.NewSet("function_definition", "method_declaration", "anonymous_function", "arrow_function"),
	ImportHandler:     phpImport,
	ClassHook:         phpClassHook,
	FunctionHook:      phpFunctionHook,
	ExtraWalk:         phpExtraWalk,
	CallName:          phpCallName,
}

// ExtractPHP extracts a PHP file.
func ExtractPHP(path, root string, src []byte) *model.Extraction {
	return generic.Extract(phpConfig, path, root, src)
}
