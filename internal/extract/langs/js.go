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

var (
	jsFunctionValueTypes = base.NewSet("arrow_function", "function_expression", "function", "generator_function")
	jsxElementTypes      = base.NewSet("jsx_opening_element", "jsx_self_closing_element")
)

// jsTargetID converts resolveJSTarget output to a node id.
func jsTargetID(target string) string {
	if strings.HasPrefix(target, "ref\x00") {
		return ids.MakeID("ref", target[4:])
	}

	return ids.MakeID(target)
}

func jsStringArg(n *tsx.Node) string {
	return strings.Trim(n.Text(), "'\"` ")
}

func jsImport(x *generic.Ctx, n *tsx.Node) [][2]string {
	b := x.B
	reexport := n.Type() == "export_statement"

	if reexport && n.ChildOfType("string") == nil {
		return nil
	}

	typeOnly := false

	for _, c := range n.Children() {
		if c.Type() == "type" && !c.IsNamed() {
			typeOnly = true
		}
	}

	var modStr *tsx.Node

	for _, c := range n.Children() {
		if c.Type() == "string" {
			modStr = c

			break
		}

		if c.Type() == "import_require_clause" {
			modStr = c.ChildOfType("string")

			break
		}
	}

	resolved := ""

	if modStr != nil {
		raw := jsStringArg(modStr)
		if target, rp, ok := resolveJSTarget(raw, x.Path); ok {
			tgt := jsTargetID(target)
			if rp != "" && !isFile(rp) {
				tgt, rp = ids.MakeID("ref", raw), ""
			}

			ctx := "import"
			if reexport {
				ctx = "re-export"
			}

			e := b.AddEdgeCtx(b.FileID, tgt, "imports_from", n.Line(), ctx)
			if typeOnly {
				e.Extra = map[string]any{"type_only": true}
			}

			resolved = rp
		}
	}

	if resolved == "" {
		return nil
	}

	targetStem := base.FileStem(resolved)
	line := n.Line()

	if reexport {
		for _, c := range n.Children() {
			if c.Type() != "export_clause" {
				continue
			}

			for _, spec := range c.Children() {
				if spec.Type() != "export_specifier" {
					continue
				}

				nn := spec.Field("name")
				if nn == nil || nn.Text() == "default" {
					continue
				}

				e := b.AddEdgeCtx(b.FileID, ids.MakeID(targetStem, nn.Text()), "re_exports", line, "re-export")
				if typeOnly {
					e.Extra = map[string]any{"type_only": true}
				}
			}
		}

		return nil
	}

	for _, c := range n.Children() {
		if c.Type() != "import_clause" {
			continue
		}

		for _, sub := range c.Children() {
			if sub.Type() != "named_imports" {
				continue
			}

			for _, spec := range sub.Children() {
				if spec.Type() != "import_specifier" {
					continue
				}

				if nn := spec.Field("name"); nn != nil {
					b.AddEdgeCtx(b.FileID, ids.MakeID(targetStem, nn.Text()), "imports", line, "import")
				}
			}
		}
	}

	return nil
}

func findRequireCall(v *tsx.Node) *tsx.Node {
	if v == nil {
		return nil
	}

	switch v.Type() {
	case "call_expression":
		if fn := v.Field("function"); fn != nil && fn.Type() == "identifier" {
			return v
		}
	case "member_expression":
		return findRequireCall(v.Field("object"))
	}

	return nil
}

