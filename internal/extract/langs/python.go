package langs

import (
	"crypto/sha1" //nolint:gosec // id salt, mirrors Graphify
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

var pyTypeContainers = base.NewSet(
	"list", "dict", "set", "tuple", "frozenset", "type",
	"List", "Dict", "Set", "Tuple", "FrozenSet", "Type",
	"Optional", "Union", "Sequence", "Iterable", "Mapping", "MutableMapping",
	"Iterator", "Callable", "Awaitable", "AsyncIterable", "AsyncIterator", "Coroutine",
	"Generator", "AsyncGenerator", "ContextManager", "AsyncContextManager",
	"Annotated", "ClassVar", "Final", "Literal", "Concatenate", "ParamSpec", "TypeVar",
	"None", "Ellipsis",
)

var pyAnnotationNoise = base.NewSet(
	"str", "int", "float", "bool", "bytes", "bytearray", "complex", "object",
	"True", "False",
	"MagicMock", "Mock", "AsyncMock", "NonCallableMock",
	"NonCallableMagicMock", "PropertyMock", "patch", "sentinel",
)

var pyDecoratorNoise = base.NewSet(
	"property", "staticmethod", "classmethod", "abstractmethod",
	"abstractproperty", "cached_property", "wraps", "lru_cache", "cache",
	"singledispatch", "singledispatchmethod", "total_ordering",
	"contextmanager", "asynccontextmanager", "overload", "override",
	"final", "no_type_check", "runtime_checkable", "dataclass",
)

type typeRef struct{ name, role string }

func roleOf(generic bool) string {
	if generic {
		return "generic_arg"
	}

	return "type"
}

func pyTypeRefs(n *tsx.Node, generic bool, out *[]typeRef) {
	if n == nil {
		return
	}

	ok := func(s string) bool { return s != "" && !pyTypeContainers.Has(s) && !pyAnnotationNoise.Has(s) }

	switch n.Type() {
	case "type":
		for _, c := range n.NamedChildren() {
			pyTypeRefs(c, generic, out)
		}

		return
	case "identifier":
		if s := n.Text(); ok(s) {
			*out = append(*out, typeRef{s, roleOf(generic)})
		}

		return
	case "attribute":
		t := n.Text()
		t = t[strings.LastIndexByte(t, '.')+1:]

		if ok(t) {
			*out = append(*out, typeRef{t, roleOf(generic)})
		}

		return
	case "generic_type":
		for _, c := range n.Children() {
			switch c.Type() {
			case "identifier":
				if s := c.Text(); ok(s) {
					*out = append(*out, typeRef{s, roleOf(generic)})
				}
			case "type_parameter":
				for _, s := range c.NamedChildren() {
					pyTypeRefs(s, true, out)
				}
			}
		}

		return
	case "subscript":
		v := n.Field("value")
		pyTypeRefs(v, generic, out)

		for _, c := range n.NamedChildren() {
			if !c.Same(v) {
				pyTypeRefs(c, true, out)
			}
		}

		return
	}

	if n.IsNamed() {
		for _, c := range n.NamedChildren() {
			pyTypeRefs(c, generic, out)
		}
	}
}

func emitRefs(x *generic.Ctx, from string, line int, refs []typeRef, def string) {
	emitRefsSeen(x, from, line, refs, def, map[string]bool{})
}

func emitRefsSeen(x *generic.Ctx, from string, line int, refs []typeRef, def string, seen map[string]bool) {
	for _, r := range refs {
		ctx := def
		if r.role == "generic_arg" {
			ctx = "generic_arg"
		}

		key := r.name + "\x00" + ctx
		if seen[key] {
			continue
		}

		seen[key] = true
		x.Ref(from, x.EnsureNamed(r.name), line, ctx)
	}
}

func pyDecoratorName(d *tsx.Node) string {
	for _, c := range d.NamedChildren() {
		t := c
		if t.Type() == "call" {
			if f := t.Field("function"); f != nil {
				t = f
			}
		}

		switch t.Type() {
		case "attribute":
			if a := t.Field("attribute"); a != nil {
				return a.Text()
			}

			return ""
		case "identifier":
			return t.Text()
		}

		return ""
	}

	return ""
}

func probePy(cand string) string {
	if st, err := os.Stat(cand); err == nil {
		if st.IsDir() {
			if fi, err := os.Stat(filepath.Join(cand, "__init__.py")); err == nil && fi.Mode().IsRegular() {
				return filepath.Join(cand, "__init__.py")
			}
		} else if st.Mode().IsRegular() {
			return cand
		}
	}

	if filepath.Base(cand) == "" {
		return ""
	}

	py := strings.TrimSuffix(cand, filepath.Ext(cand)) + ".py"
	if filepath.Ext(cand) == "" {
		py = cand + ".py"
	}

	if st, err := os.Stat(py); err == nil && st.Mode().IsRegular() {
		return py
	}

	return ""
}

func isFile(p string) bool {
	st, err := os.Stat(p)

	return err == nil && st.Mode().IsRegular()
}

func isDir(p string) bool {
	st, err := os.Stat(p)

	return err == nil && st.IsDir()
}

// resolvePyModule mirrors Graphify's _resolve_python_module_path.
func resolvePyModule(module, current, root string, level int) string {
	if level > 0 {
		b := filepath.Dir(current)
		for i := 0; i < level-1; i++ {
			b = filepath.Dir(b)
		}

		cand := b
		if module != "" {
			cand = filepath.Join(b, strings.ReplaceAll(module, ".", "/"))
		}

		return probePy(cand)
	}

	rel := strings.ReplaceAll(module, ".", "/")
	if hit := probePy(filepath.Join(root, rel)); hit != "" {
		return hit
	}

	if stripped, ok := stripScanRootNamespace(module, root); ok {
		if hit := probePy(filepath.Join(root, strings.ReplaceAll(stripped, ".", "/"))); hit != "" {
			return hit
		}
	}

	for anc := filepath.Dir(current); ; anc = filepath.Dir(anc) {
		if r, err := filepath.Rel(root, anc); err != nil || strings.HasPrefix(r, "..") {
			break
		}

		if anc != root && !isFile(filepath.Join(anc, "__init__.py")) {
			if hit := probePy(filepath.Join(anc, rel)); hit != "" {
				return hit
			}
		}

		if anc == root || filepath.Dir(anc) == anc {
			break
		}
	}

	return ""
}

// resolvePyNamespaceDir returns the PEP 420 namespace-package directory (no
// __init__.py, inside root) a `from <module> import ...` names, or "".
//
// Adapted from Graphify's _resolve_python_namespace_dir (Apache-2.0).
func resolvePyNamespaceDir(module, current, root string, level int) string {
	ns := func(cand string) string {
		if !isDir(cand) || isFile(filepath.Join(cand, "__init__.py")) {
			return ""
		}

		if r, err := filepath.Rel(root, cand); err != nil || r == ".." || strings.HasPrefix(r, "../") {
			return ""
		}

		return cand
	}

	if level > 0 {
		b := filepath.Dir(current)
		for i := 0; i < level-1; i++ {
			b = filepath.Dir(b)
		}

		if module != "" {
			b = filepath.Join(b, strings.ReplaceAll(module, ".", "/"))
		}

		return ns(b)
	}

	if module == "" {
		return ""
	}

	rel := strings.ReplaceAll(module, ".", "/")
	if hit := ns(filepath.Join(root, rel)); hit != "" {
		return hit
	}

	if stripped, ok := stripScanRootNamespace(module, root); ok {
		if hit := ns(filepath.Join(root, strings.ReplaceAll(stripped, ".", "/"))); hit != "" {
			return hit
		}
	}

	for anc := filepath.Dir(current); ; anc = filepath.Dir(anc) {
		if r, err := filepath.Rel(root, anc); err != nil || strings.HasPrefix(r, "..") {
			break
		}

		if anc != root && !isFile(filepath.Join(anc, "__init__.py")) {
			if hit := ns(filepath.Join(anc, rel)); hit != "" {
				return hit
			}
		}

		if anc == root || filepath.Dir(anc) == anc {
			break
		}
	}

	return ""
}

var scanRootNamespaces sync.Map

// stripScanRootNamespace removes the dotted package namespace of root from an
// absolute module name when root sits inside a package chain
// (root = Team/, `Company.Apps.Team.lib` -> `lib`).
//
// Adapted from Graphify's _infer_scan_root_namespace (Apache-2.0).
func stripScanRootNamespace(module, root string) (string, bool) {
	var ns string

	if v, ok := scanRootNamespaces.Load(root); ok {
		ns = v.(string)
	} else {
		parts := []string{filepath.Base(root)}

		for cur := root; ; {
			parent := filepath.Dir(cur)
			if parent == cur || !isFile(filepath.Join(parent, "__init__.py")) {
				break
			}

			parts = append(parts, filepath.Base(parent))
			cur = parent
		}

		if len(parts) > 1 {
			for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
				parts[i], parts[j] = parts[j], parts[i]
			}

			ns = strings.Join(parts, ".")
		}

		scanRootNamespaces.Store(root, ns)
	}

	if ns == "" || (module != ns && !strings.HasPrefix(module, ns+".")) {
		return "", false
	}

	return strings.TrimPrefix(strings.TrimPrefix(module, ns), "."), true
}

