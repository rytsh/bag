// Package base holds helpers shared by every language extractor.
//
// Adapted from Graphify's graphify/extractors/base.py (Apache-2.0).
package base

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// MakeID is a shortcut for ids.MakeID.
func MakeID(parts ...string) string { return ids.MakeID(parts...) }

// FileStem returns the extension-less, slash-separated path used as the
// node-ID prefix for a file and its symbols.
func FileStem(path string) string {
	p := filepath.ToSlash(path)
	if p == "" || p == "." {
		return ""
	}

	base := p
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		base = p[i+1:]
	}

	if base == "" {
		return ""
	}

	if ext := pySuffix(base); ext != "" {
		return p[:len(p)-len(ext)]
	}

	return p
}

// pySuffix mirrors pathlib's PurePath.suffix: the final ".ext" of the name,
// ignoring a leading dot and names ending in a dot.
func pySuffix(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return ""
	}

	return name[i:]
}

// Suffix returns pathlib-style suffix of a path.
func Suffix(path string) string {
	return pySuffix(filepath.Base(filepath.ToSlash(path)))
}

// FileNodeID is the canonical file node ID for a root-relative path.
func FileNodeID(relPath string) string {
	return MakeID(FileStem(relPath))
}

// Loc formats a 1-based line as "L<n>".
func Loc(line int) string { return fmt.Sprintf("L%d", line) }

// Builder accumulates nodes and edges for one file, de-duplicating node IDs
// exactly like the Python extractors' seen_ids set.
type Builder struct {
	Path   string
	Stem   string
	FileID string

	Nodes    []*model.Node
	Edges    []*model.Edge
	RawCalls []*model.RawCall

	// OnAdd, when set, is called for every sourced node as it is added.
	OnAdd func(n *model.Node)

	seen map[string]*model.Node
}

// NewBuilder creates a builder for path (as passed to the extractor).
func NewBuilder(path string) *Builder {
	b := &Builder{
		Path: path,
		Stem: FileStem(path),
		seen: map[string]*model.Node{},
	}
	b.FileID = MakeID(path)

	return b
}

// Has reports whether id has already been emitted.
func (b *Builder) Has(id string) bool {
	_, ok := b.seen[id]

	return ok
}

// Get returns an emitted node.
func (b *Builder) Get(id string) *model.Node { return b.seen[id] }

// AddNode adds a sourced code node; duplicates are ignored. It returns the
// node (existing or new).
func (b *Builder) AddNode(id, label string, line int) *model.Node {
	if n, ok := b.seen[id]; ok {
		return n
	}

	n := &model.Node{
		ID:             id,
		Label:          label,
		FileType:       model.FileTypeCode,
		SourceFile:     b.Path,
		SourceLocation: Loc(line),
	}
	b.seen[id] = n
	b.Nodes = append(b.Nodes, n)

	if b.OnAdd != nil {
		b.OnAdd(n)
	}

	return n
}

// AddStub adds a sourceless reference stub (cross-file reference).
func (b *Builder) AddStub(id, label string) *model.Node {
	if n, ok := b.seen[id]; ok {
		return n
	}

	n := &model.Node{
		ID:            id,
		Label:         label,
		FileType:      model.FileTypeCode,
		OriginFile:    b.Path,
		EmptyLocation: true,
	}
	b.seen[id] = n
	b.Nodes = append(b.Nodes, n)

	return n
}

// AddFileNode adds the file-level node.
func (b *Builder) AddFileNode() *model.Node {
	return b.AddNode(b.FileID, filepath.Base(b.Path), 1)
}

// AddEdge appends an EXTRACTED edge with weight 1.
func (b *Builder) AddEdge(src, tgt, relation string, line int) *model.Edge {
	return b.AddEdgeCtx(src, tgt, relation, line, "")
}

// AddEdgeCtx appends an EXTRACTED edge with an optional context.
func (b *Builder) AddEdgeCtx(src, tgt, relation string, line int, context string) *model.Edge {
	e := &model.Edge{
		Source:         src,
		Target:         tgt,
		Relation:       relation,
		Confidence:     model.Extracted,
		SourceFile:     b.Path,
		SourceLocation: Loc(line),
		Weight:         1.0,
		Context:        context,
	}
	b.Edges = append(b.Edges, e)

	return e
}

// EnsureNamed resolves a referenced type name to a node in this file under
// scope, or mints a sourceless stub keyed by the bare name.
func (b *Builder) EnsureNamed(scope, name string) string {
	id := MakeID(scope, name)
	if b.Has(id) {
		return id
	}

	id = MakeID(name)
	if !b.Has(id) {
		b.AddStub(id, name)
	}

	return id
}

// Result builds the extraction, dropping edges whose endpoints were never
// emitted (except import relations, which may target external modules).
func (b *Builder) Result() *model.Extraction {
	clean := make([]*model.Edge, 0, len(b.Edges))
	for _, e := range b.Edges {
		if !b.Has(e.Source) {
			continue
		}

		if b.Has(e.Target) || e.Relation == "imports" || e.Relation == "imports_from" || e.Relation == "re_exports" ||
			e.Relation == "dynamic_import" ||
			(e.PyImport != nil && e.PyImport.MarkerOnly) {
			clean = append(clean, e)
		}
	}

	return &model.Extraction{Nodes: b.Nodes, Edges: clean, RawCalls: b.RawCalls}
}

// ResultUnfiltered returns every node and edge as-is.
func (b *Builder) ResultUnfiltered() *model.Extraction {
	return &model.Extraction{Nodes: b.Nodes, Edges: b.Edges, RawCalls: b.RawCalls}
}

// Set is a small string set helper.
type Set map[string]struct{}

// NewSet builds a set.
func NewSet(items ...string) Set {
	s := make(Set, len(items))
	for _, i := range items {
		s[i] = struct{}{}
	}

	return s
}

// Has reports membership.
func (s Set) Has(k string) bool {
	_, ok := s[k]

	return ok
}

// Union returns a new set with both sets' items.
func (s Set) Union(items ...string) Set {
	out := make(Set, len(s)+len(items))
	for k := range s {
		out[k] = struct{}{}
	}

	for _, i := range items {
		out[i] = struct{}{}
	}

	return out
}