func jsRequireImports(x *generic.Ctx, n *tsx.Node, importer string) bool {
	b := x.B
	found := false

	for _, c := range n.Children() {
		if c.Type() != "variable_declarator" {
			continue
		}

		value := c.Field("value")

		call := findRequireCall(value)
		if call == nil {
			continue
		}

		if fn := call.Field("function"); fn == nil || fn.Text() != "require" {
			continue
		}

		args := call.Field("arguments")
		if args == nil {
			continue
		}

		raw := ""
		if s := args.ChildOfType("string"); s != nil {
			raw = jsStringArg(s)
		}

		if raw == "" {
			continue
		}

		target, rp, ok := resolveJSTarget(raw, x.Path)
		if !ok {
			continue
		}

		line := n.Line()
		b.AddEdgeCtx(importer, jsTargetID(target), "imports_from", line, "import")

		found = true

		if rp == "" {
			continue
		}

		stem := base.FileStem(rp)

		var syms []string

		name := c.Field("name")

		switch {
		case name != nil && name.Type() == "object_pattern":
			for _, p := range name.Children() {
				switch p.Type() {
				case "shorthand_property_identifier_pattern":
					syms = append(syms, p.Text())
				case "pair_pattern":
					if k := p.Field("key"); k != nil {
						syms = append(syms, k.Text())
					}
				}
			}
		case value != nil && value.Type() == "member_expression":
			if p := value.Field("property"); p != nil {
				syms = append(syms, p.Text())
			}
		}

		for _, s := range syms {
			b.AddEdgeCtx(importer, ids.MakeID(stem, s), "imports", line, "import")
		}
	}

	return found
}

func jsTopmostClosures(n *tsx.Node, out *[]*tsx.Node) {
	for _, c := range n.Children() {
		if jsFunctionValueTypes.Has(c.Type()) {
			*out = append(*out, c)

			continue
		}

		jsTopmostClosures(c, out)
	}
}

func jsNestedFunctions(x *generic.Ctx, container *tsx.Node, parent string) {
	if container == nil {
		return
	}

	for _, c := range container.Children() {
		switch {
		case c.Type() == "function_declaration" || c.Type() == "generator_function_declaration":
			nn := c.Field("name")
			if nn == nil || ids.NormalizeID(nn.Text()) == "" {
				continue
			}

			id := ids.MakeID(parent, nn.Text())
			x.B.AddNode(id, nn.Text()+"()", c.Line())
			x.B.AddEdge(parent, id, "contains", c.Line())
			x.MarkCallable(id, false)

			if bd := c.Field("body"); bd != nil {
				x.AddBody(id, bd)
				jsNestedFunctions(x, bd, id)
			}
		case jsFunctionValueTypes.Has(c.Type()):
			jsNestedFunctions(x, c.Field("body"), parent)
		default:
			jsNestedFunctions(x, c, parent)
		}
	}
}

func jsMemberAssignTarget(left *tsx.Node) (kind, owner, member string, ok bool) {
	if left == nil || left.Type() != "member_expression" {
		return "", "", "", false
	}

	obj, prop := left.Field("object"), left.Field("property")
	if obj == nil || prop == nil {
		return "", "", "", false
	}

	member = prop.Text()

	switch {
	case obj.Text() == "exports" || obj.Text() == "module.exports":
		return "exports", "", member, true
	case obj.Type() == "member_expression":
		o2, p2 := obj.Field("object"), obj.Field("property")
		if o2 != nil && p2 != nil && p2.Text() == "prototype" && o2.Type() == "identifier" {
			return "prototype", o2.Text(), member, true
		}
	}

	return "", "", "", false
}

