package langs

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// ---------------- Scala ----------------

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
	var parts []string

	for i, c := range n.Children() {
		if n.FieldNameForChild(i) == "path" && c.Type() == "identifier" {
			parts = append(parts, c.Text())
		}
	}

	raw := strings.Join(parts, ".")
	if raw == "" {
		if c := n.ChildOfType("stable_id", "identifier"); c != nil {
			raw = c.Text()
		}
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
	ClassTypes:        base.NewSet("class_definition", "object_definition", "trait_definition"),
	FunctionTypes:     base.NewSet("function_definition"),
	ImportTypes:       base.NewSet("import_declaration"),
	CallTypes:         base.NewSet("call_expression"),
	CallAccessorTypes: base.NewSet("field_expression"),
	CallAccessorField: "field",
	NameFallback:      []string{"identifier"},
	BodyFallback:      []string{"template_body"},
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

// ---------------- PHP ----------------

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

// ---------------- Ruby ----------------

func rubySanitize(name string) string {
	switch {
	case name == "":
		return name
	case strings.HasSuffix(name, "!"):
		return name[:len(name)-1] + "_bang"
	case strings.HasSuffix(name, "?"):
		return name[:len(name)-1] + "_pred"
	case strings.HasSuffix(name, "="):
		return name[:len(name)-1] + "_eq"
	}

	return name
}

func rubyConstFullName(n *tsx.Node) string {
	if n == nil {
		return ""
	}

	return strings.TrimPrefix(strings.TrimSpace(n.Text()), "::")
}

func rubyClassHook(x *generic.Ctx, n *tsx.Node, classID string, line int) {
	if sup := n.Field("superclass"); sup != nil {
		for _, s := range sup.Children() {
			base := ""

			switch s.Type() {
			case "constant":
				base = s.Text()
			case "scope_resolution":
				var consts []*tsx.Node

				for _, c := range s.Children() {
					if c.Type() == "constant" {
						consts = append(consts, c)
					}
				}

				if len(consts) > 0 {
					base = consts[len(consts)-1].Text()
				}
			default:
				continue
			}

			if base != "" {
				x.B.AddEdge(classID, x.EnsureNamed(base), "inherits", line)
			}

			break
		}
	}

	bd := x.Cfg.FindBody(n)
	if bd == nil {
		return
	}

	for _, st := range bd.Children() {
		if st.Type() != "call" || st.Field("receiver") != nil {
			continue
		}

		m := st.Field("method")
		if m == nil || (m.Text() != "include" && m.Text() != "extend" && m.Text() != "prepend") {
			continue
		}

		args := st.Field("arguments")
		if args == nil {
			continue
		}

		for _, a := range args.Children() {
			if a.Type() != "constant" && a.Type() != "scope_resolution" {
				continue
			}

			if mod := rubyConstFullName(a); mod != "" {
				x.B.RawCalls = append(x.B.RawCalls, &model.RawCall{
					CallerID: classID, Callee: mod, Language: "mixin",
					SourceFile: x.Path, SourceLocation: base.Loc(st.Line()),
				})
			}
		}
	}
}

func rubyCallName(_ *generic.Ctx, n *tsx.Node) (string, bool, string) {
	callee := ""
	if m := n.Field("method"); m != nil {
		callee = m.Text()
	}

	recv := n.Field("receiver")
	if recv == nil {
		return callee, false, ""
	}

	switch recv.Type() {
	case "identifier", "constant":
		return callee, true, recv.Text()
	case "scope_resolution":
		return callee, true, rubyConstFullName(recv)
	}

	return callee, true, ""
}

var rubyConfig = &generic.Config{
	Lang:             "ruby",
	Grammar:          "ruby",
	ClassTypes:       base.NewSet("class", "module"),
	FunctionTypes:    base.NewSet("method", "singleton_method"),
	CallTypes:        base.NewSet("call"),
	NameFallback:     []string{"constant", "scope_resolution", "identifier"},
	BodyFallback:     []string{"body_statement"},
	FunctionBoundary: base.NewSet("method", "singleton_method"),
	SanitizeName:     rubySanitize,
	ClassHook:        rubyClassHook,
	CallName:         rubyCallName,
}

// ExtractRuby extracts a Ruby file.
func ExtractRuby(path, root string, src []byte) *model.Extraction {
	return generic.Extract(rubyConfig, path, root, src)
}

// ---------------- Lua ----------------

var luaRequireRe = regexp.MustCompile(`require\s*[\('"]\s*['"]?([^'")\s]+)`)

func luaImportTarget(raw, fromPath string) string {
	rel := strings.ReplaceAll(raw, ".", "/")
	probe := filepath.Dir(fromPath)

	for range 6 {
		for _, sfx := range []string{".lua", ".luau"} {
			if c := filepath.Join(probe, rel+sfx); isFile(c) {
				return ids.MakeID(c)
			}
		}

		for _, sfx := range []string{".lua", ".luau"} {
			if c := filepath.Join(probe, rel, "init"+sfx); isFile(c) {
				return ids.MakeID(c)
			}
		}

		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}

		probe = parent
	}

	return ids.MakeID(raw)
}

