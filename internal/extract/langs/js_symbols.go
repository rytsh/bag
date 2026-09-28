package langs

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// JS/TS symbol-level resolution: named/default imports bound to the defining
// symbol through re-export barrels, heritage (extends/implements) and member
// type references resolved through imports, and calls from top-level
// functions to imported names.
//
// Adapted from Graphify's _collect_js_symbol_resolution_facts /
// _apply_symbol_resolution_facts (Apache-2.0).

var jsSymbolSuffixes = base.NewSet(".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts")

var jsPrimitiveTypes = base.NewSet("string", "number", "boolean", "any", "unknown", "void", "never",
	"object", "null", "undefined", "bigint", "symbol", "this")

type jsSymKey struct{ path, name string }

type jsImportFact struct {
	file, local, target, imported string
	line                          int
}

type jsAliasFact struct {
	file, alias, target string
}

type jsExportFact struct {
	file, exported     string
	line               int
	target, targetName string
	local              string
	hasTarget          bool
	typeOnly           bool
}

type jsFileFact struct {
	file, target string
	name         string
	line         int
	typeOnly     bool
}

type jsUseFact struct {
	file, sourceID, local, relation, context string
	line                                     int
}

type jsDeclFact struct {
	file, name string
	line       int
}

type jsFacts struct {
	decls      []jsDeclFact
	imports    []jsImportFact
	aliases    []jsAliasFact
	exports    []jsExportFact
	stars      []jsFileFact
	namespaces []jsFileFact
	uses       []jsUseFact
}

func jsGrammarFor(path string) string {
	switch filepath.Ext(path) {
	case ".tsx":
		return "tsx"
	case ".ts", ".mts", ".cts":
		return "typescript"
	default:
		return "javascript"
	}
}

func jsResolvedPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}

	return filepath.Clean(p)
}

func jsModuleSpecifier(n *tsx.Node) (string, bool) {
	s := n.Field("source")
	if s == nil {
		s = n.ChildOfType("string")
	}

	if s == nil {
		return "", false
	}

	raw := strings.Trim(strings.TrimSpace(s.Text()), "'\"`")

	return raw, raw != ""
}

func jsNamedSpecifiers(n *tsx.Node, specType string) [][2]string {
	var out [][2]string

	n.Walk(func(c *tsx.Node) bool {
		if c.Type() != specType {
			return true
		}

		nn := c.Field("name")
		if nn == nil {
			return true
		}

		name, exposed := nn.Text(), nn.Text()
		if al := c.Field("alias"); al != nil {
			exposed = al.Text()
		}

		if name != "" && exposed != "" {
			out = append(out, [2]string{name, exposed})
		}

		return true
	})

	return out
}

func jsLexicalAliases(n *tsx.Node) [][2]string {
	if n.Type() != "lexical_declaration" {
		return nil
	}

	var out [][2]string

	for _, c := range n.Children() {
		if c.Type() != "variable_declarator" {
			continue
		}

		nn, v := c.Field("name"), c.Field("value")
		if nn != nil && v != nil && (v.Type() == "identifier" || v.Type() == "type_identifier") {
			out = append(out, [2]string{nn.Text(), v.Text()})
		}
	}

	return out
}

func jsExportedDeclarationNames(n *tsx.Node) []string {
	decl := n.Field("declaration")
	if decl == nil {
		return nil
	}

	var names []string

	switch decl.Type() {
	case "lexical_declaration", "variable_declaration":
		for _, a := range jsLexicalAliases(decl) {
			names = append(names, a[0])
		}

		for _, d := range decl.NamedChildren() {
			if d.Type() != "variable_declarator" {
				continue
			}

			if nn := d.Field("name"); nn != nil && nn.Type() == "identifier" {
				if !containsString(names, nn.Text()) {
					names = append(names, nn.Text())
				}
			}
		}
	case "class_declaration", "abstract_class_declaration", "interface_declaration",
		"type_alias_declaration", "function_declaration":
		if nn := decl.Field("name"); nn != nil {
			names = append(names, nn.Text())
		}
	}

	return names
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}

	return false
}

func jsDefaultImportName(n *tsx.Node) string {
	for _, c := range n.Children() {
		if c.Type() != "import_clause" {
			continue
		}

		for _, sub := range c.Children() {
			if sub.Type() == "identifier" {
				return sub.Text()
			}
		}
	}

	return ""
}

