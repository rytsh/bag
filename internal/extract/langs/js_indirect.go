package langs

import (
	"path/filepath"
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
)

// JS/TS indirect dispatch: a function referenced BY NAME (a call argument,
// an object/array literal value) is an INFERRED indirect_call dependency.
//
// Adapted from Graphify's _emit_indirect_ref / _js_local_bound_names /
// _js_module_bound_names / _js_external_import_names (Apache-2.0).

var jsScopeBoundary = base.NewSet("function_declaration", "function_expression", "function", "arrow_function",
	"method_definition", "class_declaration", "class", "generator_function", "generator_function_declaration")

// jsPatternIdents collects the names a parameter or declarator pattern binds.
func jsPatternIdents(n *tsx.Node, out map[string]bool) {
	switch n.Type() {
	case "identifier", "shorthand_property_identifier_pattern":
		out[n.Text()] = true

		return
	case "type_annotation":
		return
	case "assignment_pattern":
		if l := n.Field("left"); l != nil {
			jsPatternIdents(l, out)
		}

		return
	case "pair_pattern":
		if v := n.Field("value"); v != nil {
			jsPatternIdents(v, out)
		}

		return
	}

	for _, c := range n.NamedChildren() {
		jsPatternIdents(c, out)
	}
}

// jsLocalNames returns a function's parameters plus its function-scoped
// `var` bindings; let/const are block-scoped (see jsScopeLocals).
func jsLocalNames(fn *tsx.Node) map[string]bool {
	out := map[string]bool{}

	if p := fn.Field("parameters"); p != nil {
		jsPatternIdents(p, out)
	}

	if p := fn.Field("parameter"); p != nil {
		jsPatternIdents(p, out)
	}

	var walk func(n *tsx.Node)
	walk = func(n *tsx.Node) {
		for _, c := range n.Children() {
			if jsScopeBoundary.Has(c.Type()) {
				continue
			}

			switch {
			case c.Type() == "variable_declarator" && n.Type() == "variable_declaration":
				if nn := c.Field("name"); nn != nil {
					jsPatternIdents(nn, out)
				}
			case c.Type() == "for_in_statement" && c.ChildOfType("var") != nil:
				if l := c.Field("left"); l != nil {
					jsPatternIdents(l, out)
				}
			}

			walk(c)
		}
	}

	if bd := fn.Field("body"); bd != nil {
		walk(bd)
	}

	return out
}

// jsScopeLocals returns names bound for exactly n's subtree: an untracked
// closure's own locals, block-level let/const, catch and for-in bindings.
func jsScopeLocals(n *tsx.Node) map[string]bool {
	switch n.Type() {
	case "arrow_function", "function_expression", "function_declaration",
		"generator_function_declaration", "generator_function":
		return jsLocalNames(n)
	case "catch_clause":
		if p := n.Field("parameter"); p != nil {
			out := map[string]bool{}
			jsPatternIdents(p, out)

			return out
		}
	case "statement_block", "for_statement":
		var out map[string]bool

		for _, c := range n.Children() {
			if c.Type() != "lexical_declaration" {
				continue
			}

			for _, d := range c.Children() {
				if d.Type() != "variable_declarator" {
					continue
				}

				if nn := d.Field("name"); nn != nil {
					if out == nil {
						out = map[string]bool{}
					}

					jsPatternIdents(nn, out)
				}
			}
		}

		return out
	case "for_in_statement":
		if l := n.Field("left"); l != nil {
			out := map[string]bool{}
			jsPatternIdents(l, out)

			return out
		}
	}

	return nil
}

// jsModuleBound returns module-scope names bound to non-function data.
func jsModuleBound(root *tsx.Node) map[string]bool {
	out := map[string]bool{}

	var walk func(n *tsx.Node)
	walk = func(n *tsx.Node) {
		for _, c := range n.Children() {
			if jsScopeBoundary.Has(c.Type()) {
				continue
			}

			if c.Type() == "variable_declarator" {
				if v := jsDeclValue(c.Field("value")); v == nil || !jsFunctionValueTypes.Has(v.Type()) {
					if nn := c.Field("name"); nn != nil {
						jsPatternIdents(nn, out)
					}
				}
			}

			walk(c)
		}
	}

	walk(root)

	return out
}

