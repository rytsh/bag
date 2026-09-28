// Package model holds the extraction and graph schema shared across bag.
//
// The JSON shape mirrors Graphify's extraction schema and graph.json
// (node_link_data with "links") so graphs are interchangeable.
package model

import (
	"bytes"
	"encoding/json"
)

// Confidence tags.
const (
	Extracted = "EXTRACTED"
	Inferred  = "INFERRED"
	Ambiguous = "AMBIGUOUS"
)

// File types.
const (
	FileTypeCode      = "code"
	FileTypeDocument  = "document"
	FileTypePaper     = "paper"
	FileTypeImage     = "image"
	FileTypeRationale = "rationale"
	FileTypeConcept   = "concept"
)

// Node is a graph node. Extra carries any additional attributes so round
// trips of foreign graph.json files do not lose data.
type Node struct {
	ID             string         `json:"id"`
	Label          string         `json:"label"`
	FileType       string         `json:"file_type,omitempty"`
	SourceFile     string         `json:"source_file"`
	SourceLocation string         `json:"source_location,omitempty"`
	Type           string         `json:"type,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	// MetaOrder lists metadata keys in insertion order when it matters for
	// output parity; unlisted keys follow sorted.
	MetaOrder []string `json:"-"`

	// OriginFile is set on sourceless stubs: the file that referenced them.
	OriginFile string `json:"origin_file,omitempty"`
	// EmptyLocation writes `"source_location": ""` for a sourceless named
	// reference stub, as Graphify's ensure_named_node does.
	EmptyLocation bool `json:"-"`

	// Internal extraction markers (not written to graph.json).
	Callable      bool `json:"-"`
	CallableClass bool `json:"-"`
	// TopModule marks a top-level Elixir module/protocol, the only safe
	// cross-file import target.
	TopModule bool `json:"-"`
	// RustImplKey is the owner/arity key of a simple generic Rust impl
	// (`Bucket/1`); RustDeclCount counts struct/enum/trait declarations.
	RustImplKey   string `json:"-"`
	RustDeclCount int    `json:"-"`

	Extra map[string]any `json:"-"`
}

// Edge is a directed relation between two nodes.
type Edge struct {
	Source          string   `json:"source"`
	Target          string   `json:"target"`
	Relation        string   `json:"relation"`
	Confidence      string   `json:"confidence"`
	ConfidenceScore *float64 `json:"confidence_score,omitempty"`
	SourceFile      string   `json:"source_file"`
	SourceLocation  string   `json:"source_location,omitempty"`
	Weight          float64  `json:"weight"`
	// NoWeight omits "weight" from graph.json (Graphify's regex-rescued
	// import edges carry none).
	NoWeight bool           `json:"-"`
	Context  string         `json:"context,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
	// MetaOrder lists metadata keys in insertion order (see Node.MetaOrder).
	MetaOrder []string `json:"-"`

	// LocalAlias is the name an import bound in the importing file when it
	// differs from the target's own name (`import pkg.mod as alias`). It is a
	// resolution hint and never serialized.
	LocalAlias string `json:"-"`
	// TargetFile is the resolved file an import edge points at, used to
	// canonicalize targets that have no node of their own (asset imports).
	TargetFile string `json:"-"`
	// PyImport carries the absolute module an import edge names (Python) so
	// the ambiguous-module guard can find it; nil for everything else.
	PyImport *PyImport `json:"-"`

	Extra map[string]any `json:"-"`
}

// PyImport describes a Python absolute import for the ambiguous-module guard.
type PyImport struct {
	Module string
	// Bindings are (imported name, local binding) pairs.
	Bindings [][2]string
	// ModuleBinding marks `import a.b [as c]` (binds a module, not names).
	ModuleBinding bool
	// MarkerOnly marks a placeholder edge for a namespace-package import;
	// it is removed once the guard ran.
	MarkerOnly bool
	// TargetFile is the resolved module file, if any.
	TargetFile string
}