func jsDefaultExportName(n *tsx.Node) string {
	if n.ChildOfType("default") == nil {
		return ""
	}

	if decl := n.Field("declaration"); decl != nil {
		if nn := decl.Field("name"); nn != nil {
			return nn.Text()
		}

		return ""
	}

	if v := n.Field("value"); v != nil && v.Type() == "identifier" {
		return v.Text()
	}

	return ""
}

func jsTopLevelFunctionBodies(path string, root *tsx.Node) [][2]any {
	stem := base.FileStem(path)

	var top []*tsx.Node

	for _, n := range root.Children() {
		if n.Type() == "export_statement" && n.ChildOfType("string") == nil {
			for _, c := range n.Children() {
				if c.Type() == "function_declaration" || c.Type() == "lexical_declaration" {
					top = append(top, c)
				}
			}
		} else {
			top = append(top, n)
		}
	}

	var out [][2]any

	for _, n := range top {
		switch n.Type() {
		case "function_declaration":
			nn, bd := n.Field("name"), n.Field("body")
			if nn != nil && bd != nil {
				out = append(out, [2]any{ids.MakeID(stem, nn.Text()), bd})
			}
		case "lexical_declaration":
			for _, c := range n.Children() {
				if c.Type() != "variable_declarator" {
					continue
				}

				nn, v := c.Field("name"), c.Field("value")
				if nn != nil && v != nil && v.Type() == "arrow_function" {
					out = append(out, [2]any{ids.MakeID(stem, nn.Text()), v})
				}
			}
		}
	}

	return out
}

func jsCallIdentifier(n *tsx.Node) string {
	if n.Type() != "call_expression" {
		return ""
	}

	fn := n.Field("function")
	if fn == nil {
		if nc := n.NamedChildren(); len(nc) > 0 {
			fn = nc[0]
		}
	}

	if fn != nil && (fn.Type() == "identifier" || fn.Type() == "type_identifier") {
		if _, awaited := jsAwaitGenericCallee(n); awaited {
			return ""
		}

		return fn.Text()
	}

	return ""
}

func tsHeritageEntries(clause *tsx.Node) []string {
	var out []string

	for _, c := range clause.Children() {
		if !c.IsNamed() {
			continue
		}

		switch c.Type() {
		case "identifier", "type_identifier":
			if t := c.Text(); t != "" {
				out = append(out, t)
			}
		case "generic_type":
			nn := c.Field("name")
			if nn == nil {
				nn = c.ChildOfType("type_identifier", "nested_type_identifier", "identifier")
			}

			if nn != nil {
				if t := lastSeg(nn.Text(), "."); t != "" {
					out = append(out, t)
				}
			}
		case "nested_type_identifier":
			if t := lastSeg(c.Text(), "."); t != "" {
				out = append(out, t)
			}
		}
	}

	return out
}

// tsCollectTypeRefs walks a TS type annotation; roles are "type" or
// "generic_arg".
func tsCollectTypeRefs(n *tsx.Node, generic bool, out *[][2]string) {
	if n == nil {
		return
	}

	role := "type"
	if generic {
		role = "generic_arg"
	}

	switch n.Type() {
	case "type_annotation":
		for _, c := range n.NamedChildren() {
			tsCollectTypeRefs(c, generic, out)
		}

		return
	case "type_identifier", "identifier":
		if name := n.Text(); name != "" && !jsPrimitiveTypes.Has(name) {
			*out = append(*out, [2]string{name, role})
		}

		return
	case "nested_type_identifier":
		if t := lastSeg(n.Text(), "."); t != "" && !jsPrimitiveTypes.Has(t) {
			*out = append(*out, [2]string{t, role})
		}

		return
	case "generic_type":
		if nn := n.Field("name"); nn != nil {
			if t := lastSeg(nn.Text(), "."); t != "" && !jsPrimitiveTypes.Has(t) {
				*out = append(*out, [2]string{t, role})
			}
		} else if c := n.ChildOfType("type_identifier", "nested_type_identifier"); c != nil {
			if t := lastSeg(c.Text(), "."); t != "" && !jsPrimitiveTypes.Has(t) {
				*out = append(*out, [2]string{t, role})
			}
		}

		for _, c := range n.Children() {
			if c.Type() == "type_arguments" {
				for _, sub := range c.NamedChildren() {
					tsCollectTypeRefs(sub, true, out)
				}
			}
		}

		return
	}

	if n.IsNamed() {
		for _, c := range n.NamedChildren() {
			tsCollectTypeRefs(c, generic, out)
		}
	}
}

