// Package tsx wraps the pure-Go tree-sitter runtime with small helpers used
// by the extractors.
package tsx

import (
	"fmt"
	"sync"

	ts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// Tree is a parsed source file.
type Tree struct {
	Lang *ts.Language
	Src  []byte
	tree *ts.Tree
	Root *Node
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

// Parse parses src with the named grammar.
func Parse(grammar string, src []byte) (*Tree, error) {
	lang, err := Language(grammar)
	if err != nil {
		return nil, err
	}

	p := ts.NewParser(lang)

	tree, err := p.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("parse %s; %w", grammar, err)
	}

	t := &Tree{Lang: lang, Src: src, tree: tree}
	t.Root = t.wrap(tree.RootNode())

	return t, nil
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
