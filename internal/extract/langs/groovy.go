package langs

import (
	"regexp"
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// Groovy extraction over gotreesitter's Groovy grammar, reproducing what
// Graphify's Java-shaped extractor emits on the reference tree-sitter-groovy
// grammar: classes/interfaces, methods and constructors, heritage, class
// annotations, imports and in-body calls (all non-member). Spock specs use
// Graphify's line-based fallback.
//
// Adapted from Graphify's extract_groovy / _extract_spock_fallback
// (Apache-2.0).

var (
	groovySpockFeatureRe = regexp.MustCompile(`(?m)^\s*def\s+["']`)
	groovySpockClassRe   = regexp.MustCompile(`^\s*(?:[\w@]+\s+)*class\s+(\w+)`)
	groovySpockMethodRe  = regexp.MustCompile(`^\s*def\s+(?:"([^"]+)"|'([^']+)')\s*\(`)
	groovySpockPlainRe   = regexp.MustCompile(`^\s*def\s+(\w+)\s*\(`)
	groovyModifierWords  = base.NewSet("abstract", "final", "public", "private", "protected", "static", "sealed")
)

// ExtractGroovy extracts a Groovy or Gradle file.
func ExtractGroovy(path, _ string, src []byte) *model.Extraction {
	tree, err := tsx.Parse("groovy", src)
	if err != nil {
		return &model.Extraction{Error: err.Error()}
	}
	defer tree.Release()

	b := base.NewBuilder(path)
	b.AddFileNode()

	g := &groovyExtractor{b: b, path: path, callable: map[string]bool{}, classes: map[string]bool{}}

	for _, c := range tree.Root.Children() {
		if c.Type() == "groovy_import" {
			g.importDecl(c)
		}
	}

	g.walkScope(tree.Root, "")

	if groovySpockFeatureRe.Match(src) {
		return groovySpockFallback(b, path, src)
	}

	g.walkCalls()

	res := b.Result()
	for _, n := range res.Nodes {
		if g.callable[n.ID] {
			n.Callable = true
			n.CallableClass = g.classes[n.ID]
		}
	}

	res.ParseErrors = tree.Root.HasError()

	return res
}

type groovyBody struct {
	caller string
	node   *tsx.Node
}

type groovyExtractor struct {
	b        *base.Builder
	path     string
	bodies   []groovyBody
	callable map[string]bool
	classes  map[string]bool
}

func (g *groovyExtractor) importDecl(n *tsx.Node) {
	q := n.Field("import")
	if q == nil {
		q = n.ChildOfType("qualified_name", "identifier")
	}

	if q == nil {
		return
	}

	segs := strings.Split(strings.ReplaceAll(q.Text(), " ", ""), ".")

	mod := strings.Trim(strings.Trim(segs[len(segs)-1], "*"), ".")
	if mod == "" && len(segs) > 1 {
		mod = segs[len(segs)-2]
	}

	if mod != "" {
		g.b.AddEdgeCtx(g.b.FileID, ids.MakeID(mod), "imports", n.Line(), "import")
	}
}

// walkScope visits the children of a file or class body.
func (g *groovyExtractor) walkScope(scope *tsx.Node, parentClass string) {
	kids := scope.Children()

	for i, c := range kids {
		switch c.Type() {
		case "class_definition":
			anns, line := groovyLeadingAnnotations(kids[:i])
			g.classDef(c, parentClass, anns, line)
		case "function_definition", "function_declaration":
			if parentClass != "" {
				g.method(c, parentClass)
			}
		case "function_call":
			if parentClass != "" {
				g.closureConstructor(c, parentClass)
			}
		case "ERROR":
			if parentClass != "" && i+1 < len(kids) && kids[i+1].Type() == "closure" {
				g.errorConstructor(c, kids[i+1], parentClass)
			}
		}
	}
}

// groovyLeadingAnnotations returns annotations and the first line of
// `@Ann abstract class X` split by gotreesitter into a preceding
// `declaration` plus modifier identifiers.
func groovyLeadingAnnotations(prev []*tsx.Node) ([]*tsx.Node, int) {
	var anns []*tsx.Node

	line := 0

	for i := len(prev) - 1; i >= 0; i-- {
		p := prev[i]

		if p.Type() == "identifier" && groovyModifierWords.Has(p.Text()) {
			line = p.Line()

			continue
		}

		if p.Type() == "declaration" {
			var a []*tsx.Node

			only := true

			for _, c := range p.NamedChildren() {
				if c.Type() == "annotation" {
					a = append(a, c)
				} else if c.Text() != "" {
					only = false
				}
			}

			if only && len(a) > 0 {
				anns = append(a, anns...)
				line = p.Line()

				continue
			}
		}

		break
	}

	return anns, line
}

func (g *groovyExtractor) classDef(n *tsx.Node, parentClass string, lead []*tsx.Node, leadLine int) {
	nn := n.Field("name")
	if nn == nil {
		return
	}

	name := nn.Text()
	if ids.NormalizeID(name) == "" {
		return
	}

	b := g.b
	line := n.Line()

	if leadLine > 0 && leadLine < line {
		line = leadLine
	}

	classID := ids.MakeID(b.Stem, name)
	b.AddNode(classID, name, line)
	g.callable[classID], g.classes[classID] = true, true

	if parentClass != "" && parentClass != classID {
		b.AddEdge(parentClass, classID, "contains", line)
	} else {
		b.AddEdge(b.FileID, classID, "contains", line)
	}

	g.heritage(n, classID, line)

	seen := map[string]bool{}

	anns := append(append([]*tsx.Node{}, lead...), n.Children()...)
	for _, a := range anns {
		if a.Type() != "annotation" {
			continue
		}

		an := a.ChildOfType("identifier", "qualified_name", "dotted_identifier")
		if an == nil {
			continue
		}

		tgt := b.EnsureNamed(b.Stem, lastSeg(an.Text(), "."))
		if tgt != classID && !seen[tgt] {
			b.AddEdgeCtx(classID, tgt, "references", line, "attribute")
			seen[tgt] = true
		}
	}

	if bd := n.Field("body"); bd != nil {
		g.walkScope(bd, classID)
	}
}

// heritage reads `extends`/`implements` from the header text: gotreesitter
// leaves generic or multi-type clauses in ERROR nodes.
func (g *groovyExtractor) heritage(n *tsx.Node, classID string, line int) {
	start := n.Field("name").EndByte()

	end := n.EndByte()
	if bd := n.Field("body"); bd != nil {
		end = bd.StartByte()
	}

	if end <= start {
		return
	}

	header := strings.TrimSpace(n.Text()[start-n.StartByte() : end-n.StartByte()])

	// Class type parameters (`class A<T> extends B<T>`) are not base types.
	skip := base.NewSet()

	if strings.HasPrefix(header, "<") {
		depth := 0

		for i, r := range header {
			if r == '<' {
				depth++
			} else if r == '>' {
				depth--
				if depth == 0 {
					for _, p := range strings.Split(header[1:i], ",") {
						if f := strings.Fields(p); len(f) > 0 {
							skip[f[0]] = struct{}{}
						}
					}

					header = header[i+1:]

					break
				}
			}
		}
	}

	isInterface := n.ChildOfType("interface") != nil

	for _, clause := range groovyHeritageClauses(header) {
		rel := "inherits"
		if clause.keyword == "implements" {
			rel = "implements"
		}

		for _, typ := range clause.types {
			var refs []typeRef
			groovyTypeRefs(typ, false, &refs, skip)

			parentDone := false

			for _, r := range refs {
				switch {
				case r.role == "type" && !parentDone:
					tgt := g.ensureBase(r.name)
					if tgt != classID {
						g.b.AddEdge(classID, tgt, rel, line)
					}

					parentDone = true
				case r.role == "generic_arg":
					if tgt := g.b.EnsureNamed(g.b.Stem, r.name); tgt != classID {
						g.b.AddEdgeCtx(classID, tgt, "references", line, "generic_arg")
					}
				}
			}

			if rel == "inherits" && !isInterface {
				break
			}
		}
	}
}

func (g *groovyExtractor) ensureBase(name string) string {
	id := ids.MakeID(g.b.Stem, name)
	if g.b.Has(id) {
		return id
	}

	id = ids.MakeID(name)
	if !g.b.Has(id) {
		// Heritage bases carry no origin_file (Graphify's Java path), so
		// same-named bases across files stay one node.
		g.b.AddStub(id, name).OriginFile = ""
	}

	return id
}

type groovyClause struct {
	keyword string
	types   []string
}

func groovyHeritageClauses(header string) []groovyClause {
	var (
		out   []groovyClause
		cur   *groovyClause
		depth int
		tok   strings.Builder
	)

	flush := func() {
		t := strings.TrimSpace(tok.String())
		tok.Reset()

		switch {
		case t == "":
		case depth == 0 && (t == "extends" || t == "implements"):
			out = append(out, groovyClause{keyword: t})
			cur = &out[len(out)-1]
		case cur != nil:
			if n := len(cur.types); n > 0 && !strings.HasSuffix(cur.types[n-1], ",") &&
				strings.Count(cur.types[n-1], "<") > strings.Count(cur.types[n-1], ">") {
				cur.types[n-1] += t
			} else {
				cur.types = append(cur.types, t)
			}
		}
	}

	for _, r := range header {
		switch {
		case r == '<':
			depth++

			tok.WriteRune(r)
		case r == '>':
			depth--

			tok.WriteRune(r)
		case depth == 0 && (r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			flush()
		case depth > 0 && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
		default:
			tok.WriteRune(r)
		}
	}

	flush()

	for i := range out {
		var clean []string

		for _, t := range out[i].types {
			if t = strings.Trim(t, ","); t != "" {
				clean = append(clean, t)
			}
		}

		out[i].types = clean
	}

	return out
}

// groovyTypeRefs mirrors Graphify's _java_collect_type_refs on a textual
// type (`a.b.Base<String, Q>`): the simple base name, then generic args.
func groovyTypeRefs(typ string, generic bool, out *[]typeRef, skip base.Set) {
	typ = strings.TrimSpace(strings.TrimSuffix(typ, "[]"))
	if typ == "" || typ == "?" {
		return
	}

	baseName, args := typ, ""
	if i := strings.Index(typ, "<"); i >= 0 && strings.HasSuffix(typ, ">") {
		baseName, args = typ[:i], typ[i+1:len(typ)-1]
	}

	simple := lastSeg(baseName, ".")
	if simple != "" && ids.NormalizeID(simple) != "" && !javaBuiltinTypes.Has(simple) &&
		(strings.Contains(baseName, ".") || !skip.Has(simple)) {
		role := "type"
		if generic {
			role = "generic_arg"
		}

		*out = append(*out, typeRef{name: simple, role: role})
	}

	depth, last := 0, 0

	for i, r := range args {
		switch r {
		case '<':
			depth++
		case '>':
			depth--
		case ',':
			if depth == 0 {
				groovyTypeRefs(args[last:i], true, out, skip)
				last = i + 1
			}
		}
	}

	if args != "" {
		groovyTypeRefs(args[last:], true, out, skip)
	}
}

func (g *groovyExtractor) addMethod(name, parentClass string, line int) string {
	if ids.NormalizeID(name) == "" {
		return ""
	}

	id := ids.MakeID(parentClass, name)
	g.b.AddNode(id, "."+name+"()", line)
	g.b.AddEdge(parentClass, id, "method", line)
	g.callable[id] = true

	return id
}

func (g *groovyExtractor) method(n *tsx.Node, parentClass string) {
	fn := n.Field("function")
	if fn == nil {
		return
	}

	id := g.addMethod(fn.Text(), parentClass, n.Line())
	if id == "" {
		return
	}

	// `public Foo() {}` then `Foo(int a) {}`: gotreesitter merges both
	// constructors into one definition (type Foo, function Foo); the
	// reference grammar yields one method edge per declaration line.
	if tn := n.Field("type"); tn != nil && tn.Text() == fn.Text() && fn.Line() != n.Line() {
		g.b.AddEdge(parentClass, id, "method", fn.Line())
	}

	if bd := n.Field("body"); bd != nil {
		g.bodies = append(g.bodies, groovyBody{id, bd})
	}
}

// closureConstructor handles `Foo(args) { ... }` in a class body, which
// gotreesitter reads as a call whose last argument is a closure.
func (g *groovyExtractor) closureConstructor(n *tsx.Node, parentClass string) {
	fn, args := n.Field("function"), n.Field("args")
	if fn == nil || args == nil || fn.Type() != "identifier" {
		return
	}

	cls := g.b.Get(parentClass)
	if cls == nil || cls.Label != fn.Text() {
		return
	}

	var body *tsx.Node

	for _, c := range args.NamedChildren() {
		if c.Type() == "closure" {
			body = c
		}
	}

	if body == nil {
		return
	}

	if id := g.addMethod(fn.Text(), parentClass, n.Line()); id != "" {
		g.bodies = append(g.bodies, groovyBody{id, body})
	}
}

// errorConstructor handles `public Foo() {}`, which gotreesitter splits into
// an ERROR (modifiers, name, parens) followed by the body closure.
func (g *groovyExtractor) errorConstructor(n, body *tsx.Node, parentClass string) {
	cls := g.b.Get(parentClass)
	if cls == nil {
		return
	}

	var name *tsx.Node

	for _, c := range n.NamedChildren() {
		switch c.Type() {
		case "access_modifier", "modifier":
		case "identifier":
			name = c
		default:
			return
		}
	}

	if name == nil || name.Text() != cls.Label || n.ChildOfType("(") == nil {
		return
	}

	if id := g.addMethod(name.Text(), parentClass, n.Line()); id != "" {
		g.bodies = append(g.bodies, groovyBody{id, body})
	}
}

// groovyCallee returns the callee name of a call-like node.
func groovyCallee(fn *tsx.Node) string {
	switch fn.Type() {
	case "identifier":
		return fn.Text()
	case "dotted_identifier":
		kids := fn.NamedChildren()
		if len(kids) > 0 && kids[len(kids)-1].Type() == "identifier" {
			return kids[len(kids)-1].Text()
		}
	}

	return ""
}

// groovyNewTarget returns the constructor call of `new X(...)...`.
func groovyNewTarget(n *tsx.Node) *tsx.Node {
	cur := n.ChildOfType("function_call", "dotted_identifier")

	for cur != nil {
		switch cur.Type() {
		case "function_call":
			fn := cur.Field("function")
			if fn != nil && fn.Type() == "dotted_identifier" {
				cur = fn

				continue
			}

			return cur
		case "dotted_identifier":
			kids := cur.NamedChildren()
			if len(kids) == 0 {
				return nil
			}

			cur = kids[0]
		default:
			return nil
		}
	}

	return nil
}

func (g *groovyExtractor) walkCalls() {
	b := g.b

	labelToID := map[string]string{}
	for _, n := range b.Nodes {
		labelToID[strings.TrimLeft(strings.Trim(n.Label, "()"), ".")] = n.ID
	}

	seen := map[[2]string]bool{}
	skipped := map[[2]uint32]bool{}

	emit := func(caller, callee string, line int) {
		if callee == "" || base.BuiltinGlobals.Has(callee) {
			return
		}

		if tgt := labelToID[callee]; tgt != "" {
			if t := b.Get(tgt); t != nil && t.SourceFile != "" {
				if pair := [2]string{caller, tgt}; !seen[pair] {
					seen[pair] = true
					b.AddEdgeCtx(caller, tgt, "calls", line, "call")
				}

				return
			}
		}

		b.RawCalls = append(b.RawCalls, &model.RawCall{
			CallerID: caller, Callee: callee, Language: "groovy",
			SourceFile: g.path, SourceLocation: base.Loc(line),
		})
	}

	var walk func(n *tsx.Node, caller string)
	walk = func(n *tsx.Node, caller string) {
		switch n.Type() {
		case "class_definition", "function_definition", "function_declaration":
			return
		case "unary_op":
			if n.ChildOfType("new") != nil {
				if t := groovyNewTarget(n); t != nil {
					skipped[[2]uint32{t.StartByte(), t.EndByte()}] = true
				}
			}
		case "function_call", "juxt_function_call":
			fn := n.Field("function")
			key := [2]uint32{n.StartByte(), n.EndByte()}

			// A bare `name arg` juxtaposition is not a method invocation in
			// the reference grammar.
			if fn != nil && !skipped[key] && (n.Type() == "function_call" || fn.Type() == "dotted_identifier") {
				emit(caller, groovyCallee(fn), n.Line())
			}
		case "dotted_identifier":
			// `recv.m { ... }`: a trailing-closure call.
			if nx := groovyNextNamed(n); nx != nil && nx.Type() == "closure" &&
				n.Parent() != nil && n.Parent().Type() == "closure" {
				emit(caller, groovyCallee(n), n.Line())
			}
		}

		for _, c := range n.Children() {
			walk(c, caller)
		}
	}

	for _, bd := range g.bodies {
		walk(bd.node, bd.caller)
	}
}

func groovyNextNamed(n *tsx.Node) *tsx.Node {
	p := n.Parent()
	if p == nil {
		return nil
	}

	kids := p.NamedChildren()
	for i, k := range kids {
		if k.Same(n) && i+1 < len(kids) {
			return kids[i+1]
		}
	}

	return nil
}

// groovySpockFallback rebuilds a Spock spec from its lines: classes and
// `def "feature"()` / `def name()` methods, keeping only the tree pass's file
// node and import edges.
func groovySpockFallback(tb *base.Builder, path string, src []byte) *model.Extraction {
	b := base.NewBuilder(path)
	b.AddFileNode()

	for _, e := range tb.Edges {
		if e.Context == "import" {
			b.Edges = append(b.Edges, e)
		}
	}

	cur := ""

	for i, ln := range strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n") {
		line := i + 1

		if m := groovySpockClassRe.FindStringSubmatch(ln); m != nil {
			cur = ids.MakeID(b.Stem, m[1])
			b.AddNode(cur, m[1], line)
			b.AddEdge(b.FileID, cur, "contains", line)

			continue
		}

		if cur == "" {
			continue
		}

		if m := groovySpockMethodRe.FindStringSubmatch(ln); m != nil {
			name := m[1]
			if name == "" {
				name = m[2]
			}

			id := ids.MakeID(cur, name)
			b.AddNode(id, `"`+name+`"`, line)
			b.AddEdge(cur, id, "method", line)

			continue
		}

		if m := groovySpockPlainRe.FindStringSubmatch(ln); m != nil {
			switch m[1] {
			case "if", "while", "for", "switch", "catch":
			default:
				id := ids.MakeID(cur, m[1])
				b.AddNode(id, "."+m[1]+"()", line)
				b.AddEdge(cur, id, "method", line)
			}
		}
	}

	return b.Result()
}