func pyImport(x *generic.Ctx, n *tsx.Node) [][2]string {
	b := x.B
	root := x.Root

	switch n.Type() {
	case "import_statement":
		for _, c := range n.Children() {
			if c.Type() != "dotted_name" && c.Type() != "aliased_import" {
				continue
			}

			raw := c.Text()
			mod, alias, _ := strings.Cut(raw, " as ")
			mod = strings.TrimLeft(strings.TrimSpace(mod), ".")
			alias = strings.TrimSpace(alias)

			tp := resolvePyModule(mod, x.Path, root, 0)
			if tp == x.Path {
				tp = ""
			}

			tgt := ids.MakeID(mod)
			if tp != "" {
				tgt = ids.MakeID(tp)
			}

			local := alias
			if local == "" {
				local, _, _ = strings.Cut(mod, ".")
			}

			e := b.AddEdgeCtx(b.FileID, tgt, "imports", n.Line(), "import")
			e.LocalAlias = alias
			e.PyImport = &model.PyImport{Module: mod, Bindings: [][2]string{{mod, local}}, ModuleBinding: true, TargetFile: tp}
		}
	case "import_from_statement":
		mn := n.Field("module_name")
		if mn == nil {
			return nil
		}

		raw := mn.Text()

		var tgt string

		if strings.HasPrefix(raw, ".") {
			dots := len(raw) - len(strings.TrimLeft(raw, "."))
			mod := strings.TrimLeft(raw, ".")

			tp := resolvePyModule(mod, x.Path, root, dots)
			if tp == "" {
				bdir := filepath.Dir(x.Path)
				for i := 0; i < dots-1; i++ {
					bdir = filepath.Dir(bdir)
				}

				rel := "__init__.py"
				if mod != "" {
					rel = strings.ReplaceAll(mod, ".", "/") + ".py"
				}

				tp = filepath.Join(bdir, rel)
			}

			tgt = ids.MakeID(tp)
		} else {
			tp := resolvePyModule(raw, x.Path, root, 0)
			if tp == "" && resolvePyNamespaceDir(raw, x.Path, root, 0) != "" {
				// A namespace package has no file node; keep a marker so the
				// ambiguous-module guard still sees the bindings.
				b.Edges = append(b.Edges, &model.Edge{
					Source: b.FileID, Relation: pyImportMarker, SourceFile: x.Path,
					PyImport: &model.PyImport{Module: raw, Bindings: pyImportBindings(n), MarkerOnly: true},
				})

				return nil
			}

			if tp == x.Path {
				tp = ""
			}

			tgt = ids.MakeID(raw)
			if tp != "" {
				tgt = ids.MakeID(tp)
			}

			e := b.AddEdgeCtx(b.FileID, tgt, "imports_from", n.Line(), "import")
			e.PyImport = &model.PyImport{Module: raw, Bindings: pyImportBindings(n), TargetFile: tp}

			return nil
		}

		b.AddEdgeCtx(b.FileID, tgt, "imports_from", n.Line(), "import")
	}

	return nil
}

