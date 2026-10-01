package langs

import (
	"path/filepath"
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

var cPrimitiveTypes = base.NewSet("primitive_type", "sized_type_specifier", "auto", "placeholder_type_specifier")

func cTypeRefs(n *tsx.Node, generic bool, out *[]typeRef) {
	if n == nil || cPrimitiveTypes.Has(n.Type()) {
		return
	}

	switch n.Type() {
	case "type_identifier":
		if s := n.Text(); s != "" {
			*out = append(*out, typeRef{s, roleOf(generic)})
		}
	case "pointer_declarator", "reference_declarator", "array_declarator",
		"type_qualifier", "type_descriptor", "abstract_pointer_declarator",
		"abstract_reference_declarator", "abstract_array_declarator":
		for _, c := range n.NamedChildren() {
			cTypeRefs(c, generic, out)
		}
	}
}

func cppTypeRefs(n *tsx.Node, generic bool, out *[]typeRef) {
	if n == nil || cPrimitiveTypes.Has(n.Type()) {
		return
	}

	switch n.Type() {
	case "type_identifier":
		if s := n.Text(); s != "" {
			*out = append(*out, typeRef{s, roleOf(generic)})
		}
	case "qualified_identifier":
		cppTypeRefs(n.Field("name"), generic, out)
	case "template_type":
		if nn := n.Field("name"); nn != nil && nn.Text() != "" {
			*out = append(*out, typeRef{nn.Text(), roleOf(generic)})
		}

		if args := n.Field("arguments"); args != nil {
			for _, c := range args.NamedChildren() {
				cppTypeRefs(c, true, out)
			}
		}
	case "type_descriptor", "pointer_declarator", "reference_declarator",
		"array_declarator", "type_qualifier", "abstract_pointer_declarator",
		"abstract_reference_declarator", "abstract_array_declarator":
		for _, c := range n.NamedChildren() {
			cppTypeRefs(c, generic, out)
		}
	}
}

func cFuncName(n *tsx.Node) string {
	if n.Type() == "identifier" {
		return n.Text()
	}

	if d := n.Field("declarator"); d != nil {
		return cFuncName(d)
	}

	if c := n.ChildOfType("identifier"); c != nil {
		return c.Text()
	}

	return ""
}

func cppFuncName(n *tsx.Node) string {
	switch n.Type() {
	case "identifier", "field_identifier", "destructor_name", "operator_name", "qualified_identifier":
		return n.Text()
	}

	if d := n.Field("declarator"); d != nil {
		return cppFuncName(d)
	}

	if c := n.ChildOfType("identifier"); c != nil {
		return c.Text()
	}

	return ""
}

func cImport(x *generic.Ctx, n *tsx.Node) [][2]string {
	b := x.B

	for _, c := range n.Children() {
		t := c.Type()
		if t != "string_literal" && t != "system_lib_string" && t != "string" {
			continue
		}

		raw := strings.Trim(c.Text(), `"<> `)

		if t != "system_lib_string" {
			cand := filepath.Clean(filepath.Join(filepath.Dir(x.Path), raw))
			if isFile(cand) {
				b.AddEdgeCtx(b.FileID, ids.MakeID(cand), "imports", n.Line(), "import")

				break
			}
		}

		mod := strings.SplitN(lastSeg(raw, "/"), ".", 2)[0]
		if mod != "" {
			b.AddEdgeCtx(b.FileID, ids.MakeID(mod), "imports", n.Line(), "import")
		}

		break
	}

	return nil
}

func cFunctionHook(x *generic.Ctx, n *tsx.Node, funcID string, line int) {
	collect := cTypeRefs
	if x.Cfg.Lang == "cpp" {
		collect = cppTypeRefs
	}

	var refs []typeRef
	collect(n.Field("type"), false, &refs)
	emitRefs(x, funcID, line, refs, "return_type")

	d := n.Field("declarator")
	for d != nil && (d.Type() == "pointer_declarator" || d.Type() == "reference_declarator") {
		d = d.Field("declarator")
	}

	if d == nil || d.Type() != "function_declarator" {
		return
	}

	params := d.Field("parameters")
	if params == nil {
		return
	}

	for _, p := range params.Children() {
		if p.Type() != "parameter_declaration" {
			continue
		}

		pt := p.Field("type")
		if pt == nil {
			continue
		}

		refs = nil
		collect(pt, false, &refs)
		emitRefs(x, funcID, line, refs, "parameter_type")
	}
}

func cppClassHook(x *generic.Ctx, n *tsx.Node, classID string, line int) {
	for _, c := range n.Children() {
		if c.Type() != "base_class_clause" {
			continue
		}

		for _, s := range c.Children() {
			var (
				name string
				args *tsx.Node
			)

			switch s.Type() {
			case "type_identifier":
				name = s.Text()
			case "qualified_identifier":
				if t := s.Field("name"); t != nil {
					name = t.Text()
				} else {
					name = s.Text()
				}
			case "template_type":
				if t := s.Field("name"); t != nil {
					name = t.Text()
				} else {
					name = s.Text()
				}

				args = s.Field("arguments")
			default:
				continue
			}

			if name == "" {
				continue
			}

			x.B.AddEdge(classID, x.EnsureNamed(name), "inherits", line)

			if args != nil {
				var refs []typeRef
				for _, a := range args.NamedChildren() {
					cppTypeRefs(a, true, &refs)
				}

				for _, r := range refs {
					x.Ref(classID, x.EnsureNamed(r.name), line, "generic_arg")
				}
			}
		}
	}
}

func cppExtraWalk(x *generic.Ctx, n *tsx.Node, parentClass string) bool {
	// Adapted from Graphify's _cpp_extra_walk (Apache-2.0).
	if n.Type() == "enumerator" && parentClass != "" {
		nn := n.Field("name")
		if nn == nil {
			nn = n.ChildOfType("identifier")
		}
		if nn != nil && nn.Text() != "" {
			id := ids.MakeID(parentClass, nn.Text())
			if x.B.Get(id) == nil {
				x.B.AddNode(id, nn.Text(), n.Line())
				x.B.AddEdge(parentClass, id, "case_of", n.Line())
			}
		}
		return true
	}
	if n.Type() != "field_declaration" || parentClass == "" {
		return false
	}

	var decls []*tsx.Node

	for i, c := range n.Children() {
		if n.FieldNameForChild(i) == "declarator" {
			decls = append(decls, c)
		}
	}

	isMethod := false

	for _, d := range decls {
		if d.Type() == "function_declarator" {
			isMethod = true
		}

		if d.Type() == "pointer_declarator" || d.Type() == "reference_declarator" {
			if d.ChildOfType("function_declarator") != nil {
				isMethod = true
			}
		}
	}

	tn := n.Field("type")
	nested := tn != nil && x.Cfg.ClassTypes.Has(tn.Type()) && tn.Field("body") != nil

	if nested {
		x.Walk(tn, parentClass)
	}

	if !isMethod && !nested && tn != nil {
		var refs []typeRef
		cppTypeRefs(tn, false, &refs)

		for _, r := range refs {
			ctx := "field"
			if r.role == "generic_arg" {
				ctx = "generic_arg"
			}

			x.Ref(parentClass, x.EnsureNamed(r.name), n.Line(), ctx)
		}
	}

	for _, d := range decls {
		if name := cppFuncName(d); name != "" {
			fid := ids.MakeID(parentClass, name)
			x.B.AddNode(fid, name, d.Line())
			x.B.AddEdgeCtx(parentClass, fid, "defines", d.Line(), "field")
		}
	}

	return true
}

func cppCallName(_ *generic.Ctx, n *tsx.Node) (string, bool, string) {
	fn := n.Field("function")
	if fn == nil {
		return "", false, ""
	}

	switch fn.Type() {
	case "identifier":
		return fn.Text(), false, ""
	case "field_expression":
		callee, recv := "", ""
		if f := fn.Field("field"); f != nil {
			callee = f.Text()
		}

		if o := fn.Field("argument"); o != nil {
			switch o.Type() {
			case "identifier":
				recv = o.Text()
			case "this":
				recv = "this"
			}
		}

		return callee, true, recv
	case "qualified_identifier":
		callee, recv := "", ""
		if f := fn.Field("name"); f != nil {
			callee = f.Text()
		}

		if s := fn.Field("scope"); s != nil {
			recv = s.Text()
		}

		return callee, true, recv
	}

	return "", false, ""
}

var cConfig = &generic.Config{
	Lang:                "c",
	Grammar:             "c",
	FunctionTypes:       base.NewSet("function_definition"),
	ImportTypes:         base.NewSet("preproc_include"),
	CallTypes:           base.NewSet("call_expression"),
	CallFunctionField:   "function",
	CallAccessorTypes:   base.NewSet("field_expression"),
	CallAccessorField:   "field",
	FunctionBoundary:    base.NewSet("function_definition"),
	ImportHandler:       cImport,
	ResolveFunctionName: cFuncName,
	FunctionHook:        cFunctionHook,
}

var cppConfig = &generic.Config{
	Lang:                "cpp",
	Grammar:             "cpp",
	ClassTypes:          base.NewSet("class_specifier", "struct_specifier", "enum_specifier"),
	FunctionTypes:       base.NewSet("function_definition"),
	ImportTypes:         base.NewSet("preproc_include"),
	CallTypes:           base.NewSet("call_expression"),
	CallFunctionField:   "function",
	CallAccessorTypes:   base.NewSet("field_expression", "qualified_identifier"),
	CallAccessorField:   "field",
	FunctionBoundary:    base.NewSet("function_definition"),
	ImportHandler:       cImport,
	ResolveFunctionName: cppFuncName,
	FunctionHook:        cFunctionHook,
	ClassHook:           cppClassHook,
	ExtraWalk:           cppExtraWalk,
	CallName:            cppCallName,
	PostProcess:         cppPostProcess,
}

// cppPostProcess records the file's `var -> ClassName` table from local
// declarations in every function body (Graphify's cpp_type_table).
func cppPostProcess(x *generic.Ctx, res *model.Extraction) {
	table := map[string]string{}
	for _, bd := range x.Bodies() {
		cppLocalVarTypes(bd, table)
	}

	if len(table) > 0 {
		res.TypeTable = table
	}
}

// cppDeclaratorName returns the bare variable name of a declarator
// (`*f`, `&r`, `f = Foo()`), or "" for anything else.
//
// Adapted from Graphify's _cpp_declarator_name (Apache-2.0).
func cppDeclaratorName(n *tsx.Node) string {
	switch n.Type() {
	case "identifier":
		return n.Text()
	case "pointer_declarator", "reference_declarator", "init_declarator":
		inner := n.Field("declarator")
		if inner == nil {
			inner = n.ChildOfType("identifier", "pointer_declarator", "reference_declarator")
		}

		if inner != nil {
			return cppDeclaratorName(inner)
		}
	}

	return ""
}

// cppLocalVarTypes collects `var -> ClassName` from single-declarator local
// declarations of a class-like type, without entering nested functions.
//
// Adapted from Graphify's _cpp_local_var_types (Apache-2.0).
func cppLocalVarTypes(body *tsx.Node, table map[string]string) {
	stack := []*tsx.Node{body}

	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if (n.Type() == "function_definition" || n.Type() == "lambda_expression") && n != body {
			continue
		}

		if n.Type() == "declaration" {
			if tn := n.Field("type"); tn != nil && (tn.Type() == "type_identifier" || tn.Type() == "qualified_identifier") {
				tname := strings.TrimSpace(lastSeg(tn.Text(), "::"))

				var decls []*tsx.Node

				for _, c := range n.Children() {
					switch c.Type() {
					case "identifier", "pointer_declarator", "reference_declarator", "init_declarator":
						decls = append(decls, c)
					}
				}

				if tname != "" && upperStart(tname) && len(decls) == 1 {
					if v := cppDeclaratorName(decls[0]); v != "" {
						if _, ok := table[v]; !ok {
							table[v] = tname
						}
					}
				}
			}
		}

		stack = append(stack, n.Children()...)
	}
}