func jsExtraWalk(x *generic.Ctx, n *tsx.Node, parentClass string) bool {
	b := x.B
	t := n.Type()

	if x.Cfg.Lang == "typescript" && tsExtraWalk(x, n, parentClass) {
		return true
	}

	if t == "expression_statement" {
		if call := n.ChildOfType("call_expression", "new_expression"); call != nil {
			var cl []*tsx.Node
			jsTopmostClosures(call, &cl)

			for _, c := range cl {
				if bd := c.Field("body"); bd != nil {
					x.AddBody(b.FileID, bd)
				}
			}
		}

		if as := n.ChildOfType("assignment_expression"); as != nil {
			value := as.Field("right")
			kind, owner, member, ok := jsMemberAssignTarget(as.Field("left"))

			if value != nil && ok {
				line := n.Line()

				if jsFunctionValueTypes.Has(value.Type()) {
					var id string

					switch kind {
					case "exports":
						id = ids.MakeID(b.Stem, member)
						b.AddNode(id, member+"()", line)
						b.AddEdge(b.FileID, id, "contains", line)
					case "prototype":
						oid := ids.MakeID(b.Stem, owner)
						id = ids.MakeID(oid, member)
						b.AddNode(id, "."+member+"()", line)
						b.AddEdge(oid, id, "method", line)
					}

					if id != "" {
						x.MarkCallable(id, false)

						if bd := value.Field("body"); bd != nil {
							x.AddBody(id, bd)
						}

						return true
					}
				}
			}
		}

		return false
	}

	if parentClass != "" && (t == "field_definition" || t == "public_field_definition") {
		prop := n.Field("property")
		if prop == nil {
			prop = n.Field("name")
		}

		value := n.Field("value")
		if prop != nil && value != nil && jsFunctionValueTypes.Has(value.Type()) {
			if name := prop.Text(); name != "" {
				id := ids.MakeID(parentClass, name)
				b.AddNode(id, "."+name+"()", n.Line())
				b.AddEdge(parentClass, id, "method", n.Line())
				x.MarkCallable(id, false)

				if bd := value.Field("body"); bd != nil {
					x.AddBody(id, bd)
				}

				return true
			}
		}
	}

	if t != "lexical_declaration" && t != "variable_declaration" {
		return false
	}

	requireFound := jsRequireImports(x, n, b.FileID)

	parent := n.Parent()
	exported := parent != nil && parent.Type() == "export_statement"
	moduleLevel := parent != nil && (parent.Type() == "program" ||
		(exported && parent.Parent() != nil && parent.Parent().Type() == "program"))

	found := false

	if t == "lexical_declaration" && moduleLevel {
		for _, c := range n.Children() {
			if c.Type() != "variable_declarator" {
				continue
			}

			value, name := c.Field("value"), c.Field("name")
			exportedScalar := exported && name != nil && name.Type() == "identifier" && ids.NormalizeID(name.Text()) != ""
			line := c.Line()

			switch {
			case value != nil && jsFunctionValueTypes.Has(value.Type()):
				if name == nil || ids.NormalizeID(name.Text()) == "" {
					continue
				}

				id := ids.MakeID(b.Stem, name.Text())
				b.AddNode(id, name.Text()+"()", line)
				b.AddEdge(b.FileID, id, "contains", line)
				x.MarkCallable(id, false)

				if bd := value.Field("body"); bd != nil {
					x.AddBody(id, bd)
					jsNestedFunctions(x, bd, id)
				}

				found = true
			case value != nil && (exportedScalar || base.NewSet("object", "array", "as_expression",
				"satisfies_expression", "call_expression", "new_expression").Has(value.Type())):
				if name != nil && name.Type() == "object_pattern" && exported {
					for _, p := range name.NamedChildren() {
						var en string

						switch p.Type() {
						case "shorthand_property_identifier_pattern":
							en = p.Text()
						case "pair_pattern":
							if k := p.Field("key"); k != nil {
								en = k.Text()
							}
						}

						if en == "" || ids.NormalizeID(en) == "" {
							continue
						}

						pid := ids.MakeID(b.Stem, en)
						b.AddNode(pid, en, line)
						b.AddEdge(b.FileID, pid, "contains", line)
					}

					found = true
				} else if name != nil {
					cid := ids.MakeID(b.Stem, name.Text())
					b.AddNode(cid, name.Text(), line)
					b.AddEdge(b.FileID, cid, "contains", line)

					found = true

					inner := value
					for inner != nil && (inner.Type() == "as_expression" || inner.Type() == "satisfies_expression") {
						if nc := inner.NamedChildren(); len(nc) > 0 {
							inner = nc[0]
						} else {
							inner = nil
						}
					}

					if inner != nil && (inner.Type() == "call_expression" || inner.Type() == "new_expression") {
						var cl []*tsx.Node
						jsTopmostClosures(inner, &cl)

						for _, cc := range cl {
							if bd := cc.Field("body"); bd != nil {
								x.AddBody(cid, bd)
							}
						}
					}
				}
			}
		}
	}

	return found || requireFound
}