// pyImportMarker is the relation of a transient namespace-import marker edge.
const pyImportMarker = "_python_import_marker"

// pyImportBindings returns (imported, local) pairs, with ("*", "*") for a
// wildcard import.
//
// Adapted from Graphify's _python_import_bindings (Apache-2.0).
func pyImportBindings(n *tsx.Node) [][2]string {
	out := pyImportedNames(n)

	for _, c := range n.Children() {
		if c.Type() == "wildcard_import" {
			out = append(out, [2]string{"*", "*"})
		}
	}

	return out
}

func pyClassHook(x *generic.Ctx, n *tsx.Node, classID string, line int) {
	if args := n.Field("superclasses"); args != nil {
		for _, a := range args.Children() {
			if a.Type() == "identifier" {
				x.B.AddEdge(classID, x.EnsureNamed(a.Text()), "inherits", line)
			}
		}
	}

	pyDecorators(x, n, classID)
}

func pyDecorators(x *generic.Ctx, n *tsx.Node, owner string) {
	p := n.Parent()
	if p == nil || p.Type() != "decorated_definition" {
		return
	}

	for _, c := range p.Children() {
		if c.Type() != "decorator" {
			continue
		}

		name := pyDecoratorName(c)
		if name == "" || pyDecoratorNoise.Has(name) {
			continue
		}

		x.Ref(owner, x.EnsureNamed(name), c.Line(), "decorator")
	}
}

