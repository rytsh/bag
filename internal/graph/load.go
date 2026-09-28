package graph

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/rytsh/bag/internal/model"
)

// Loaded is a graph read from graph.json with its community assignment.
type Loaded struct {
	G           *Graph
	Communities Communities
	Labels      map[int]string
	Commit      string
}

var nodeKnown = map[string]bool{
	"id": true, "label": true, "file_type": true, "source_file": true, "source_location": true,
	"type": true, "metadata": true, "community": true, "community_name": true, "norm_label": true,
	"_callable": true, "_callable_class": true,
}

var edgeKnown = map[string]bool{
	"source": true, "target": true, "relation": true, "confidence": true, "confidence_score": true,
	"source_file": true, "source_location": true, "weight": true, "context": true, "metadata": true,
}

// Load reads a graph.json file (Graphify- or bag-produced).
func Load(path string) (*Loaded, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	return Parse(raw)
}

// Parse decodes graph.json bytes.
func Parse(raw []byte) (*Loaded, error) {
	var doc struct {
		Nodes      []map[string]any `json:"nodes"`
		Links      []map[string]any `json:"links"`
		Edges      []map[string]any `json:"edges"`
		Hyperedges []map[string]any `json:"hyperedges"`
		Commit     string           `json:"built_at_commit"`
	}

	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode graph.json; %w", err)
	}

	links := doc.Links
	if len(links) == 0 {
		links = doc.Edges
	}

	out := &Loaded{G: New(), Communities: Communities{}, Labels: map[int]string{}, Commit: doc.Commit}

	for _, m := range doc.Nodes {
		n := &model.Node{
			ID:             str(m["id"]),
			Label:          str(m["label"]),
			FileType:       str(m["file_type"]),
			SourceFile:     str(m["source_file"]),
			SourceLocation: str(m["source_location"]),
			Type:           str(m["type"]),
			Callable:       m["_callable"] == true,
			CallableClass:  m["_callable_class"] == true,
		}

		if loc, ok := m["source_location"].(string); ok && loc == "" {
			n.EmptyLocation = true
		}

		if md, ok := m["metadata"].(map[string]any); ok {
			n.Metadata = md
		}

		for k, v := range m {
			if !nodeKnown[k] {
				if n.Extra == nil {
					n.Extra = map[string]any{}
				}

				n.Extra[k] = v
			}
		}

		if n.ID == "" {
			continue
		}

		out.G.AddNode(n)

		if c, ok := m["community"].(float64); ok {
			cid := int(c)
			out.Communities[cid] = append(out.Communities[cid], n.ID)

			if name, ok := m["community_name"].(string); ok {
				out.Labels[cid] = name
			}
		}
	}

	for _, m := range links {
		e := &model.Edge{
			Source:         str(m["source"]),
			Target:         str(m["target"]),
			Relation:       str(m["relation"]),
			Confidence:     str(m["confidence"]),
			SourceFile:     str(m["source_file"]),
			SourceLocation: str(m["source_location"]),
			Context:        str(m["context"]),
			Weight:         1,
		}

		if w, ok := m["weight"].(float64); ok {
			e.Weight = w
		} else if _, has := m["weight"]; !has {
			e.NoWeight = true
		}

		if s, ok := m["confidence_score"].(float64); ok {
			e.ConfidenceScore = model.Score(s)
		}

		if md, ok := m["metadata"].(map[string]any); ok {
			e.Metadata = md
		}

		for k, v := range m {
			if !edgeKnown[k] {
				if e.Extra == nil {
					e.Extra = map[string]any{}
				}

				e.Extra[k] = v
			}
		}

		if !out.G.Has(e.Source) || !out.G.Has(e.Target) {
			continue
		}

		out.G.SetEdge(e)
	}

	for _, m := range doc.Hyperedges {
		h := &model.Hyperedge{
			ID: str(m["id"]), Label: str(m["label"]), Relation: str(m["relation"]),
			SourceFile: str(m["source_file"]),
		}

		if arr, ok := m["nodes"].([]any); ok {
			for _, x := range arr {
				h.Nodes = append(h.Nodes, str(x))
			}
		}

		for k, v := range m {
			switch k {
			case "id", "label", "relation", "source_file", "nodes":
			default:
				if h.Extra == nil {
					h.Extra = map[string]any{}
				}

				h.Extra[k] = v
			}
		}

		out.G.Hyperedges = append(out.G.Hyperedges, h)
	}

	return out, nil
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case float64:
		return fmt.Sprintf("%v", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}
