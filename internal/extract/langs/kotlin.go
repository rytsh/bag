package langs

import (
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

var kotlinBuiltinTypes = base.NewSet(
	"Any", "Unit", "Nothing", "Boolean", "Byte", "Short", "Int", "Long",
	"Float", "Double", "Char", "String", "CharSequence", "Number",
	"Comparable", "Enum", "Annotation", "Pair", "Triple", "Lazy",
	"Function",
	"Throwable", "Exception", "RuntimeException", "Error",
	"IllegalArgumentException", "IllegalStateException", "NullPointerException",
	"IndexOutOfBoundsException", "ClassCastException", "NumberFormatException",
	"ArithmeticException", "UnsupportedOperationException",
	"NoSuchElementException", "ConcurrentModificationException",
	"StackOverflowError", "OutOfMemoryError", "AssertionError",
	"InterruptedException",
	"Array", "List", "MutableList", "ArrayList", "Set", "MutableSet",
	"HashSet", "LinkedHashSet", "Map", "MutableMap", "HashMap",
	"LinkedHashMap", "Collection", "MutableCollection", "Iterable",
	"MutableIterable", "Iterator", "MutableIterator", "ListIterator",
	"MutableListIterator", "Sequence", "Comparator",
	"Regex", "MatchResult", "StringBuilder",
)

func ktOK(s string) bool {
	return s != "" && !kotlinBuiltinTypes.Has(s) && !javaBuiltinTypes.Has(s)
}

func ktUserTypeName(n *tsx.Node) string {
	if n == nil {
		return ""
	}

	name := ""

	for _, c := range n.Children() {
		switch c.Type() {
		case "type_identifier", "identifier":
			if t := c.Text(); t != "" {
				name = t
			}
		case "simple_user_type":
			if s := c.ChildOfType("identifier", "type_identifier"); s != nil && s.Text() != "" {
				name = s.Text()
			}
		}
	}

	return name
}

func ktTypeRefs(n *tsx.Node, generic bool, out *[]typeRef) {
	if n == nil {
		return
	}

	switch n.Type() {
	case "integral_literal", "boolean_literal":
		return
	case "user_type":
		for _, c := range n.Children() {
			if c.Type() == "identifier" || c.Type() == "type_identifier" {
				if ktOK(c.Text()) {
					*out = append(*out, typeRef{c.Text(), roleOf(generic)})
				}

				break
			}

			if c.Type() == "simple_user_type" {
				if s := c.ChildOfType("identifier", "type_identifier"); s != nil && ktOK(s.Text()) {
					*out = append(*out, typeRef{s.Text(), roleOf(generic)})
				}

				break
			}
		}

		for _, c := range n.Children() {
			if c.Type() == "type_arguments" {
				for _, a := range c.Children() {
					if a.Type() == "type_projection" {
						for _, s := range a.NamedChildren() {
							ktTypeRefs(s, true, out)
						}
					} else if a.IsNamed() {
						ktTypeRefs(a, true, out)
					}
				}
			}
		}

		return
	case "identifier", "type_identifier":
		if ktOK(n.Text()) {
			*out = append(*out, typeRef{n.Text(), roleOf(generic)})
		}

		return
	case "nullable_type", "parenthesized_type", "type_reference":
		for _, c := range n.NamedChildren() {
			ktTypeRefs(c, generic, out)
		}

		return
	}

	if n.IsNamed() {
		for _, c := range n.NamedChildren() {
			ktTypeRefs(c, generic, out)
		}
	}
}

func ktAnnotations(decl *tsx.Node) []string {
	mods := decl.ChildOfType("modifiers")
	if mods == nil {
		return nil
	}

	var out []string

	for _, a := range mods.Children() {
		if a.Type() != "annotation" {
			continue
		}

		for _, s := range a.Children() {
			var ut *tsx.Node

			switch s.Type() {
			case "user_type":
				ut = s
			case "constructor_invocation":
				ut = s.ChildOfType("user_type")
			}

			if name := ktUserTypeName(ut); name != "" {
				out = append(out, name)
			}
		}
	}

	return out
}

func ktImport(x *generic.Ctx, n *tsx.Node) [][2]string {
	pn := n.Field("path")
	if pn == nil {
		pn = n.ChildOfType("qualified_identifier")
	}

	raw := ""
	if pn != nil {
		raw = strings.TrimSpace(pn.Text())
	} else if id := n.ChildOfType("identifier"); id != nil {
		raw = strings.TrimSpace(id.Text())
	}

	if raw == "" || strings.HasSuffix(raw, ".*") || raw == "*" {
		return nil
	}

	for _, c := range n.Children() {
		if c.Type() == "*" || c.Type() == "wildcard_import" {
			return nil
		}
	}

	alias := ""

	if ia := n.ChildOfType("import_alias"); ia != nil {
		if id := ia.ChildOfType("type_identifier", "simple_identifier", "identifier"); id != nil {
			alias = id.Text()
		}
	}

	mod := strings.TrimSpace(lastSeg(raw, "."))
	if mod == "" {
		return nil
	}

	e := x.B.AddEdgeCtx(x.B.FileID, ids.MakeID(mod), "imports", n.Line(), "import")
	e.Metadata = map[string]any{"target_fqn": raw}

	if alias != "" {
		e.Metadata["alias"] = alias
	}

	return nil
}

func ktEmitAnnotations(x *generic.Ctx, decl *tsx.Node, owner string, line int) {
	seen := map[string]bool{}

	for _, a := range ktAnnotations(decl) {
		tgt := x.EnsureNamed(a)
		if tgt != owner && !seen[tgt] {
			x.B.AddEdgeCtx(owner, tgt, "references", line, "attribute")
			seen[tgt] = true
		}
	}
}

func ktClassHook(x *generic.Ctx, n *tsx.Node, classID string, line int) {
	for _, c := range n.Children() {
		if c.Type() != "delegation_specifiers" && c.Type() != "delegation_specifier" {
			continue
		}

		specs := c.Children()
		if c.Type() == "delegation_specifier" {
			specs = []*tsx.Node{c}
		}

		for _, spec := range specs {
			if spec.Type() != "delegation_specifier" {
				continue
			}

			rel := "implements"

			var ut *tsx.Node

			for _, s := range spec.Children() {
				switch s.Type() {
				case "constructor_invocation":
					rel = "inherits"
					ut = s.ChildOfType("user_type")
				case "user_type":
					ut = s
				case "explicit_delegation":
					ut = s.ChildOfType("user_type")
				default:
					continue
				}

				break
			}

			name := ktUserTypeName(ut)
			if name == "" {
				continue
			}

			x.B.AddEdge(classID, x.EnsureNamed(name), rel, line)

			for _, ta := range ut.Children() {
				if ta.Type() != "type_arguments" {
					continue
				}

				for _, a := range ta.Children() {
					if a.Type() != "type_projection" {
						continue
					}

					for _, in := range a.NamedChildren() {
						var refs []typeRef
						ktTypeRefs(in, true, &refs)

						for _, r := range refs {
							x.B.AddEdgeCtx(classID, x.EnsureNamed(r.name), "references", line, "generic_arg")
						}
					}
				}
			}
		}
	}

	ktEmitAnnotations(x, n, classID, line)

	for _, c := range n.Children() {
		if c.Type() != "primary_constructor" {
			continue
		}

		params := c.Children()
		if cp := c.ChildOfType("class_parameters"); cp != nil {
			params = cp.Children()
		}

		for _, p := range params {
			if p.Type() != "class_parameter" {
				continue
			}

			hasValVar := false

			for _, s := range p.Children() {
				t := s.Type()
				if t == "val" || t == "var" || (t == "binding_pattern_kind") {
					hasValVar = true
				}
			}

			if !hasValVar {
				continue
			}

			pt := p.ChildOfType("user_type", "nullable_type", "type_reference")
			if pt == nil {
				continue
			}

			var refs []typeRef
			ktTypeRefs(pt, false, &refs)

			for _, r := range refs {
				ctx := "field"
				if r.role == "generic_arg" {
					ctx = "generic_arg"
				}

				x.Ref(classID, x.EnsureNamed(r.name), p.Line(), ctx)
			}

			ktEmitAnnotations(x, p, classID, p.Line())
		}
	}
}

func ktFunctionHook(x *generic.Ctx, n *tsx.Node, funcID string, line int) {
	if params := n.ChildOfType("function_value_parameters"); params != nil {
		for _, p := range params.Children() {
			if p.Type() != "parameter" {
				continue
			}

			var refs []typeRef
			ktTypeRefs(p.ChildOfType("user_type", "nullable_type", "type_reference"), false, &refs)
			emitRefs(x, funcID, line, refs, "parameter_type")
		}
	}

	sawParams, sawColon := false, false

	for _, c := range n.Children() {
		if c.Type() == "function_value_parameters" {
			sawParams = true

			continue
		}

		if sawParams && c.Type() == ":" {
			sawColon = true

			continue
		}

		if sawColon && c.IsNamed() {
			var refs []typeRef
			ktTypeRefs(c, false, &refs)
			emitRefs(x, funcID, line, refs, "return_type")

			break
		}
	}

	ktEmitAnnotations(x, n, funcID, line)
}

func ktExtraWalk(x *generic.Ctx, n *tsx.Node, parentClass string) bool {
	switch n.Type() {
	case "enum_entry":
		if parentClass == "" {
			return false
		}

		nn := n.ChildOfType("simple_identifier", "identifier")
		if nn == nil {
			return true
		}

		cid := ids.MakeID(parentClass, nn.Text())
		x.B.AddNode(cid, nn.Text(), n.Line())
		x.B.AddEdge(parentClass, cid, "case_of", n.Line())

		if cb := n.ChildOfType("class_body"); cb != nil {
			for _, m := range cb.Children() {
				x.Walk(m, cid)
			}
		}

		return true
	case "companion_object":
		for _, c := range n.Children() {
			if c.Type() == "class_body" {
				for _, m := range c.Children() {
					x.Walk(m, parentClass)
				}
			} else {
				x.Walk(c, parentClass)
			}
		}

		return true
	case "property_declaration":
		if parentClass == "" {
			owner := x.B.FileID
			sawEq := false

			for _, c := range n.Children() {
				if !c.IsNamed() {
					sawEq = sawEq || c.Type() == "="

					continue
				}

				if sawEq {
					x.AddInitializer(owner, c)
				}
			}

			return true
		}

		var tn *tsx.Node

		for _, c := range n.Children() {
			if c.Type() == "variable_declaration" {
				tn = c.ChildOfType("user_type", "nullable_type", "type_reference")
			} else if c.Type() == "user_type" || c.Type() == "nullable_type" || c.Type() == "type_reference" {
				tn = c
			}

			if tn != nil {
				break
			}
		}

		if tn != nil {
			var refs []typeRef
			ktTypeRefs(tn, false, &refs)

			for _, r := range refs {
				ctx := "field"
				if r.role == "generic_arg" {
					ctx = "generic_arg"
				}

				x.Ref(parentClass, x.EnsureNamed(r.name), n.Line(), ctx)
			}
		}
		// Adapted from Graphify's _extract_generic Kotlin property branch
		// (Apache-2.0): annotations also apply to inferred-type properties.
		ktEmitAnnotations(x, n, parentClass, n.Line())

		owner := parentClass
		sawEq := false

		for _, c := range n.Children() {
			if !c.IsNamed() {
				sawEq = sawEq || c.Type() == "="

				continue
			}

			if sawEq {
				x.AddInitializer(owner, c)
			} else if c.Type() == "property_delegate" {
				for _, s := range c.NamedChildren() {
					x.AddInitializer(owner, s)
				}
			}
		}

		return true
	}

	return false
}

func ktCallName(_ *generic.Ctx, n *tsx.Node) (string, bool, string) {
	ch := n.Children()
	if len(ch) == 0 {
		return "", false, ""
	}

	first := ch[0]

	switch first.Type() {
	case "simple_identifier", "identifier":
		return first.Text(), false, ""
	case "navigation_expression":
		callee := ""

		fc := first.Children()
		for i := len(fc) - 1; i >= 0; i-- {
			c := fc[i]
			if c.Type() == "simple_identifier" || c.Type() == "identifier" {
				callee = c.Text()

				break
			}

			if c.Type() == "navigation_suffix" {
				if s := c.ChildOfType("simple_identifier", "identifier"); s != nil {
					callee = s.Text()

					break
				}
			}
		}

		return callee, true, ""
	}

	return "", false, ""
}

var kotlinConfig = &generic.Config{
	Lang:              "kotlin",
	Grammar:           "kotlin",
	ClassTypes:        base.NewSet("class_declaration", "object_declaration"),
	FunctionTypes:     base.NewSet("function_declaration"),
	ImportTypes:       base.NewSet("import_header", "import"),
	CallTypes:         base.NewSet("call_expression"),
	CallAccessorTypes: base.NewSet("navigation_expression"),
	NameFallback:      []string{"simple_identifier", "identifier", "type_identifier"},
	BodyFallback:      []string{"function_body", "class_body", "enum_class_body"},
	FunctionBoundary:  base.NewSet("function_declaration"),
	ImportHandler:     ktImport,
	ClassHook:         ktClassHook,
	FunctionHook:      ktFunctionHook,
	ExtraWalk:         ktExtraWalk,
	CallName:          ktCallName,
	DecorateRawCall:   ktDecorateRawCall,
	PostProcess:       ktPostProcess,
}

// ExtractKotlin extracts a Kotlin file.
func ExtractKotlin(path, root string, src []byte) *model.Extraction {
	return generic.Extract(kotlinConfig, path, root, src)
}
