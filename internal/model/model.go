// Package model holds the extraction and graph schema shared across bag.
//
// The JSON shape mirrors Graphify's extraction schema and graph.json
// (node_link_data with "links") so graphs are interchangeable.
package model

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

	// OriginFile is set on sourceless stubs: the file that referenced them.
	OriginFile string `json:"origin_file,omitempty"`

	// Internal extraction markers (not written to graph.json).
	Callable      bool `json:"-"`
	CallableClass bool `json:"-"`

	Extra map[string]any `json:"-"`
}

// Edge is a directed relation between two nodes.
type Edge struct {
	Source          string         `json:"source"`
	Target          string         `json:"target"`
	Relation        string         `json:"relation"`
	Confidence      string         `json:"confidence"`
	ConfidenceScore *float64       `json:"confidence_score,omitempty"`
	SourceFile      string         `json:"source_file"`
	SourceLocation  string         `json:"source_location,omitempty"`
	Weight          float64        `json:"weight"`
	Context         string         `json:"context,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`

	Extra map[string]any `json:"-"`
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
	Context        string
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
