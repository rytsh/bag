// Package generic is the config-driven tree-sitter extractor shared by most
// class/function languages.
//
// Adapted from Graphify's _extract_generic (graphify/extractors/engine.py,
// Apache-2.0). Language-specific behaviour plugs in through Config hooks.
package generic

import (
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// Config drives the generic extractor for one language.
type Config struct {
	Lang    string // language family key used by hooks ("python", "java", ...)
	Grammar string // gotreesitter grammar name

	ClassTypes           base.Set
	FunctionTypes        base.Set
	ImportTypes          base.Set
	CallTypes            base.Set
	FunctionBoundary     base.Set
	CallAccessorTypes    base.Set
	NameField            string
	NameFallback         []string
	BodyField            string
	BodyFallback         []string
	CallFunctionField    string
	CallAccessorField    string
	CallAccessorObjField string

	// ImportHandler handles import nodes. It may return (id,label) module
	// anchors that should become type=module nodes.
	ImportHandler func(x *Ctx, n *tsx.Node) [][2]string
	// ResolveFunctionName extracts a function name from its declarator
	// (C/C++).
	ResolveFunctionName func(declarator *tsx.Node) string
	// SanitizeName rewrites symbol names for ids (Ruby `save!`).
	SanitizeName func(string) string
	// ClassHook emits heritage/type edges for a class node.
	ClassHook func(x *Ctx, n *tsx.Node, classID string, line int)
	// FunctionHook emits param/return reference edges for a function node.
	FunctionHook func(x *Ctx, n *tsx.Node, funcID string, line int)
	// ExtraWalk handles language-specific node types. Returning true stops
	// the default handling for n.
	ExtraWalk func(x *Ctx, n *tsx.Node, parentClass string) bool
	// CallName extracts (callee, isMember, receiver) for a call node. When
	// nil the generic field/accessor logic is used.
	CallName func(x *Ctx, n *tsx.Node) (callee string, member bool, receiver string)
	// DeferMember reports whether a member call must never bind in-file.
	DeferMember func(member bool, receiver string) bool
	// PreprocessSource may rewrite the source before parsing.
	PreprocessSource func(src []byte) []byte
	// FunctionLabelNoParens renders function labels without "()".
	FunctionLabelNoParens bool
	// Rationale extracts NOTE/WHY comments.
	Rationale func(x *Ctx)
	// NoNewlineFix disables appending a trailing newline.
	NoNewlineFix bool
	// BodyNode is invoked for every node visited by the call walker.
	BodyNode func(x *Ctx, n *tsx.Node, caller string)
	// IndirectRefs yields identifier nodes referenced as values under n
	// (call arguments, dispatch-table values, assignment RHS) with a context.
	IndirectRefs func(x *Ctx, n *tsx.Node) []IndirectRef
	// ModuleIndirect scans module-level code for indirect references.
	ModuleIndirect func(x *Ctx)
	// PreScan runs before the main walk (collision pre-scans, tables).
	PreScan func(x *Ctx)
	// PostProcess runs after call extraction on the final result.
	PostProcess func(x *Ctx, res *model.Extraction)
}

// Ctx is the per-file extraction state exposed to hooks.
type Ctx struct {
	Cfg  *Config
	B    *base.Builder
	Tree *tsx.Tree
	Path string
	Root string

	// ParentClass is the enclosing class id while a ClassHook runs.
	ParentClass string

	// ScopeParents maps a nested function id to its enclosing scope id and
	// LexicalScope maps scope id -> bare name -> nested function id, so bare
	// calls resolve to the lexically closest definition first.
	ScopeParents map[string]string
	LexicalScope map[string]map[string]string
	// LocalNames holds names bound locally per function (params/locals); a
	// bare call to one is a call through a local value, not a definition.
	LocalNames map[string]map[string]bool
	// ExternalImports are names bound by imports of external modules.
	ExternalImports map[string]bool

	// SymbolID, when set, rewrites a plain function id (collision salting).
	SymbolID func(plain, name string) string
	// Data holds language-specific per-file state.
	Data map[string]any

	NamespaceStack []string
	ScopeStack     []string

	bodies       []body
	caller       string
	parentOf     map[string]string
	emitIndirect func(ref IndirectRef, scope string, locals map[string]bool)
	callable     map[string]bool
	callableCls  map[string]bool
	initializers []body

	// Walk re-enters the main walker (for hooks).
	Walk func(n *tsx.Node, parentClass string)
}

// IndirectRef is a function referenced by name (callback, dispatch table).
type IndirectRef struct {
	Ident   *tsx.Node
	Name    string
	Context string
	// ByName skips local-shadow filtering (getattr string names).
	ByName bool
}

type body struct {
	caller string
	node   *tsx.Node
}

// CurrentCaller is the caller id while the call walker runs.
func (x *Ctx) CurrentCaller() string {
	if x.caller == "" {
		return x.B.FileID
	}

	return x.caller
}

// AddBody registers a function body for call extraction.
func (x *Ctx) AddBody(caller string, n *tsx.Node) {
	x.bodies = append(x.bodies, body{caller, n})
}

// AddInitializer registers an expression whose calls belong to owner.
func (x *Ctx) AddInitializer(owner string, n *tsx.Node) {
	x.initializers = append(x.initializers, body{owner, n})
}

// MarkCallable marks a node id as a callable definition.
func (x *Ctx) MarkCallable(id string, isClass bool) {
	x.callable[id] = true
	if isClass {
		x.callableCls[id] = true
	}
}

// EnsureNamed resolves a referenced name to an in-file node or a stub.
func (x *Ctx) EnsureNamed(name string) string {
	id := ids.MakeID(x.B.Stem, strings.Join(x.NamespaceStack, "."), name)
	if x.B.Has(id) {
		return id
	}

	id = ids.MakeID(name)
	if !x.B.Has(id) {
		x.B.AddStub(id, name)
	}

	return id
}

// EnsureBase resolves a base type in-file (stem-scoped) or as a sourceless
// stub, the way the Python extractors do for heritage.
func (x *Ctx) EnsureBase(name string) string {
	id := ids.MakeID(x.B.Stem, name)
	if x.B.Has(id) {
		return id
	}

	id = ids.MakeID(name)
	if !x.B.Has(id) {
		x.B.AddStub(id, name)
	}

	return id
}

// Ref emits a references edge unless it is a self-reference.
func (x *Ctx) Ref(from, to string, line int, ctx string) {
	if from != to {
		x.B.AddEdgeCtx(from, to, "references", line, ctx)
	}
}

// Extract runs the generic extractor.
func Extract(cfg *Config, path, root string, src []byte) *model.Extraction {
	if cfg.PreprocessSource != nil {
		src = cfg.PreprocessSource(src)
	}

	if !cfg.NoNewlineFix && len(src) > 0 && src[len(src)-1] != '\n' {
		src = append(append([]byte(nil), src...), '\n')
	}

	tree, err := tsx.Parse(cfg.Grammar, src)
	if err != nil {
		return &model.Extraction{Error: err.Error()}
	}
	defer tree.Release()

	return run(cfg, path, root, tree)
}

func (c *Config) name(n *tsx.Node) *tsx.Node {
	field := c.NameField
	if field == "" {
		field = "name"
	}

	if nn := n.Field(field); nn != nil {
		return nn
	}

	if len(c.NameFallback) > 0 {
		return n.ChildOfType(c.NameFallback...)
	}

	return nil
}

// FindBody returns the body node of a class/function.
func (c *Config) FindBody(n *tsx.Node) *tsx.Node {
	field := c.BodyField
	if field == "" {
		field = "body"
	}

	if b := n.Field(field); b != nil {
		return b
	}

	if len(c.BodyFallback) > 0 {
		return n.ChildOfType(c.BodyFallback...)
	}

	return nil
}

func run(cfg *Config, path, root string, tree *tsx.Tree) *model.Extraction {
	b := base.NewBuilder(path)
	x := &Ctx{
		Cfg: cfg, B: b, Tree: tree, Path: path, Root: root,
		callable: map[string]bool{}, callableCls: map[string]bool{},
		Data: map[string]any{},
	}

	b.AddFileNode()

	if cfg.PreScan != nil {
		cfg.PreScan(x)
	}

	var walk func(n *tsx.Node, parentClass string)
	walk = func(n *tsx.Node, parentClass string) {
		t := n.Type()

		if cfg.ImportTypes.Has(t) {
			if cfg.ImportHandler != nil {
				for _, m := range cfg.ImportHandler(x, n) {
					if !b.Has(m[0]) {
						mn := b.AddNode(m[0], m[1], n.Line())
						mn.Type = "module"
					}
				}
			}

			if t == "export_statement" && n.ChildOfType("string") == nil {
				for _, c := range n.Children() {
					walk(c, parentClass)
				}
			}

			return
		}

		if cfg.ClassTypes.Has(t) {
			nameNode := cfg.name(n)
			if nameNode == nil {
				return
			}

			className := nameNode.Text()
			classID := ids.MakeID(b.Stem, strings.Join(x.NamespaceStack, "."), className)
			line := n.Line()
			b.AddNode(classID, className, line)
			x.MarkCallable(classID, true)

			if parentClass != "" && parentClass != classID {
				b.AddEdge(parentClass, classID, "contains", line)
			} else {
				b.AddEdge(b.FileID, classID, "contains", line)
			}

			if cfg.ClassHook != nil {
				x.ParentClass = parentClass
				cfg.ClassHook(x, n, classID, line)
				x.ParentClass = ""
			}

			if bd := cfg.FindBody(n); bd != nil {
				for _, c := range bd.Children() {
					walk(c, classID)
				}
			}

			return
		}

		if cfg.ExtraWalk != nil && !cfg.FunctionTypes.Has(t) && cfg.ExtraWalk(x, n, parentClass) {
			return
		}

		if cfg.FunctionTypes.Has(t) {
			var funcName string

			switch {
			case t == "deinit_declaration":
				funcName = "deinit"
			case t == "subscript_declaration":
				funcName = "subscript"
			case cfg.ResolveFunctionName != nil:
				if d := n.Field("declarator"); d != nil {
					funcName = cfg.ResolveFunctionName(d)
				}
			default:
				if nn := cfg.name(n); nn != nil {
					funcName = nn.Text()
				}
			}

			if funcName == "" {
				if cfg.ExtraWalk != nil {
					cfg.ExtraWalk(x, n, parentClass)
				}

				return
			}

			x.WalkFunctionNamed(n, parentClass, funcName)

			return
		}

		if t == "ERROR" || t == "decorated_definition" || t == "enum_body_declarations" {
			for _, c := range n.Children() {
				walk(c, parentClass)
			}

			return
		}

		for _, c := range n.Children() {
			walk(c, "")
		}
	}

	x.Walk = walk
	walk(tree.Root, "")

	if cfg.Rationale != nil {
		cfg.Rationale(x)
	}

	walkCalls(x)

	res := b.Result()

	for _, n := range res.Nodes {
		if x.callable[n.ID] {
			n.Callable = true
			n.CallableClass = x.callableCls[n.ID]
		}
	}

	res.ParseErrors = tree.Root.HasError()

	if cfg.PostProcess != nil {
		cfg.PostProcess(x, res)
	}

	return res
}

// WalkFunctionNamed emits a function/method node for n with the given name.
func (x *Ctx) WalkFunctionNamed(n *tsx.Node, parentClass, funcName string) string {
	cfg, b := x.Cfg, x.B

	sanitized := funcName
	if cfg.SanitizeName != nil {
		sanitized = cfg.SanitizeName(funcName)
	}

	if ids.NormalizeID(sanitized) == "" {
		return ""
	}

	line := n.Line()

	paren := "()"
	if cfg.FunctionLabelNoParens {
		paren = ""
	}

	var funcID string

	if parentClass != "" {
		funcID = x.symbolID(ids.MakeID(parentClass, sanitized), sanitized)
		b.AddNode(funcID, "."+funcName+paren, line)
		b.AddEdge(parentClass, funcID, "method", line)
	} else {
		funcID = x.symbolID(ids.MakeID(b.Stem, sanitized), sanitized)
		b.AddNode(funcID, funcName+paren, line)
		b.AddEdge(b.FileID, funcID, "contains", line)
	}

	x.MarkCallable(funcID, false)

	if x.parentOf == nil {
		x.parentOf = map[string]string{}
	}

	x.parentOf[funcID] = parentClass

	if cfg.FunctionHook != nil {
		cfg.FunctionHook(x, n, funcID, line)
	}

	if bd := cfg.FindBody(n); bd != nil {
		x.AddBody(funcID, bd)
	}

	return funcID
}

// EmitIndirect emits (or defers) an indirect reference from scope.
func (x *Ctx) EmitIndirect(ref IndirectRef, scope string, locals map[string]bool) {
	if x.emitIndirect != nil {
		x.emitIndirect(ref, scope, locals)
	}
}

// ParentOf returns the enclosing class id of an emitted function.
func (x *Ctx) ParentOf(funcID string) string { return x.parentOf[funcID] }

func (x *Ctx) symbolID(plain, name string) string {
	if x.SymbolID != nil {
		return x.SymbolID(plain, name)
	}

	return plain
}

func walkCalls(x *Ctx) {
	cfg := x.Cfg
	b := x.B

	labelToID := map[string]string{}

	for _, n := range b.Nodes {
		if n.Type == "namespace" {
			continue
		}

		norm := strings.TrimLeft(strings.Trim(n.Label, "()"), ".")
		if _, nested := x.ScopeParents[n.ID]; nested {
			if _, ok := labelToID[norm]; !ok {
				labelToID[norm] = n.ID
			}

			continue
		}

		labelToID[norm] = n.ID
	}

	nidSourced := func(id string) bool {
		n := b.Get(id)

		return n != nil && n.SourceFile != ""
	}

	seen := map[[2]string]bool{}
	seenIndirect := map[[2]string]bool{}

	nidSF := func(id string) string {
		if n := b.Get(id); n != nil {
			return n.SourceFile
		}

		return ""
	}

	x.emitIndirect = func(ref IndirectRef, scope string, locals map[string]bool) {
		name := ref.Name
		if !ref.ByName {
			if locals[name] || name == "self" || name == "cls" {
				return
			}

			if x.ExternalImports[name] {
				return
			}
		}

		refID := labelToID[name]
		if refID == "" || (!x.callable[refID] && nidSF(refID) != x.Path) {
			b.RawCalls = append(b.RawCalls, &model.RawCall{
				CallerID: scope, Callee: name, Indirect: true, Context: ref.Context,
				Language: cfg.Lang, SourceFile: x.Path, SourceLocation: base.Loc(ref.Ident.Line()),
			})

			return
		}

		if refID == scope || !x.callable[refID] || x.callableCls[refID] {
			return
		}

		pair := [2]string{scope, refID}
		if seen[pair] || seenIndirect[pair] {
			return
		}

		seenIndirect[pair] = true
		e := b.AddEdgeCtx(scope, refID, "indirect_call", ref.Ident.Line(), ref.Context)
		e.Confidence = model.Inferred
		e.ConfidenceScore = model.Score(0.85)
	}

	var walkC func(n *tsx.Node, caller string)
	walkC = func(n *tsx.Node, caller string) {
		t := n.Type()
		if cfg.FunctionBoundary.Has(t) {
			return
		}

		x.caller = caller

		if cfg.BodyNode != nil {
			cfg.BodyNode(x, n, caller)
		}

		if cfg.CallTypes.Has(t) {
			var (
				callee   string
				member   bool
				receiver string
			)

			if cfg.CallName != nil {
				callee, member, receiver = cfg.CallName(x, n)
			} else {
				callee, member, receiver = DefaultCallName(cfg, n)
			}

			builtinMember := member && base.BuiltinGlobals.Has(callee)
			if callee != "" && (!base.BuiltinGlobals.Has(callee) || builtinMember) {
				tgt := ""

				deferred := builtinMember
				if !deferred && cfg.DeferMember != nil {
					deferred = cfg.DeferMember(member, receiver)
				}

				if !deferred && member && receiver != "" && isUpper(receiver) {
					deferred = true
				}

				if !deferred {
					if !member && x.LexicalScope != nil {
						for sc := caller; sc != "" && tgt == ""; sc = x.ScopeParents[sc] {
							tgt = x.LexicalScope[sc][callee]
						}
					}

					if tgt == "" {
						tgt = labelToID[callee]
					}
				}

				if tgt != "" && nidSourced(tgt) {
					pair := [2]string{caller, tgt}
					if !seen[pair] {
						seen[pair] = true
						b.AddEdgeCtx(caller, tgt, "calls", n.Line(), "call")
					}
				} else if tgt == "" && !(cfg.Lang == "python" && !member && x.LocalNames[caller][callee]) {
					b.RawCalls = append(b.RawCalls, &model.RawCall{
						CallerID:       caller,
						Callee:         callee,
						IsMemberCall:   member,
						Receiver:       receiver,
						Language:       cfg.Lang,
						SourceFile:     x.Path,
						SourceLocation: base.Loc(n.Line()),
					})
				}
			}
		}

		if cfg.IndirectRefs != nil {
			for _, ref := range cfg.IndirectRefs(x, n) {
				x.emitIndirect(ref, caller, x.LocalNames[caller])
			}
		}

		for _, c := range n.Children() {
			walkC(c, caller)
		}
	}

	for _, bd := range x.bodies {
		walkC(bd.node, bd.caller)
	}

	if cfg.ModuleIndirect != nil {
		cfg.ModuleIndirect(x)
	}

	for _, in := range x.initializers {
		walkC(in.node, in.caller)
	}
}

// DefaultCallName extracts a callee using the config's field/accessor rules.
func DefaultCallName(cfg *Config, n *tsx.Node) (string, bool, string) {
	var fn *tsx.Node
	if cfg.CallFunctionField != "" {
		fn = n.Field(cfg.CallFunctionField)
	}

	if fn == nil && n.Type() == "new_expression" {
		fn = n.Field("constructor")
	}

	if fn == nil {
		return "", false, ""
	}

	if fn.Type() == "identifier" {
		return fn.Text(), false, ""
	}

	if cfg.CallAccessorTypes.Has(fn.Type()) {
		callee, recv := "", ""

		if cfg.CallAccessorField != "" {
			if a := fn.Field(cfg.CallAccessorField); a != nil {
				callee = a.Text()
			}
		}

		if cfg.CallAccessorObjField != "" {
			if o := fn.Field(cfg.CallAccessorObjField); o != nil {
				switch {
				case o.Type() == "identifier":
					recv = o.Text()
				case cfg.CallAccessorTypes.Has(o.Type()):
					if inner := o.Field(cfg.CallAccessorObjField); inner != nil && inner.Type() == "this" {
						if p := o.Field(cfg.CallAccessorField); p != nil {
							recv = "this." + p.Text()
						}
					}
				case cfg.Lang == "python" && o.Type() == "call":
					if rf := o.Field("function"); rf != nil && rf.Type() == "identifier" && rf.Text() == "super" {
						recv = "super"
					}
				}
			}
		}

		return callee, true, recv
	}

	return fn.Text(), false, ""
}

func isUpper(s string) bool {
	if s == "" {
		return false
	}

	c := s[0]

	return (c >= 'A' && c <= 'Z') || strings.HasPrefix(s, "this.")
}