// RawCall is an unresolved call site recorded by an extractor for the
// cross-file resolution pass.
type RawCall struct {
	CallerID       string
	Callee         string
	IsMemberCall   bool
	Language       string
	Receiver       string
	ImportPath     string
	SourceFile     string
	SourceLocation string
	Indirect       bool
	// AmbiguousPyImport marks a call bound through an ambiguous Python import.
	AmbiguousPyImport bool
	// RustSelfType is the bare impl type of a `self.m()` call's receiver,
	// RustSelfImplKey its generic owner/arity key.
	RustSelfType    string
	RustSelfImplKey string
	Context         string
	// ReceiverType is the receiver's declared type when the extractor could
	// type it (C#, Java, Ruby member calls).
	ReceiverType string
	// QualifiedPrefix is the namespace/package written before a qualified
	// callee (`new A.B.Cache()`, `com.pkg.fn()`).
	QualifiedPrefix string
}

// Hyperedge groups several nodes under one relation.
type Hyperedge struct {
	ID         string         `json:"id,omitempty"`
	Label      string         `json:"label,omitempty"`
	Relation   string         `json:"relation,omitempty"`
	Nodes      []string       `json:"nodes"`
	SourceFile string         `json:"source_file,omitempty"`
	Extra      map[string]any `json:"-"`
}

// Extraction is the per-file (or merged) extractor output.
type Extraction struct {
	Nodes      []*Node
	Edges      []*Edge
	RawCalls   []*RawCall
	Hyperedges []*Hyperedge

	// Language-specific side tables consumed by resolvers.
	GoImports map[string]string
	// TypeTable maps receiver names (fields, params, locals) to their
	// declared type for the receiver-typed member-call resolvers.
	TypeTable map[string]string
	// Package is the file's declared package (Kotlin).
	Package string
	// Factory holds pending `x = Factory.make()` receiver bindings (Swift).
	Factory map[string][2]string
	// FieldTables maps a class label to its field -> declared type table
	// (Java), bound to class nodes by the corpus member-call resolver.
	FieldTables map[string]map[string]string

	Error       string
	ParseErrors bool
	// Skipped marks files an extractor declined by design (not a failure).
	Skipped bool
}

// Merge appends other into e.
func (e *Extraction) Merge(other *Extraction) {
	if other == nil {
		return
	}

	e.Nodes = append(e.Nodes, other.Nodes...)
	e.Edges = append(e.Edges, other.Edges...)
	e.RawCalls = append(e.RawCalls, other.RawCalls...)
	e.Hyperedges = append(e.Hyperedges, other.Hyperedges...)
}

// Score returns a pointer to f, for optional confidence scores.
func Score(f float64) *float64 { return &f }

// OrderedFields is a JSON object that keeps its key order (for metadata
// values whose key order is part of Graphify's output).
type OrderedFields [][2]any

// MarshalJSON renders the fields in order.
func (o OrderedFields) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer

	buf.WriteByte('{')

	for i, kv := range o {
		if i > 0 {
			buf.WriteByte(',')
		}

		k, err := json.Marshal(kv[0])
		if err != nil {
			return nil, err
		}

		v, err := json.Marshal(kv[1])
		if err != nil {
			return nil, err
		}

		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(v)
	}

	buf.WriteByte('}')

	return buf.Bytes(), nil
}

// Get returns the value for key.
func (o OrderedFields) Get(key string) any {
	for _, kv := range o {
		if kv[0] == key {
			return kv[1]
		}
	}

	return nil
}

// SetMeta sets a metadata key, remembering insertion order.
func (n *Node) SetMeta(k string, v any) {
	if n.Metadata == nil {
		n.Metadata = map[string]any{}
	}

	if _, ok := n.Metadata[k]; !ok {
		n.MetaOrder = append(n.MetaOrder, k)
	}

	n.Metadata[k] = v
}

// SetMeta sets a metadata key, remembering insertion order.
func (e *Edge) SetMeta(k string, v any) {
	if e.Metadata == nil {
		e.Metadata = map[string]any{}
	}

	if _, ok := e.Metadata[k]; !ok {
		e.MetaOrder = append(e.MetaOrder, k)
	}

	e.Metadata[k] = v
}