// ExtractC extracts a C file.
func ExtractC(path, root string, src []byte) *model.Extraction {
	return generic.Extract(cConfig, path, root, src)
}

// ExtractCPP extracts a C++ (or CUDA/Metal) file.
func ExtractCPP(path, root string, src []byte) *model.Extraction {
	return generic.Extract(cppConfig, path, root, src)
}

var (
	objcMarkers = []string{"@interface", "@protocol", "@implementation", "@import", "#import"}
	cppMarkers  = []string{"class ", "namespace ", "template", "::", "public:", "private:", "protected:"}
)

func sniff(src []byte, markers []string) bool {
	head := src
	if len(head) > 256*1024 {
		head = head[:256*1024]
	}

	for _, m := range markers {
		if strings.Contains(string(head), m) {
			return true
		}
	}

	return false
}

// ExtractHeader routes a .h file to the ObjC, C++ or C extractor by content.
func ExtractHeader(path, root string, src []byte) *model.Extraction {
	if sniff(src, objcMarkers) {
		return ExtractObjC(path, root, src)
	}

	if sniff(src, cppMarkers) {
		return ExtractCPP(path, root, src)
	}

	return ExtractC(path, root, src)
}

// ExtractDotM routes .m files: Objective-C when it has ObjC directives,
// otherwise nothing (MATLAB/Octave).
func ExtractDotM(path, root string, src []byte) *model.Extraction {
	if sniff(src, objcMarkers) {
		return ExtractObjC(path, root, src)
	}

	return &model.Extraction{Skipped: true}
}