func tsWalkClassMembers(cls *tsx.Node, path, classID string, f *jsFacts) {
	line := cls.Line()

	for _, c := range cls.Children() {
		switch c.Type() {
		case "class_heritage":
			saw := false

			for _, clause := range c.Children() {
				rel := ""

				switch clause.Type() {
				case "extends_clause":
					rel = "inherits"
				case "implements_clause":
					rel = "implements"
				default:
					continue
				}

				saw = true

				for _, name := range tsHeritageEntries(clause) {
					f.uses = append(f.uses, jsUseFact{path, classID, name, rel, "type", clause.Line()})
				}
			}

			if !saw {
				for _, name := range tsHeritageEntries(c) {
					f.uses = append(f.uses, jsUseFact{path, classID, name, "inherits", "type", c.Line()})
				}
			}
		case "extends_type_clause":
			for _, name := range tsHeritageEntries(c) {
				f.uses = append(f.uses, jsUseFact{path, classID, name, "inherits", "type", c.Line()})
			}
		}
	}

	_ = line

	body := cls.Field("body")
	if body == nil {
		return
	}

	for _, m := range body.Children() {
		mLine := m.Line()

		switch m.Type() {
		case "method_definition", "method_signature", "abstract_method_signature":
			nn := m.Field("name")
			if nn == nil {
				continue
			}

			methodID := ids.MakeID(classID, nn.Text())

			if params := m.Field("parameters"); params != nil {
				for _, p := range params.Children() {
					if p.Type() != "required_parameter" && p.Type() != "optional_parameter" {
						continue
					}

					var refs [][2]string

					tsCollectTypeRefs(p.Field("type"), false, &refs)

					for _, r := range refs {
						ctx := "parameter_type"
						if r[1] == "generic_arg" {
							ctx = "generic_arg"
						}

						f.uses = append(f.uses, jsUseFact{path, methodID, r[0], "references", ctx, mLine})
					}
				}
			}

			if rt := m.Field("return_type"); rt != nil {
				var refs [][2]string

				tsCollectTypeRefs(rt, false, &refs)

				for _, r := range refs {
					ctx := "return_type"
					if r[1] == "generic_arg" {
						ctx = "generic_arg"
					}

					f.uses = append(f.uses, jsUseFact{path, methodID, r[0], "references", ctx, mLine})
				}
			}
		case "public_field_definition", "property_signature":
			ta := m.ChildOfType("type_annotation")
			if ta == nil {
				continue
			}

			var refs [][2]string

			tsCollectTypeRefs(ta, false, &refs)

			for _, r := range refs {
				ctx := "field"
				if r[1] == "generic_arg" {
					ctx = "generic_arg"
				}

				f.uses = append(f.uses, jsUseFact{path, classID, r[0], "references", ctx, mLine})
			}
		}
	}
}

func jsStatementTypeOnly(n *tsx.Node) bool {
	for _, c := range n.Children() {
		if c.Type() == "type" && !c.IsNamed() {
			return true
		}
	}

	return false
}

