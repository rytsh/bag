// Package tsx wraps the pure-Go tree-sitter runtime with small helpers used
// by the extractors.
package tsx

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	ts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// Tree is a parsed source file.
type Tree struct {
	Lang *ts.Language
	Src  []byte
	tree *ts.Tree
	Root *Node

	timedOut bool
}

// Node is a thin wrapper that carries the language and source so callers do
// not have to thread them through every helper.
type Node struct {
	n    *ts.Node
	tree *Tree
}

var (
	langMu    sync.Mutex
	langCache = map[string]*ts.Language{}
)

// Language loads (and caches) a grammar by registry name.
func Language(name string) (*ts.Language, error) {
	langMu.Lock()
	defer langMu.Unlock()

	if l, ok := langCache[name]; ok {
		return l, nil
	}
	if name == "vbnet" {
		l, err := ts.LoadLanguage(vbnetGrammar)
		if err != nil {
			return nil, fmt.Errorf("load vbnet grammar; %w", err)
		}
		langCache[name] = l
		return l, nil
	}

	entry := grammars.DetectLanguageByName(name)
	if entry == nil {
		return nil, fmt.Errorf("grammar %q not available", name)
	}

	l := entry.Language()
	if l == nil {
		return nil, fmt.Errorf("grammar %q failed to load", name)
	}

	langCache[name] = l

	return l, nil
}

// Parse time budget. A pathological input (deeply ambiguous punctuation, a
// large file the grammar keeps backtracking on) can take minutes in the GLR
// parser and still yield almost nothing; a budget that grows with the file
// keeps ordinary large files whole and stops the rest. Real source parses in
// milliseconds (a 250 KB Go file in well under a second), so the budget is
// generous for it. The partial tree is still used, and the stop is reported.
const (
	parseBaseBudget    = 5 * time.Second
	parseBudgetPerByte = 50 * time.Microsecond
	parseMaxBudget     = time.Minute
)

// ParseBudget returns the parse timeout for a source of n bytes.
func ParseBudget(n int) time.Duration {
	return min(parseBaseBudget+time.Duration(n)*parseBudgetPerByte, parseMaxBudget)
}

// slowSources remembers, per grammar, the content hashes whose parse ran
// into its budget. Several passes parse the same file (the extractor, then
// the cross-file symbol passes); without this each of them would spend the
// full budget again on a file already known to stall. A repeat parse gets a
// small budget instead, so it still returns the (partial) tree quickly.
var (
	slowMu      sync.Mutex
	slowSources = map[[2]string]bool{}
)

const repeatSlowBudget = time.Second

func slowKey(grammar string, src []byte) [2]string {
	// Extractors append a final newline before parsing and the cross-file
	// passes do not; both are the same file.
	sum := sha256.Sum256(bytes.TrimRight(src, "\r\n"))

	return [2]string{grammar, string(sum[:])}
}

// Parse parses src with the named grammar.
func Parse(grammar string, src []byte) (*Tree, error) {
	lang, err := Language(grammar)
	if err != nil {
		return nil, err
	}

	budget := ParseBudget(len(src))
	key := slowKey(grammar, src)

	slowMu.Lock()
	known := slowSources[key]
	slowMu.Unlock()

	if known {
		budget = repeatSlowBudget
	}

	p := ts.NewParser(lang)
	p.SetTimeoutMicros(uint64(budget.Microseconds()))

	start := time.Now()

	tree, err := p.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("parse %s; %w", grammar, err)
	}

	t := &Tree{Lang: lang, Src: src, tree: tree}
	t.Root = t.wrap(tree.RootNode())

	// The runtime does not always report a timeout as one, so the clock is
	// the evidence: a parse that used its whole budget stopped early.
	if time.Since(start) >= budget || tree.ParseStopReason() == ts.ParseStopTimeout {
		t.timedOut = true

		slowMu.Lock()
		slowSources[key] = true
		slowMu.Unlock()
	}

	return t, nil
}

// TypeScriptGrammar returns the grammar a .ts source is parsed with. A .ts
// file is not supposed to contain JSX, but plenty do, and the typescript
// grammar backtracks on every <tag> - a component file took minutes and lost
// almost every declaration - while tsx parses it in milliseconds. The tsx
// grammar only differs on type assertions (<T>x), so a file with JSX in it is
// parsed as tsx.
func TypeScriptGrammar(src []byte) string {
	if looksLikeJSX(src) {
		return "tsx"
	}

	return "typescript"
}