func tsExtraWalk(x *generic.Ctx, n *tsx.Node, parentClass string) bool {
	b := x.B
	t := n.Type()

	if parentClass != "" && n.Parent() != nil && n.Parent().Type() == "enum_body" &&
		(t == "property_identifier" || t == "enum_assignment") {
		nameNode := n
		if t == "enum_assignment" {
			nameNode = n.Field("name")
		}

		if nameNode != nil {
			name := nameNode.Text()
			if nameNode.Type() == "string" {
				name = strings.Trim(name, "'\"`")
			}

			if name != "" {
				id := ids.MakeID(parentClass, name)
				if !b.Has(id) {
					b.AddNode(id, name, n.Line())
					b.AddEdge(parentClass, id, "case_of", n.Line())
				}
			}
		}

		if t == "enum_assignment" {
			if v := n.Field("value"); v != nil {
				x.Walk(v, parentClass)
			}
		}

		return true
	}

	if n.IsNamed() && (t == "internal_module" || t == "module") {
		nameNode := n.Field("name")
		if nameNode == nil {
			for _, c := range n.NamedChildren() {
				if c.Type() == "identifier" || c.Type() == "nested_identifier" || c.Type() == "string" {
					nameNode = c

					break
				}
			}
		}

		bd := n.Field("body")
		if bd == nil {
			bd = n.ChildOfType("statement_block")
		}

		if nameNode != nil {
			name := nameNode.Text()
			if nameNode.Type() == "string" {
				name = strings.Trim(name, "'\"`")
			}

			if name != "" {
				id := ids.MakeID(b.Stem, name)
				b.AddNode(id, name, n.Line())
				b.AddEdge(b.FileID, id, "contains", n.Line())
			}
		}

		if bd != nil {
			for _, c := range bd.Children() {
				x.Walk(c, parentClass)
			}
		}

		return true
	}

	return false
}

func jsFunctionHook(x *generic.Ctx, n *tsx.Node, funcID string, _ int) {
	if bd := n.Field("body"); bd != nil {
		jsNestedFunctions(x, bd, funcID)
	}
}

func jsCallName(x *generic.Ctx, n *tsx.Node) (string, bool, string) {
	if jsxElementTypes.Has(n.Type()) {
		fn := n.Field("name")
		if fn == nil || fn.Type() != "identifier" {
			return "", false, ""
		}

		name := fn.Text()
		if name == "" || (name[0] >= 'a' && name[0] <= 'z') {
			return "", false, ""
		}

		return name, false, ""
	}

	if n.Type() == "call_expression" {
		fn := n.Field("function")
		if fn == nil && len(n.Children()) > 0 && n.Children()[0].Text() == "import" {
			fn = n.Children()[0]
		}

		if fn != nil && fn.Text() == "import" {
			jsDynamicImport(x, n)

			return "", false, ""
		}
	}

	callee, member, recv := generic.DefaultCallName(x.Cfg, n)
	if member && strings.HasPrefix(recv, "this.") {
		recv = strings.TrimPrefix(recv, "this.")

		return callee, member, "this." + recv
	}

	return callee, member, recv
}

func jsDynamicImport(x *generic.Ctx, n *tsx.Node) {
	args := n.Field("arguments")
	if args == nil {
		return
	}

	for _, a := range args.Children() {
		var raw string

		switch a.Type() {
		case "template_string":
			if a.ChildOfType("template_substitution") != nil {
				return
			}

			raw = strings.Trim(a.Text(), "`")
		case "string":
			raw = jsStringArg(a)
		default:
			continue
		}

		if raw == "" {
			return
		}

		target, _, ok := resolveJSTarget(raw, x.Path)
		if !ok {
			return
		}

		caller := x.CurrentCaller()
		e := x.B.AddEdgeCtx(caller, jsTargetID(target), "imports_from", n.Line(), "import")
		e.Extra = map[string]any{"deferred": true}

		return
	}
}