func pyParamNames(params *tsx.Node, out map[string]bool) {
	if params == nil {
		return
	}

	for _, c := range params.Children() {
		switch c.Type() {
		case "identifier":
			out[c.Text()] = true
		case "typed_parameter", "list_splat_pattern", "dictionary_splat_pattern":
			if id := c.ChildOfType("identifier"); id != nil {
				out[id.Text()] = true
			}

			if inner := c.ChildOfType("list_splat_pattern", "dictionary_splat_pattern"); inner != nil {
				if id := inner.ChildOfType("identifier"); id != nil {
					out[id.Text()] = true
				}
			}
		case "default_parameter", "typed_default_parameter":
			if nn := c.Field("name"); nn != nil && nn.Type() == "identifier" {
				out[nn.Text()] = true
			}
		}
	}
}

func pyAssignTargets(n *tsx.Node, out map[string]bool) {
	if n == nil {
		return
	}

	switch n.Type() {
	case "identifier":
		out[n.Text()] = true
	case "pattern_list", "tuple_pattern", "list_pattern", "list_splat_pattern", "parenthesized_expression", "tuple", "list":
		for _, c := range n.NamedChildren() {
			pyAssignTargets(c, out)
		}
	}
}

func pyLocalNames(fn *tsx.Node) map[string]bool {
	out := map[string]bool{}
	pyParamNames(fn.Field("parameters"), out)

	var walk func(n *tsx.Node)
	walk = func(n *tsx.Node) {
		for _, c := range n.Children() {
			switch c.Type() {
			case "function_definition", "class_definition", "lambda":
				continue
			case "assignment", "augmented_assignment":
				pyAssignTargets(c.Field("left"), out)
			case "for_statement", "for_in_clause":
				pyAssignTargets(c.Field("left"), out)
			case "with_statement":
				for _, wc := range c.Children() {
					if wc.Type() == "with_clause" {
						for _, wi := range wc.Children() {
							if wi.Type() == "with_item" {
								if al := wi.Field("alias"); al != nil {
									pyAssignTargets(al, out)
								}
							}
						}
					}
				}
			case "named_expression":
				pyAssignTargets(c.Field("name"), out)
			}

			walk(c)
		}
	}

	if bd := fn.Field("body"); bd != nil {
		walk(bd)
	}

	return out
}

// pyNested emits nodes for functions defined inside another function and
// records their lexical scope.
func pyNested(x *generic.Ctx, container *tsx.Node, parent string) {
	if container == nil {
		return
	}

	for _, c := range container.Children() {
		target := c
		if target.Type() == "decorated_definition" {
			if in := target.Field("definition"); in != nil {
				target = in
			}
		}

		if target.Type() != "function_definition" {
			pyNested(x, c, parent)

			continue
		}

		nn := target.Field("name")
		if nn == nil || ids.NormalizeID(nn.Text()) == "" {
			continue
		}

		name := nn.Text()
		id := ids.MakeID(parent, name)
		x.B.AddNode(id, name+"()", target.Line())
		x.B.AddEdge(parent, id, "contains", target.Line())
		x.MarkCallable(id, false)
		x.LocalNames[id] = pyLocalNames(target)
		x.ScopeParents[id] = parent

		if x.LexicalScope[parent] == nil {
			x.LexicalScope[parent] = map[string]string{}
		}

		if x.LexicalScope[id] == nil {
			x.LexicalScope[id] = map[string]string{}
		}

		x.LexicalScope[parent][name] = id
		x.LexicalScope[id][name] = id

		if bd := target.Field("body"); bd != nil {
			x.AddBody(id, bd)
			pyNested(x, bd, id)
		}
	}
}