func collectJSSymbolFacts(per []extract.FileResult) (*jsFacts, []string) {
	f := &jsFacts{}

	type parsed struct {
		path string
		tree *tsx.Tree
	}

	var (
		trees []parsed
		paths []string
	)

	defer func() {
		for _, t := range trees {
			t.tree.Release()
		}
	}()

	for _, fr := range per {
		if !jsSymbolSuffixes.Has(filepath.Ext(fr.Path)) {
			continue
		}

		src, err := os.ReadFile(fr.Path)
		if err != nil {
			continue
		}

		tree, err := tsx.Parse(jsGrammarFor(fr.Path), src)
		if err != nil {
			continue
		}

		path := fr.Path
		trees = append(trees, parsed{path, tree})
		paths = append(paths, path)

		tree.Root.Walk(func(n *tsx.Node) bool {
			if n.Type() == "export_statement" {
				for _, name := range jsExportedDeclarationNames(n) {
					f.decls = append(f.decls, jsDeclFact{path, name, n.Line()})
				}
			}

			if n.Type() != "import_statement" {
				return true
			}

			raw, ok := jsModuleSpecifier(n)
			if !ok {
				return true
			}

			target := resolveJSModule(raw, filepath.Dir(path))
			if target == "" {
				return true
			}

			target = jsResolvedPath(target)

			for _, s := range jsNamedSpecifiers(n, "import_specifier") {
				f.imports = append(f.imports, jsImportFact{path, s[1], target, s[0], n.Line()})
			}

			if d := jsDefaultImportName(n); d != "" {
				f.imports = append(f.imports, jsImportFact{path, d, target, "default", n.Line()})
			}

			return true
		})

		tree.Root.Walk(func(n *tsx.Node) bool {
			for _, a := range jsLexicalAliases(n) {
				f.aliases = append(f.aliases, jsAliasFact{path, a[0], a[1]})
			}

			return true
		})
	}

	for _, t := range trees {
		path := t.path

		t.tree.Root.Walk(func(n *tsx.Node) bool {
			if n.Type() != "export_statement" {
				return true
			}

			raw, hasModule := jsModuleSpecifier(n)
			clause := n.ChildOfType("export_clause")
			typeOnly := jsStatementTypeOnly(n)

			if hasModule {
				target := resolveJSModule(raw, filepath.Dir(path))
				if target == "" {
					return true
				}

				target = jsResolvedPath(target)

				if ns := n.ChildOfType("namespace_export"); ns != nil {
					if id := ns.ChildOfType("identifier"); id != nil && id.Text() != "" {
						f.namespaces = append(f.namespaces, jsFileFact{path, target, id.Text(), n.Line(), typeOnly})
					}
				} else if n.ChildOfType("*") != nil {
					f.stars = append(f.stars, jsFileFact{path, target, "", n.Line(), typeOnly})
				}

				if clause != nil {
					for _, s := range jsNamedSpecifiers(clause, "export_specifier") {
						f.exports = append(f.exports, jsExportFact{
							file: path, exported: s[1], line: n.Line(),
							target: target, targetName: s[0], hasTarget: true, typeOnly: typeOnly,
						})
					}
				}

				return true
			}

			if clause != nil {
				for _, s := range jsNamedSpecifiers(clause, "export_specifier") {
					f.exports = append(f.exports, jsExportFact{file: path, exported: s[1], line: n.Line(), local: s[0]})
				}

				return true
			}

			for _, name := range jsExportedDeclarationNames(n) {
				f.exports = append(f.exports, jsExportFact{file: path, exported: name, line: n.Line(), local: name})
			}

			if d := jsDefaultExportName(n); d != "" {
				f.exports = append(f.exports, jsExportFact{file: path, exported: "default", line: n.Line(), local: d})
			}

			return true
		})
	}

	for _, t := range trees {
		for _, fb := range jsTopLevelFunctionBodies(t.path, t.tree.Root) {
			sid, body := fb[0].(string), fb[1].(*tsx.Node)

			body.Walk(func(n *tsx.Node) bool {
				if name := jsCallIdentifier(n); name != "" {
					f.uses = append(f.uses, jsUseFact{t.path, sid, name, "calls", "call", n.Line()})
				}

				return true
			})
		}
	}

	for _, t := range trees {
		stem := base.FileStem(t.path)

		t.tree.Root.Walk(func(n *tsx.Node) bool {
			switch n.Type() {
			case "class_declaration", "abstract_class_declaration", "interface_declaration":
			default:
				return true
			}

			nn := n.Field("name")
			if nn == nil || nn.Text() == "" {
				return true
			}

			tsWalkClassMembers(n, t.path, ids.MakeID(stem, nn.Text()), f)

			return true
		})
	}

	return f, paths
}