// jsExternalImportNames returns names bound by imports of modules outside
// the corpus; they shadow same-named callables everywhere in the file.
func jsExternalImportNames(root *tsx.Node, path string) map[string]bool {
	out := map[string]bool{}

	clause := func(cl *tsx.Node) {
		for _, c := range cl.Children() {
			switch c.Type() {
			case "identifier":
				out[c.Text()] = true
			case "namespace_import":
				for _, id := range c.Children() {
					if id.Type() == "identifier" {
						out[id.Text()] = true
					}
				}
			case "named_imports":
				for _, spec := range c.Children() {
					if spec.Type() != "import_specifier" {
						continue
					}

					var last string

					for _, g := range spec.Children() {
						if g.Type() == "identifier" {
							last = g.Text()
						}
					}

					if last != "" {
						out[last] = true
					}
				}
			}
		}
	}

	var walk func(n *tsx.Node)
	walk = func(n *tsx.Node) {
		for _, c := range n.Children() {
			if c.Type() == "import_statement" {
				if sn := c.Field("source"); sn != nil && jsImportBindsExternal(strings.Trim(sn.Text(), "\"'`"), path) {
					for _, cc := range c.Children() {
						if cc.Type() == "import_clause" {
							clause(cc)
						}
					}
				}

				continue
			}

			walk(c)
		}
	}

	walk(root)

	return out
}

func jsImportBindsExternal(raw, path string) bool {
	_, resolved, ok := resolveJSTarget(raw, path)
	if !ok {
		return false
	}

	if resolved == "" {
		return true
	}

	for _, seg := range strings.Split(filepath.ToSlash(resolved), "/") {
		if seg == "node_modules" {
			return true
		}
	}

	return false
}

func jsDispatchIdents(coll *tsx.Node) []*tsx.Node {
	var out []*tsx.Node

	if coll.Type() == "object" {
		for _, c := range coll.Children() {
			switch c.Type() {
			case "pair":
				if v := c.Field("value"); v != nil && v.Type() == "identifier" {
					out = append(out, v)
				}
			case "shorthand_property_identifier":
				out = append(out, c)
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

func jsArgIdents(call *tsx.Node) []*tsx.Node {
	args := call.Field("arguments")
	if args == nil {
		return nil
	}

	var out []*tsx.Node

	for _, a := range args.Children() {
		if a.Type() == "identifier" {
			out = append(out, a)
		}
	}

	return out
}

func jsIndirectRefs(_ *generic.Ctx, n *tsx.Node) []generic.IndirectRef {
	switch n.Type() {
	case "call_expression", "new_expression":
		if jsIsDynamicImport(n) {
			return nil
		}

		return refsOf(jsArgIdents(n), "argument")
	case "object", "array":
		return refsOf(jsDispatchIdents(n), "collection")
	}

	return nil
}

func jsIsDynamicImport(n *tsx.Node) bool {
	fn := n.Field("function")

	return fn != nil && fn.Type() == "import"
}

func jsModuleIndirect(x *generic.Ctx) {
	bound := jsModuleBound(x.Tree.Root)
	file := x.B.FileID

	var scan func(n *tsx.Node)
	scan = func(n *tsx.Node) {
		if jsScopeBoundary.Has(n.Type()) {
			return
		}

		switch n.Type() {
		case "object", "array":
			for _, r := range refsOf(jsDispatchIdents(n), "collection") {
				x.EmitIndirect(r, file, bound)
			}
		case "call_expression", "new_expression":
			for _, r := range refsOf(jsArgIdents(n), "argument") {
				x.EmitIndirect(r, file, bound)
			}
		}

		for _, c := range n.Children() {
			scan(c)
		}
	}

	scan(x.Tree.Root)
}