func pyFunctionHook(x *generic.Ctx, n *tsx.Node, funcID string, line int) {
	x.LocalNames[funcID] = pyLocalNames(n)
	pyNested(x, n.Field("body"), funcID)

	var refs []typeRef
	seen := map[string]bool{}

	if params := n.Field("parameters"); params != nil {
		for _, c := range params.Children() {
			if c.Type() == "typed_parameter" || c.Type() == "typed_default_parameter" {
				pyTypeRefs(c.Field("type"), false, &refs)
			}
		}
	}

	emitRefsSeen(x, funcID, line, refs, "parameter_type", seen)

	refs = nil
	pyTypeRefs(n.Field("return_type"), false, &refs)
	emitRefsSeen(x, funcID, line, refs, "return_type", seen)

	pyDecorators(x, n, funcID)
}

var pyRationalePrefixes = []string{"# NOTE:", "# IMPORTANT:", "# HACK:", "# WHY:", "# RATIONALE:", "# TODO:", "# FIXME:"}

var (
	alembicRevision = regexp.MustCompile(`(?m)^revision\s*[:=]`)
)

func pyAutogenerated(src []byte) bool {
	head := string(src)
	if len(head) > 2048 {
		head = head[:2048]
	}

	for _, m := range []string{"DO NOT EDIT", "@generated", "Generated by the protocol buffer"} {
		if strings.Contains(head, m) {
			return true
		}
	}

	if alembicRevision.MatchString(head) && strings.Contains(head, "def upgrade(") && strings.Contains(head, "down_revision") {
		return true
	}

	return strings.Contains(head, "class Migration(migrations.Migration)") && strings.Contains(head, "operations")
}

func addRationale(x *generic.Ctx, text string, line int, parent string) {
	b := x.B
	rid := ids.MakeID(b.Stem, "rationale", itoa(line))

	if !b.Has(rid) {
		n := b.AddNode(rid, ShortenLabel(text, 80), line)
		n.FileType = model.FileTypeRationale
	}

	b.AddEdge(rid, parent, "rationale_for", line)
}

func pyDocstring(bodyNode *tsx.Node) (string, int, bool) {
	if bodyNode == nil {
		return "", 0, false
	}

	for _, c := range bodyNode.Children() {
		if c.Type() == "comment" {
			continue
		}

		if c.Type() == "string" || c.Type() == "concatenated_string" {
			t := strings.TrimSpace(strings.Trim(c.Text(), `"'`))
			if len([]rune(t)) > 20 {
				return t, c.Line(), true
			}

			break
		}

		if c.Type() == "expression_statement" {
			for _, s := range c.Children() {
				if s.Type() == "string" || s.Type() == "concatenated_string" {
					t := strings.TrimSpace(strings.Trim(s.Text(), `"'`))
					if len([]rune(t)) > 20 {
						return t, c.Line(), true
					}
				}
			}
		}

		break
	}

	return "", 0, false
}