func jsClassHook(x *generic.Ctx, n *tsx.Node, classID string, line int) {
	for _, c := range n.Children() {
		if c.Type() != "class_heritage" {
			continue
		}

		for _, h := range c.Children() {
			switch h.Type() {
			case "extends_clause":
				for _, v := range h.NamedChildren() {
					name := v.Text()
					if v.Type() == "generic_type" || v.Type() == "call_expression" {
						if nn := v.NamedChildren(); len(nn) > 0 {
							name = nn[0].Text()
						}
					}

					if name != "" && isIdentLike(name) {
						x.B.AddEdge(classID, x.EnsureNamed(lastSeg(name, ".")), "inherits", line)
					}

					break
				}
			case "implements_clause":
				for _, v := range h.NamedChildren() {
					name := v.Text()
					if v.Type() == "generic_type" {
						if nn := v.NamedChildren(); len(nn) > 0 {
							name = nn[0].Text()
						}
					}

					if name != "" && isIdentLike(name) {
						x.B.AddEdge(classID, x.EnsureNamed(lastSeg(name, ".")), "implements", line)
					}
				}
			case "identifier", "member_expression":
				x.B.AddEdge(classID, x.EnsureNamed(lastSeg(h.Text(), ".")), "inherits", line)
			}
		}
	}

	jsDecorators(x, n, classID)
}

func isIdentLike(s string) bool {
	for _, r := range s {
		if !(r == '_' || r == '$' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 127) {
			return false
		}
	}

	return s != ""
}

func tsDecoratorName(d *tsx.Node) string {
	for _, c := range d.NamedChildren() {
		t := c
		if t.Type() == "call_expression" {
			if f := t.Field("function"); f != nil {
				t = f
			}
		}

		switch t.Type() {
		case "member_expression":
			if p := t.Field("property"); p != nil {
				return p.Text()
			}

			return ""
		case "identifier":
			return t.Text()
		}

		return ""
	}

	return ""
}

func jsDecorators(x *generic.Ctx, classNode *tsx.Node, classID string) {
	emit := func(d *tsx.Node, owner string) {
		name := tsDecoratorName(d)
		if name == "" {
			return
		}

		x.Ref(owner, x.EnsureNamed(name), d.Line(), "decorator")
	}

	for _, c := range classNode.Children() {
		if c.Type() == "decorator" {
			emit(c, classID)
		}
	}

	if p := classNode.Parent(); p != nil && p.Type() == "export_statement" {
		for _, c := range p.Children() {
			if c.Type() == "decorator" {
				emit(c, classID)
			} else if c.Type() == "class_declaration" || c.Type() == "abstract_class_declaration" {
				break
			}
		}
	}

	bd := classNode.ChildOfType("class_body")
	if bd == nil {
		return
	}

	var descendants func(n *tsx.Node, top bool, out *[]*tsx.Node)
	descendants = func(n *tsx.Node, top bool, out *[]*tsx.Node) {
		for _, c := range n.Children() {
			switch {
			case c.Type() == "decorator":
				*out = append(*out, c)
			case c.Type() == "class_declaration" || c.Type() == "abstract_class_declaration":
			case c.Type() == "method_definition" && !top:
			default:
				descendants(c, false, out)
			}
		}
	}

	members := bd.Children()
	for i, m := range members {
		switch m.Type() {
		case "decorator":
			owner := classID

			for j := i + 1; j < len(members); j++ {
				s := members[j]
				if !s.IsNamed() || s.Type() == "decorator" {
					continue
				}

				if s.Type() == "method_definition" {
					if nn := s.Field("name"); nn != nil {
						owner = ids.MakeID(classID, nn.Text())
					}
				}

				break
			}

			emit(m, owner)
		case "method_definition":
			owner := classID
			if nn := m.Field("name"); nn != nil {
				owner = ids.MakeID(classID, nn.Text())
			}

			var ds []*tsx.Node
			descendants(m, true, &ds)

			for _, d := range ds {
				emit(d, owner)
			}
		default:
			var ds []*tsx.Node
			descendants(m, true, &ds)

			for _, d := range ds {
				emit(d, classID)
			}
		}
	}
}