func resolveJSSymbols(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	f, paths := collectJSSymbolFacts(per)
	if len(f.decls)+len(f.imports)+len(f.aliases)+len(f.exports)+len(f.stars)+len(f.namespaces)+len(f.uses) == 0 {
		return
	}

	nodes, edges := *nodesP, *edgesP

	pathByResolved := map[string]string{}
	fileID := map[string]string{}

	for _, p := range paths {
		r := jsResolvedPath(p)
		pathByResolved[r] = p
		fileID[r] = ids.MakeID(p)
	}

	origPath := func(r string) string {
		if p, ok := pathByResolved[r]; ok {
			return p
		}

		return r
	}

	resolvedCache := map[string]string{}
	resolve := func(p string) string {
		if r, ok := resolvedCache[p]; ok {
			return r
		}

		r := jsResolvedPath(p)
		resolvedCache[p] = r

		return r
	}

	symbols := map[jsSymKey]string{}
	members := map[jsSymKey]bool{}

	for _, n := range nodes {
		if n.SourceFile == "" || n.ID == "" {
			continue
		}

		raw := strings.TrimSpace(n.Label)
		label := strings.TrimLeft(strings.Trim(raw, "()"), ".")

		if label == "" {
			continue
		}

		k := jsSymKey{resolve(n.SourceFile), label}

		if strings.HasPrefix(raw, ".") {
			if _, ok := symbols[k]; ok {
				continue
			}

			members[k] = true
		} else {
			delete(members, k)
		}

		symbols[k] = n.ID
	}

	ensureSymbol := func(path, name string, line int) string {
		k := jsSymKey{resolve(path), name}
		if id, ok := symbols[k]; ok {
			return id
		}

		id := ids.MakeID(base.FileStem(path), name)
		symbols[k] = id
		nodes = append(nodes, &model.Node{
			ID: id, Label: name, FileType: model.FileTypeCode,
			SourceFile: path, SourceLocation: base.Loc(line),
		})

		return id
	}

	existing := map[[4]string]bool{}
	for _, e := range edges {
		existing[[4]string{e.Source, e.Target, e.Relation, e.Context}] = true
	}

	add := func(src, tgt, rel, ctx string, line int, file string, typeOnly bool) {
		k := [4]string{src, tgt, rel, ctx}
		if existing[k] {
			return
		}

		existing[k] = true

		e := &model.Edge{
			Source: src, Target: tgt, Relation: rel, Context: ctx, Confidence: model.Extracted,
			SourceFile: file, SourceLocation: base.Loc(line), Weight: 1,
		}
		if typeOnly {
			e.Extra = map[string]any{"type_only": true}
		}

		edges = append(edges, e)
	}

	for _, d := range f.decls {
		ensureSymbol(d.file, d.name, d.line)
	}

	localAliases := map[string]map[string][2]string{}

	for _, im := range f.imports {
		fp := resolve(im.file)
		if localAliases[fp] == nil {
			localAliases[fp] = map[string][2]string{}
		}

		localAliases[fp][im.local] = [2]string{im.target, im.imported}
	}

	var aliasFiles []string

	pending := map[string][]jsAliasFact{}

	for _, a := range f.aliases {
		fp := resolve(a.file)
		if _, ok := pending[fp]; !ok {
			aliasFiles = append(aliasFiles, fp)
		}

		pending[fp] = append(pending[fp], a)
	}

	for _, fp := range aliasFiles {
		la := localAliases[fp]
		if la == nil {
			la = map[string][2]string{}
			localAliases[fp] = la
		}

		for changed := true; changed; {
			changed = false

			for _, a := range pending[fp] {
				if _, ok := la[a.alias]; ok {
					continue
				}

				if o, ok := la[a.target]; ok {
					la[a.alias] = o
					changed = true
				}
			}
		}
	}

	namedExports := map[string]map[string][2]string{}
	exportOrigins := map[jsSymKey]map[jsSymKey]bool{}
	starExports := map[string][]string{}

	setNamed := func(file, name string, origin [2]string) {
		if namedExports[file] == nil {
			namedExports[file] = map[string][2]string{}
		}

		namedExports[file][name] = origin
	}

	addOrigin := func(k, o jsSymKey) {
		if exportOrigins[k] == nil {
			exportOrigins[k] = map[jsSymKey]bool{}
		}

		exportOrigins[k][o] = true
	}

	for _, s := range f.stars {
		sp, tp := resolve(s.file), resolve(s.target)
		starExports[sp] = append(starExports[sp], tp)

		if sid, ok := fileID[sp]; ok {
			add(sid, ids.MakeID(origPath(tp)), "re_exports", "export", s.line, s.file, s.typeOnly)
		}
	}

	for _, ns := range f.namespaces {
		sp, tp := resolve(ns.file), resolve(ns.target)
		nsID := ensureSymbol(ns.file, ns.name, ns.line)
		setNamed(sp, ns.name, [2]string{sp, ns.name})

		if sid, ok := fileID[sp]; ok {
			add(sid, nsID, "contains", "namespace_export", ns.line, ns.file, false)
			add(sid, ids.MakeID(origPath(tp)), "re_exports", "export", ns.line, ns.file, ns.typeOnly)
		}
	}

	for _, ex := range f.exports {
		fp := resolve(ex.file)

		var (
			origin [2]string
			found  bool
		)

		switch {
		case ex.hasTarget:
			origin, found = [2]string{resolve(ex.target), ex.targetName}, true
		case ex.local != "":
			origin, found = localAliases[fp][ex.local]
			if !found {
				if _, ok := symbols[jsSymKey{fp, ex.local}]; ok {
					origin, found = [2]string{fp, ex.local}, true
				}
			}
		}

		if !found {
			if ex.local != "" {
				addOrigin(jsSymKey{fp, ex.exported}, jsSymKey{fp, ex.local})
			}

			continue
		}

		addOrigin(jsSymKey{fp, ex.exported}, jsSymKey{origin[0], origin[1]})
		setNamed(fp, ex.exported, origin)

		if origin[0] != fp {
			if sid, ok := fileID[fp]; ok {
				add(sid, ids.MakeID(origPath(origin[0])), "re_exports", "export", ex.line, ex.file, ex.typeOnly)
			}
		}
	}

	var resolveOrigin func(target, name string, seen map[jsSymKey]bool) jsSymKey
	resolveOrigin = func(target, name string, seen map[jsSymKey]bool) jsSymKey {
		target = resolve(target)
		k := jsSymKey{target, name}

		if seen[k] {
			return k
		}

		seen[k] = true

		if o, ok := namedExports[target][name]; ok {
			return resolveOrigin(o[0], o[1], seen)
		}

		for _, st := range starExports[target] {
			sk := jsSymKey{st, name}
			if _, ok := symbols[sk]; ok && !members[sk] {
				return sk
			}

			r := resolveOrigin(st, name, seen)
			if _, ok := symbols[r]; ok && !members[r] {
				return r
			}
		}

		return k
	}

	var candidates func(k jsSymKey, seen map[jsSymKey]bool) (map[jsSymKey]bool, bool)
	candidates = func(k jsSymKey, seen map[jsSymKey]bool) (map[jsSymKey]bool, bool) {
		out := map[jsSymKey]bool{}
		if seen[k] {
			return out, true
		}

		next := make(map[jsSymKey]bool, len(seen)+1)
		for s := range seen {
			next[s] = true
		}

		next[k] = true
		cyclic := false

		origins, ok := exportOrigins[k]
		if !ok {
			for _, p := range starExports[k.path] {
				b, bc := candidates(jsSymKey{p, k.name}, next)
				for x := range b {
					out[x] = true
				}

				cyclic = cyclic || bc
			}

			return out, cyclic
		}

		for o := range origins {
			if o == k {
				out[o] = true

				continue
			}

			b, bc := candidates(o, next)
			for x := range b {
				out[x] = true
			}

			cyclic = cyclic || bc

			if len(b) == 0 && !bc {
				out[o] = true
			}
		}

		return out, cyclic
	}

	owned := map[string]bool{}
	for _, n := range nodes {
		owned[n.ID] = true
	}

	for _, e := range edges {
		if e.Relation != "re_exports" || owned[e.Target] || e.SourceFile == "" {
			continue
		}

		sp := resolve(e.SourceFile)

		for _, ex := range f.exports {
			if !ex.hasTarget || resolve(ex.file) != sp || base.Loc(ex.line) != e.SourceLocation {
				continue
			}

			if e.Target != ids.MakeID(base.FileStem(origPath(resolve(ex.target))), ex.targetName) &&
				e.Target != ids.MakeID(base.FileStem(ex.target), ex.targetName) {
				continue
			}

			cands, _ := candidates(jsSymKey{resolve(ex.target), ex.targetName}, map[jsSymKey]bool{})
			if len(cands) == 1 {
				for o := range cands {
					if tid, ok := symbols[o]; ok && owned[tid] {
						e.Target = tid
					}
				}
			}

			break
		}
	}

	for _, im := range f.imports {
		sid, ok := fileID[resolve(im.file)]
		if !ok {
			continue
		}

		o := resolveOrigin(im.target, im.imported, map[jsSymKey]bool{})

		if tid, ok := symbols[o]; ok {
			add(sid, tid, "imports", "import", im.line, im.file, false)
		}
	}

	owned = map[string]bool{}
	for _, n := range nodes {
		owned[n.ID] = true
	}

	for _, u := range f.uses {
		fp := resolve(u.file)
		tid := ""

		if a, ok := localAliases[fp][u.local]; ok {
			tid = symbols[resolveOrigin(a[0], a[1], map[jsSymKey]bool{})]
		}

		if tid == "" && (u.relation == "inherits" || u.relation == "implements") {
			tid = symbols[jsSymKey{fp, u.local}]
		}

		if tid == "" {
			continue
		}

		src := u.sourceID

		switch u.relation {
		case "inherits", "implements", "references":
			if !owned[src] {
				continue
			}
		case "calls":
			if !owned[src] {
				if src = fileID[fp]; src == "" {
					continue
				}
			}
		}

		add(src, tid, u.relation, u.context, u.line, u.file, false)
	}

	*nodesP, *edgesP = nodes, edges
}
