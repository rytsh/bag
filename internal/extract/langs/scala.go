package langs

import (
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

func scalaTypeRefs(n *tsx.Node, generic bool, out *[]typeRef) {
	if n == nil {
		return
	}

	switch n.Type() {
	case "type_identifier":
		if s := n.Text(); s != "" {
			*out = append(*out, typeRef{s, roleOf(generic)})
		}
	case "generic_type":
		b := n.Field("type")
		if b == nil {
			b = n.ChildOfType("type_identifier")
		}

		if b != nil && b.Type() == "type_identifier" && b.Text() != "" {
			*out = append(*out, typeRef{b.Text(), roleOf(generic)})
		}

		for _, c := range n.Children() {
			if c.Type() == "type_arguments" {
				for _, a := range c.NamedChildren() {
					scalaTypeRefs(a, true, out)
				}
			}
		}
	case "compound_type", "infix_type", "function_type", "tuple_type", "annotated_type", "projected_type":
		for _, c := range n.NamedChildren() {
			scalaTypeRefs(c, generic, out)
		}
	}
}

func scalaImport(x *generic.Ctx, n *tsx.Node) [][2]string {
	raw := ""
	if c := n.ChildOfType("stable_id", "identifier"); c != nil {
		raw = c.Text()
	}

	mod := strings.Trim(lastSeg(raw, "."), "{} ")
	if mod != "" && mod != "_" {
		x.B.AddEdgeCtx(x.B.FileID, ids.MakeID(mod), "imports", n.Line(), "import")
	}

	return nil
}

func scalaBaseName(n *tsx.Node) string {
	switch n.Type() {
	case "type_identifier":
		return n.Text()
	case "stable_type_identifier":
		ch := n.Children()
		for i := len(ch) - 1; i >= 0; i-- {
			if ch[i].Type() == "type_identifier" || ch[i].Type() == "identifier" {
				return ch[i].Text()
			}
		}
	case "generic_type":
		b := n.Field("type")
		if b == nil {
			b = n.ChildOfType("type_identifier", "stable_type_identifier")
		}

		if b != nil {
			return scalaBaseName(b)
		}
	}

	return ""
}

func scalaClassHook(x *generic.Ctx, n *tsx.Node, classID string, _ int) {
	ext := n.Field("extend")
	if ext == nil {
		ext = n.ChildOfType("extends_clause")
	}

	if ext != nil {
		idx := 0

		for _, c := range ext.Children() {
			name := scalaBaseName(c)
			if name == "" {
				continue
			}

			rel := "mixes_in"
			if idx == 0 {
				rel = "inherits"
			}

			idx++

			if tgt := x.EnsureNamed(name); tgt != classID {
				x.B.AddEdge(classID, tgt, rel, c.Line())
			}
		}
	}

	for _, c := range n.Children() {
		if c.Type() != "class_parameters" {
			continue
		}

		for _, cp := range c.Children() {
			if cp.Type() != "class_parameter" {
				continue
			}

			var refs []typeRef
			scalaTypeRefs(cp.Field("type"), false, &refs)

			for _, r := range refs {
				ctx := "field"
				if r.role == "generic_arg" {
					ctx = "generic_arg"
				}

				x.Ref(classID, x.EnsureNamed(r.name), cp.Line(), ctx)
			}
		}
	}
}

func scalaFunctionHook(x *generic.Ctx, n *tsx.Node, funcID string, line int) {
	if params := n.ChildOfType("parameters"); params != nil {
		for _, p := range params.Children() {
			if p.Type() != "parameter" {
				continue
			}

			var refs []typeRef
			scalaTypeRefs(p.Field("type"), false, &refs)
			emitRefs(x, funcID, line, refs, "parameter_type")
		}
	}

	var refs []typeRef
	scalaTypeRefs(n.Field("return_type"), false, &refs)
	emitRefs(x, funcID, line, refs, "return_type")
}

func scalaExtraWalk(x *generic.Ctx, n *tsx.Node, parentClass string) bool {
	if parentClass == "" {
		return false
	}

	switch n.Type() {
	case "enum_case_definitions":
		// Adapted from Graphify's _scala_extra_walk (Apache-2.0).
		for _, c := range n.Children() {
			if c.Type() != "simple_enum_case" && c.Type() != "full_enum_case" {
				continue
			}
			if nn := c.ChildOfType("identifier"); nn != nil && nn.Text() != "" {
				id := ids.MakeID(parentClass, nn.Text())
				if x.B.Get(id) == nil {
					x.B.AddNode(id, nn.Text(), c.Line())
					x.B.AddEdge(parentClass, id, "case_of", c.Line())
				}
			}
		}
		return true
	case "val_definition", "var_definition":
		var refs []typeRef
		scalaTypeRefs(n.Field("type"), false, &refs)

		for _, r := range refs {
			ctx := "field"
			if r.role == "generic_arg" {
				ctx = "generic_arg"
			}

			x.Ref(parentClass, x.EnsureNamed(r.name), n.Line(), ctx)
		}
	case "self_type":
		nc := n.NamedChildren()
		if len(nc) >= 2 {
			var refs []typeRef
			scalaTypeRefs(nc[1], false, &refs)

			for _, r := range refs {
				if tgt := x.EnsureNamed(r.name); tgt != parentClass {
					x.B.AddEdge(parentClass, tgt, "requires", n.Line())
				}
			}
		}
	}

	return false
}

func scalaCallName(_ *generic.Ctx, n *tsx.Node) (string, bool, string) {
	ch := n.Children()
	if len(ch) == 0 {
		return "", false, ""
	}

	first := ch[0]

	switch first.Type() {
	case "identifier":
		return first.Text(), false, ""
	case "field_expression":
		if f := first.Field("field"); f != nil {
			return f.Text(), true, ""
		}

		fc := first.Children()
		for i := len(fc) - 1; i >= 0; i-- {
			if fc[i].Type() == "identifier" {
				return fc[i].Text(), true, ""
			}
		}

		return "", true, ""
	}

	return "", false, ""
}

var scalaConfig = &generic.Config{
	Lang:              "scala",
	Grammar:           "scala",
	ClassTypes:        base.NewSet("class_definition", "object_definition", "trait_definition", "enum_definition"),
	FunctionTypes:     base.NewSet("function_definition"),
	ImportTypes:       base.NewSet("import_declaration"),
	CallTypes:         base.NewSet("call_expression"),
	CallAccessorTypes: base.NewSet("field_expression"),
	CallAccessorField: "field",
	NameFallback:      []string{"identifier"},
	BodyFallback:      []string{"template_body", "enum_body"},
	FunctionBoundary:  base.NewSet("function_definition"),
	ImportHandler:     scalaImport,
	ClassHook:         scalaClassHook,
	FunctionHook:      scalaFunctionHook,
	ExtraWalk:         scalaExtraWalk,
	CallName:          scalaCallName,
}

// ExtractScala extracts a Scala file.
func ExtractScala(path, root string, src []byte) *model.Extraction {
	return generic.Extract(scalaConfig, path, root, src)
}