func jsBodyNode(x *generic.Ctx, n *tsx.Node, caller string) {
	if n.Type() == "lexical_declaration" || n.Type() == "variable_declaration" {
		jsRequireImports(x, n, caller)
	}
}

var (
	jsRationalePrefixes = []string{
		"// NOTE:", "// IMPORTANT:", "// HACK:", "// WHY:", "// RATIONALE:",
		"// TODO:", "// FIXME:",
		"* NOTE:", "* IMPORTANT:", "* HACK:", "* WHY:", "* RATIONALE:",
		"* TODO:", "* FIXME:",
	}
	jsDocRefRe      = regexp.MustCompile(`(?i)\b(ADR[- ]?\d{1,5}|RFC[- ]?\d{1,5})\b`)
	jsDocRefPartsRe = regexp.MustCompile(`^([A-Za-z]+)[- ]?(\d+)`)
	jsCommentLineRe = regexp.MustCompile(`^\s*(//|/\*|\*)`)
	jsDynImportRe   = regexp.MustCompile("(?:^|[^\\w])import\\s*\\(\\s*(?:'([^'\\n]+)'|\"([^\"\\n]+)\"|`([^`$\\n]+)`)\\s*\\)")
)

func jsRationale(x *generic.Ctx) {
	b := x.B
	seenRefs := map[string]bool{}

	for i, line := range strings.Split(string(x.Tree.Src), "\n") {
		s := strings.TrimSpace(line)
		lineNo := i + 1

		for _, p := range jsRationalePrefixes {
			if strings.HasPrefix(s, p) {
				addRationale(x, strings.TrimLeft(s, "/* "), lineNo, b.FileID)

				break
			}
		}

		if !jsCommentLineRe.MatchString(line) {
			continue
		}

		for _, m := range jsDocRefRe.FindAllStringSubmatch(s, -1) {
			parts := jsDocRefPartsRe.FindStringSubmatch(m[1])
			if parts == nil {
				continue
			}

			kind := strings.ToUpper(parts[1])

			label := kind + "-" + parts[2]
			if kind == "ADR" {
				label = kind + "-" + zfill(parts[2], 4)
			}

			if seenRefs[label] {
				continue
			}

			seenRefs[label] = true
			rid := ids.MakeID("docref", label)

			if !b.Has(rid) {
				n := b.AddNode(rid, label, lineNo)
				n.FileType = "doc_ref"
			}

			b.AddEdge(b.FileID, rid, "cites", lineNo)
		}
	}

	jsRescueDynamicImports(x)
}

func zfill(s string, n int) string {
	for len(s) < n {
		s = "0" + s
	}

	return s
}

// jsRescueDynamicImports recovers module-level import() edges the call
// walker never visits (Graphify's _rescue_js_dynamic_imports).
func jsRescueDynamicImports(x *generic.Ctx) {
	src := string(x.Tree.Src)
	if !strings.Contains(src, "import") {
		return
	}

	b := x.B
	deferred := map[string]bool{}

	for _, e := range b.Edges {
		if e.Extra["deferred"] == true && e.Relation == "imports_from" && e.Source == b.FileID {
			deferred[e.Target] = true
		}
	}

	rescued := map[string]bool{}

	for _, m := range jsDynImportRe.FindAllStringSubmatchIndex(src, -1) {
		raw := ""

		for g := 1; g <= 3; g++ {
			if m[2*g] >= 0 {
				raw = src[m[2*g]:m[2*g+1]]

				break
			}
		}

		if raw == "" {
			continue
		}

		lineStart := strings.LastIndexByte(src[:m[0]], '\n') + 1
		if strings.Contains(src[lineStart:m[0]], "//") {
			continue
		}

		id, stubSF, resolved, ok := jsRescuedSpecifier(x.Path, raw)
		if !ok || deferred[id] || deferred[ids.MakeID("ref", raw)] {
			continue
		}

		key := raw
		if resolved != "" {
			key = resolved
		}

		if rescued[key] {
			continue
		}

		rescued[key] = true
		line := strings.Count(src[:m[0]], "\n") + 1

		e := &model.Edge{
			Source: b.FileID, Target: id, Relation: "dynamic_import",
			Confidence: model.Extracted, SourceFile: x.Path,
		}
		_ = line

		if resolved == "" && !b.Has(id) {
			n := b.AddNode(id, raw, 0)
			n.SourceFile = stubSF
			n.SourceLocation = ""
			n.Extra = map[string]any{"confidence": model.Extracted}
		}

		b.Edges = append(b.Edges, e)
	}
}