// looksLikeJSX reports whether src has a JSX element: a "<" opening a
// lowercase tag or a component, with a closing "</" or "/>" after it.
func looksLikeJSX(src []byte) bool {
	if !bytes.Contains(src, []byte("</")) && !bytes.Contains(src, []byte("/>")) {
		return false
	}

	for i := 0; i+1 < len(src); i++ {
		if src[i] != '<' {
			continue
		}

		// A JSX element starts an expression: after return, (, =, =>, :, ?
		// or a comma, not after an identifier (a generic: Array<string>).
		j := i - 1
		for j >= 0 && (src[j] == ' ' || src[j] == '\t' || src[j] == '\n' || src[j] == '\r') {
			j--
		}
		if j >= 0 && (isIdentByte(src[j]) || src[j] == ')' || src[j] == ']') && !bytes.HasSuffix(src[:j+1], []byte("return")) {
			continue
		}

		c := src[i+1]
		if c == '>' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			end := min(len(src), i+200)
			if bytes.Contains(src[i:end], []byte(">")) {
				return true
			}
		}
	}

	return false
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// StoppedEarly reports why the parser gave up before the end of the source
// ("timeout", "iteration_limit", ...), or "" when it parsed all of it. The
// tree is then partial: declarations past the stop are missing.
func (t *Tree) StoppedEarly() string {
	if t == nil || t.tree == nil {
		return ""
	}

	if t.timedOut {
		return string(ts.ParseStopTimeout)
	}

	if !t.tree.ParseStoppedEarly() {
		return ""
	}

	return string(t.tree.ParseStopReason())
}

// Release frees the tree.
func (t *Tree) Release() {
	if t.tree != nil {
		t.tree.Release()
	}
}

func (t *Tree) wrap(n *ts.Node) *Node {
	if n == nil {
		return nil
	}

	return &Node{n: n, tree: t}
}

// Raw returns the underlying node.
func (n *Node) Raw() *ts.Node { return n.n }

// Type returns the grammar node type.
func (n *Node) Type() string {
	if n == nil {
		return ""
	}

	return n.n.Type(n.tree.Lang)
}

// Text returns the node source text.
func (n *Node) Text() string {
	if n == nil {
		return ""
	}

	return n.n.Text(n.tree.Src)
}

// Line returns the 1-based start line.
func (n *Node) Line() int { return int(n.n.StartPoint().Row) + 1 }

// EndLine returns the 1-based end line.
func (n *Node) EndLine() int { return int(n.n.EndPoint().Row) + 1 }

// StartByte returns the start offset.
func (n *Node) StartByte() uint32 { return n.n.StartByte() }

// EndByte returns the end offset.
func (n *Node) EndByte() uint32 { return n.n.EndByte() }

// IsNamed reports whether the node is named.
func (n *Node) IsNamed() bool { return n != nil && n.n.IsNamed() }

// IsError reports whether the node is an ERROR node.
func (n *Node) IsError() bool { return n != nil && n.n.IsError() }

// HasError reports whether the subtree contains errors.
func (n *Node) HasError() bool { return n != nil && n.n.HasError() }

// Field returns the child for a field name.
func (n *Node) Field(name string) *Node {
	if n == nil || name == "" {
		return nil
	}

	return n.tree.wrap(n.n.ChildByFieldName(name, n.tree.Lang))
}

// Parent returns the parent node.
func (n *Node) Parent() *Node {
	if n == nil {
		return nil
	}

	return n.tree.wrap(n.n.Parent())
}

// Children returns all children.
func (n *Node) Children() []*Node {
	if n == nil {
		return nil
	}

	cnt := n.n.ChildCount()
	out := make([]*Node, 0, cnt)

	for i := range cnt {
		if c := n.n.Child(i); c != nil {
			out = append(out, &Node{n: c, tree: n.tree})
		}
	}

	return out
}

// NamedChildren returns the named children.
func (n *Node) NamedChildren() []*Node {
	var out []*Node

	for _, c := range n.Children() {
		if c.IsNamed() {
			out = append(out, c)
		}
	}

	return out
}

// FieldNameForChild returns the field name of the i-th child.
func (n *Node) FieldNameForChild(i int) string {
	return n.n.FieldNameForChild(i, n.tree.Lang)
}

// ChildOfType returns the first direct child whose type is in types.
func (n *Node) ChildOfType(types ...string) *Node {
	for _, c := range n.Children() {
		t := c.Type()
		for _, want := range types {
			if t == want {
				return c
			}
		}
	}

	return nil
}

// Walk visits the subtree in pre-order. Returning false skips children.
func (n *Node) Walk(fn func(*Node) bool) {
	if n == nil {
		return
	}

	if !fn(n) {
		return
	}

	for _, c := range n.Children() {
		c.Walk(fn)
	}
}

// Same reports whether two wrappers point to the same node.
func (n *Node) Same(o *Node) bool {
	if n == nil || o == nil {
		return n == o
	}

	return n.n == o.n
}

// FirstErrorLine returns the first line containing an ERROR node, or 0.
func (t *Tree) FirstErrorLine() int {
	line := 0

	t.Root.Walk(func(n *Node) bool {
		if line != 0 {
			return false
		}

		if n.IsError() || n.n.IsMissing() {
			line = n.Line()

			return false
		}

		return n.HasError()
	})

	return line
}