func pyRationale(x *generic.Ctx) {
	b := x.B
	root := x.Tree.Root

	if !pyAutogenerated(x.Tree.Src) {
		if t, l, ok := pyDocstring(root); ok {
			addRationale(x, t, l, b.FileID)
		}
	}

	var walk func(n *tsx.Node, parent string)
	walk = func(n *tsx.Node, parent string) {
		switch n.Type() {
		case "class_definition":
			nn, bd := n.Field("name"), n.Field("body")
			if nn != nil && bd != nil {
				id := ids.MakeID(b.Stem, nn.Text())
				if t, l, ok := pyDocstring(bd); ok {
					addRationale(x, t, l, id)
				}

				for _, c := range bd.Children() {
					walk(c, id)
				}
			}

			return
		case "function_definition":
			nn, bd := n.Field("name"), n.Field("body")
			if nn != nil && bd != nil {
				id := ids.MakeID(b.Stem, nn.Text())
				if parent != b.FileID {
					id = ids.MakeID(parent, nn.Text())
				}

				if t, l, ok := pyDocstring(bd); ok {
					addRationale(x, t, l, id)
				}
			}

			return
		}

		for _, c := range n.Children() {
			walk(c, parent)
		}
	}

	walk(root, b.FileID)

	for i, line := range strings.Split(string(x.Tree.Src), "\n") {
		s := strings.TrimSpace(line)
		for _, p := range pyRationalePrefixes {
			if strings.HasPrefix(s, p) {
				addRationale(x, s, i+1, b.FileID)

				break
			}
		}
	}
}

// pyUnderscoreGroups returns ids for names that collapse to the same id once
// leading/trailing underscores are stripped.
func pyUnderscoreGroups(root *tsx.Node, stem string) map[string]map[string]bool {
	groups := map[string]map[string]bool{}
	rec := func(plain, name string) {
		if groups[plain] == nil {
			groups[plain] = map[string]bool{}
		}

		groups[plain][name] = true
	}

	for _, c := range root.Children() {
		switch c.Type() {
		case "function_definition":
			if nn := c.Field("name"); nn != nil {
				rec(ids.MakeID(stem, nn.Text()), nn.Text())
			}
		case "class_definition":
			cn, bd := c.Field("name"), c.Field("body")
			if cn == nil || bd == nil {
				continue
			}

			cid := ids.MakeID(stem, cn.Text())

			for _, m := range bd.Children() {
				if m.Type() != "function_definition" {
					continue
				}

				if nn := m.Field("name"); nn != nil {
					rec(ids.MakeID(cid, nn.Text()), nn.Text())
				}
			}
		}
	}

	for k, v := range groups {
		if len(v) < 2 {
			delete(groups, k)
		}
	}

	return groups
}

func pySalt(plain, name string, groups map[string]map[string]bool) string {
	names := groups[plain]
	if len(names) < 2 {
		return plain
	}

	var public []string

	for n := range names {
		if !strings.HasPrefix(n, "_") {
			public = append(public, n)
		}
	}

	if len(public) == 1 && name == public[0] {
		return plain
	}

	sum := sha1.Sum([]byte(name)) //nolint:gosec // id salt

	return ids.MakeID(plain, hex.EncodeToString(sum[:])[:6])
}

var pythonConfig = &generic.Config{
	Lang:                 "python",
	Grammar:              "python",
	ClassTypes:           base.NewSet("class_definition"),
	FunctionTypes:        base.NewSet("function_definition"),
	ImportTypes:          base.NewSet("import_statement", "import_from_statement"),
	CallTypes:            base.NewSet("call"),
	CallFunctionField:    "function",
	CallAccessorTypes:    base.NewSet("attribute"),
	CallAccessorField:    "attribute",
	CallAccessorObjField: "object",
	FunctionBoundary:     base.NewSet("function_definition"),
	ImportHandler:        pyImport,
	ClassHook:            pyClassHook,
	FunctionHook:         pyFunctionHook,
	Rationale:            pyRationale,
	IndirectRefs:         pyIndirectRefs,
	ModuleIndirect:       pyModuleIndirect,
	DeferMember: func(member bool, receiver string) bool {
		return member && receiver != "self" && receiver != "cls" && receiver != "super"
	},
	PreScan: func(x *generic.Ctx) {
		x.ScopeParents = map[string]string{}
		x.LexicalScope = map[string]map[string]string{}
		x.LocalNames = map[string]map[string]bool{}

		groups := pyUnderscoreGroups(x.Tree.Root, x.B.Stem)
		if len(groups) > 0 {
			x.SymbolID = func(plain, name string) string { return pySalt(plain, name, groups) }
		}
	},
}

// ExtractPython extracts a Python file.
func ExtractPython(path, root string, src []byte) *model.Extraction {
	return generic.Extract(pythonConfig, path, root, src)
}

func itoa(i int) string { return strconv.Itoa(i) }