func jsRescuedSpecifier(path, raw string) (id, stubSF, resolved string, ok bool) {
	if strings.HasPrefix(raw, ".") {
		r := resolveJSImportPath(filepath.Join(filepath.Dir(path), raw))
		if isFile(r) {
			resolved = r
		}

		return ids.MakeID(r), r, resolved, true
	}

	if c := loadTSConfig(filepath.Dir(path)); c != nil {
		if hit := resolveTSAlias(raw, c); hit != "" {
			r := resolveJSImportPath(hit)
			if isFile(r) {
				resolved = r
			}

			return ids.MakeID(r), r, resolved, true
		}
	}

	if strings.HasPrefix(raw, "@/") {
		if r := resolveJSModule(raw, filepath.Dir(path)); r != "" && isFile(r) {
			return ids.MakeID(r), r, r, true
		}
	}

	mod := lastSeg(raw, "/")
	if mod == "" {
		return "", "", "", false
	}

	return ids.MakeID(mod), raw, "", true
}

func newJSConfig(lang, grammar string, tsx bool) *generic.Config {
	cfg := &generic.Config{
		Lang:                 lang,
		Grammar:              grammar,
		ClassTypes:           base.NewSet("class_declaration"),
		FunctionTypes:        base.NewSet("function_declaration", "generator_function_declaration", "method_definition"),
		ImportTypes:          base.NewSet("import_statement", "export_statement"),
		CallTypes:            base.NewSet("call_expression", "new_expression", "jsx_opening_element", "jsx_self_closing_element"),
		CallFunctionField:    "function",
		CallAccessorTypes:    base.NewSet("member_expression"),
		CallAccessorField:    "property",
		CallAccessorObjField: "object",
		FunctionBoundary: base.NewSet("function_declaration", "generator_function_declaration", "arrow_function",
			"method_definition", "function_expression", "generator_function"),
		ImportHandler: jsImport,
		ExtraWalk:     jsExtraWalk,
		FunctionHook:  jsFunctionHook,
		ClassHook:     jsClassHook,
		CallName:      jsCallName,
		BodyNode:      jsBodyNode,
		Rationale:     jsRationale,
	}

	if lang == "typescript" {
		cfg.ClassTypes = base.NewSet("class_declaration", "abstract_class_declaration", "interface_declaration",
			"enum_declaration", "type_alias_declaration")
		cfg.FunctionTypes = cfg.FunctionTypes.Union("method_signature")
		cfg.CallTypes = base.NewSet("call_expression", "new_expression")

		if tsx {
			cfg.CallTypes = cfg.CallTypes.Union("jsx_opening_element", "jsx_self_closing_element")
		}
	}

	return cfg
}

var (
	jsConfig  = newJSConfig("javascript", "javascript", true)
	tsConfigG = newJSConfig("typescript", "typescript", false)
	tsxConfig = newJSConfig("typescript", "tsx", true)
)

// ExtractJS extracts a JavaScript file.
func ExtractJS(path, root string, src []byte) *model.Extraction {
	return generic.Extract(jsConfig, path, root, src)
}

// ExtractTS extracts a TypeScript file.
func ExtractTS(path, root string, src []byte) *model.Extraction {
	return generic.Extract(tsConfigG, path, root, src)
}

// ExtractTSX extracts a TSX file.
func ExtractTSX(path, root string, src []byte) *model.Extraction {
	return generic.Extract(tsxConfig, path, root, src)
}