func luaImport(x *generic.Ctx, n *tsx.Node) [][2]string {
	m := luaRequireRe.FindStringSubmatch(n.Text())
	if m == nil || m[1] == "" {
		return nil
	}

	e := x.B.AddEdgeCtx(x.B.FileID, luaImportTarget(m[1], x.Path), "imports", n.Line(), "import")
	e.SourceLocation = itoa(n.Line())
	e.ConfidenceScore = model.Score(1.0)

	return nil
}

func luaIsRequire(n *tsx.Node) bool {
	if n.Type() != "function_call" {
		return false
	}

	nn := n.Field("name")

	return nn != nil && nn.Text() == "require"
}

func luaExtraWalk(x *generic.Ctx, n *tsx.Node, _ string) bool {
	if luaIsRequire(n) {
		luaImport(x, n)

		return true
	}

	return false
}

var luaConfig = &generic.Config{
	Lang:              "lua",
	Grammar:           "lua",
	FunctionTypes:     base.NewSet("function_declaration"),
	ImportTypes:       base.NewSet("variable_declaration"),
	CallTypes:         base.NewSet("function_call"),
	CallFunctionField: "name",
	CallAccessorTypes: base.NewSet("method_index_expression"),
	CallAccessorField: "name",
	NameFallback:      []string{"identifier", "method_index_expression"},
	BodyFallback:      []string{"block"},
	FunctionBoundary:  base.NewSet("function_declaration"),
	ImportHandler:     luaImport,
	ExtraWalk:         luaExtraWalk,
}

// ExtractLua extracts a Lua file.
func ExtractLua(path, root string, src []byte) *model.Extraction {
	return generic.Extract(luaConfig, path, root, src)
}

// ExtractLuau extracts a Luau file using the Luau grammar.
func ExtractLuau(path, root string, src []byte) *model.Extraction {
	cfg := *luaConfig
	cfg.Grammar = "luau"

	return generic.Extract(&cfg, path, root, src)
}

// ---------------- Swift ----------------

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

func swiftFunctionHook(x *generic.Ctx, n *tsx.Node, funcID string, line int) {
	for _, p := range n.Children() {
		if p.Type() != "parameter" {
			continue
		}

		var refs []typeRef
		swiftTypeRefs(p.Field("type"), false, &refs)
		emitRefs(x, funcID, line, refs, "parameter_type")
	}

	if rt := n.Field("return_type"); rt != nil {
		var refs []typeRef
		swiftTypeRefs(rt, false, &refs)
		emitRefs(x, funcID, line, refs, "return_type")
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

		if ta := n.ChildOfType("type_annotation"); ta != nil {
			var refs []typeRef
			swiftTypeRefs(ta, false, &refs)

			for _, r := range refs {
				ctx := "field"
				if r.role == "generic_arg" {
					ctx = "generic_arg"
				}

				x.Ref(parentClass, x.EnsureNamed(r.name), line, ctx)
			}
		}

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
	ImportHandler: swiftImport,
	ClassHook:     swiftClassHook,
	FunctionHook:  swiftFunctionHook,
	ExtraWalk:     swiftExtraWalk,
	CallName:      swiftCallName,
	PreScan:       swiftPreScan,
}

// ExtractSwift extracts a Swift file.
func ExtractSwift(path, root string, src []byte) *model.Extraction {
	return generic.Extract(swiftConfig, path, root, src)
}
