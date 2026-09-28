// Package merge combines Graphify-compatible per-repository graphs.
package merge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rytsh/bag/internal/graph"
)

// Result summarizes a merge.
type Result struct {
	Path  string
	Nodes int
	Edges int
}

type document struct {
	Directed   bool             `json:"directed"`
	Multigraph bool             `json:"multigraph"`
	Graph      map[string]any   `json:"graph"`
	Nodes      []map[string]any `json:"nodes"`
	Links      []map[string]any `json:"links"`
	Edges      []map[string]any `json:"edges,omitempty"`
	Hyperedges []map[string]any `json:"hyperedges"`
}

// Graphs combines two or more graph.json files using Graphify's merge-graphs
// prefixing and cross-repository linking rules.
func Graphs(ctx context.Context, out string, paths ...string) (*Result, error) {
	if len(paths) < 2 {
		return nil, fmt.Errorf("at least two input graphs are required")
	}

	tags := distinctRepoTags(paths)
	merged := document{Graph: map[string]any{}}
	nodeAt := map[string]int{}
	edgeAt := map[string]int{}
	communityOffset := 0

	for i, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		doc, err := load(path)
		if err != nil {
			return nil, fmt.Errorf("load %s; %w", path, err)
		}

		for k, v := range doc.Graph {
			if k != "hyperedges" {
				merged.Graph[k] = v
			}
		}

		relabel := map[string]string{}
		maxCommunity := communityOffset - 1

		for _, n := range doc.Nodes {
			id := text(n["id"])
			if id == "" {
				continue
			}

			if n["external"] != true {
				prefixed := tags[i] + "::" + id
				relabel[id] = prefixed
				n["id"] = prefixed
				n["repo"] = tags[i]
				if _, ok := n["local_id"]; !ok {
					n["local_id"] = id
				}

				if cid, ok := integer(n["community"]); ok {
					if communityOffset > 0 {
						n["local_community"] = cid
						n["community"] = cid + communityOffset
						cid += communityOffset
					}
					if cid > maxCommunity {
						maxCommunity = cid
					}
				}
			}

			putNode(&merged, nodeAt, n)
		}

		for _, e := range links(doc) {
			src := text(e["_src"])
			if src == "" {
				src = text(e["source"])
			}
			tgt := text(e["_tgt"])
			if tgt == "" {
				tgt = text(e["target"])
			}

			e["source"] = mapped(relabel, src)
			e["target"] = mapped(relabel, tgt)
			delete(e, "_src")
			delete(e, "_tgt")
			putEdge(&merged, edgeAt, e)
		}

		for _, he := range hyperedges(doc) {
			if members, ok := he["nodes"].([]any); ok {
				for j, member := range members {
					if id, ok := member.(string); ok {
						members[j] = mapped(relabel, id)
					}
				}
			}
			if id := text(he["id"]); id != "" {
				he["id"] = tags[i] + "::" + id
			}
			merged.Hyperedges = append(merged.Hyperedges, he)
		}

		if maxCommunity >= communityOffset {
			communityOffset = maxCommunity + 1
		}
	}

	merged.Hyperedges = dedupeHyperedges(merged.Hyperedges)
	merged.Graph["hyperedges"] = merged.Hyperedges
	linkSharedTypes(&merged, edgeAt)
	linkCrossRepoCalls(&merged, edgeAt)
	orderEdgesLikeNetworkX(&merged)

	raw, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode merged graph; %w", err)
	}
	raw = append(raw, '\n')

	if err := graph.WriteFileAtomic(out, raw); err != nil {
		return nil, fmt.Errorf("write %s; %w", out, err)
	}

	return &Result{Path: out, Nodes: len(merged.Nodes), Edges: len(merged.Links)}, nil
}

func load(path string) (document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return document{}, err
	}

	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return document{}, fmt.Errorf("decode graph.json; %w", err)
	}
	if doc.Graph == nil {
		doc.Graph = map[string]any{}
	}

	return doc, nil
}

func links(doc document) []map[string]any {
	if len(doc.Links) > 0 {
		return doc.Links
	}
	return doc.Edges
}

func hyperedges(doc document) []map[string]any {
	if len(doc.Hyperedges) > 0 {
		return doc.Hyperedges
	}

	raw, _ := doc.Graph["hyperedges"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if he, ok := item.(map[string]any); ok {
			out = append(out, he)
		}
	}
	return out
}

func distinctRepoTags(paths []string) []string {
	dirs := make([]string, len(paths))
	tags := make([]string, len(paths))
	counts := map[string]int{}

	for i, path := range paths {
		dirs[i] = filepath.Dir(filepath.Dir(path))
		tags[i] = filepath.Base(dirs[i])
		if tags[i] == "." || tags[i] == string(filepath.Separator) || tags[i] == "" {
			tags[i] = "repo"
		}
		counts[tags[i]]++
	}

	for i, tag := range tags {
		if counts[tag] <= 1 {
			continue
		}
		parent := filepath.Base(filepath.Dir(dirs[i]))
		if parent != "." && parent != string(filepath.Separator) && parent != "" {
			tags[i] = parent + "_" + tag
		}
	}

	seen := map[string]int{}
	for i, tag := range tags {
		seen[tag]++
		if seen[tag] > 1 {
			tags[i] = fmt.Sprintf("%s-%d", tag, seen[tag])
		}
	}

	return tags
}

func putNode(doc *document, at map[string]int, n map[string]any) {
	id := text(n["id"])
	if i, ok := at[id]; ok {
		doc.Nodes[i] = n
		return
	}
	at[id] = len(doc.Nodes)
	doc.Nodes = append(doc.Nodes, n)
}

func putEdge(doc *document, at map[string]int, e map[string]any) {
	src, tgt := text(e["source"]), text(e["target"])
	if src == "" || tgt == "" {
		return
	}
	key := pairKey(src, tgt)
	if i, ok := at[key]; ok {
		doc.Links[i] = e
		return
	}
	at[key] = len(doc.Links)
	doc.Links = append(doc.Links, e)
}

func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "\x00" + b
}

func orderEdgesLikeNetworkX(doc *document) {
	adj := map[string][]int{}
	for i, e := range doc.Links {
		src, tgt := text(e["source"]), text(e["target"])
		adj[src] = append(adj[src], i)
		if tgt != src {
			adj[tgt] = append(adj[tgt], i)
		}
	}

	seen := map[int]bool{}
	ordered := make([]map[string]any, 0, len(doc.Links))
	for _, n := range doc.Nodes {
		for _, i := range adj[text(n["id"])] {
			if !seen[i] {
				seen[i] = true
				ordered = append(ordered, doc.Links[i])
			}
		}
	}
	doc.Links = ordered
}

func mapped(relabel map[string]string, id string) string {
	if out := relabel[id]; out != "" {
		return out
	}
	return id
}

func integer(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), n == float64(int(n))
	default:
		return 0, false
	}
}

func text(v any) string {
	s, _ := v.(string)
	return s
}

func dedupeHyperedges(in []map[string]any) []map[string]any {
	seen := map[string]bool{}
	out := make([]map[string]any, 0, len(in))
	for _, he := range in {
		id := text(he["id"])
		if id != "" && seen[id] {
			continue
		}
		if id != "" {
			seen[id] = true
		}
		out = append(out, he)
	}
	return out
}

func linkSharedTypes(doc *document, edgeAt map[string]int) {
	groups := map[string][]map[string]any{}
	for _, n := range doc.Nodes {
		if n["_callable_class"] != true || text(n["source_file"]) == "" || text(n["repo"]) == "" {
			continue
		}
		md, _ := n["metadata"].(map[string]any)
		ns, label := text(md["namespace"]), text(n["label"])
		if ns != "" && label != "" {
			groups[ns+"\x00"+label] = append(groups[ns+"\x00"+label], n)
		}
	}

	for _, nodes := range groups {
		for i, left := range nodes {
			for _, right := range nodes[i+1:] {
				if left["repo"] == right["repo"] {
					continue
				}
				src, tgt := text(left["id"]), text(right["id"])
				if _, exists := edgeAt[pairKey(src, tgt)]; exists {
					continue
				}
				putEdge(doc, edgeAt, map[string]any{
					"source": src, "target": tgt, "relation": "same_type_as",
					"context": "cross_repo", "confidence": "INFERRED", "confidence_score": 0.9,
					"source_file": text(left["source_file"]), "weight": 1.0,
				})
			}
		}
	}
}

var languageSuffixes = map[string]map[string]bool{
	"cpp":    {".cpp": true, ".cc": true, ".cxx": true, ".hpp": true, ".hh": true, ".hxx": true, ".h": true, ".cu": true, ".cuh": true},
	"csharp": {".cs": true},
	"java":   {".java": true},
	"swift":  {".swift": true},
}

func linkCrossRepoCalls(doc *document, edgeAt map[string]int) {
	byID := map[string]map[string]any{}
	typesByName := map[string][]map[string]any{}
	typeIDs := map[string]bool{}

	for _, n := range doc.Nodes {
		id := text(n["id"])
		byID[id] = n
		if n["_callable_class"] == true && text(n["source_file"]) != "" && text(n["repo"]) != "" {
			name := bare(text(n["label"]))
			if name != "" {
				typesByName[name] = append(typesByName[name], n)
				typeIDs[id] = true
			}
		}
	}

	members := map[string]map[string][]string{"method": {}, "defines": {}}
	for _, e := range doc.Links {
		rel := text(e["relation"])
		if rel != "method" && rel != "defines" {
			continue
		}
		src, tgt := text(e["source"]), text(e["target"])
		var owner, member string
		switch {
		case typeIDs[src] && !typeIDs[tgt]:
			owner, member = src, tgt
		case typeIDs[tgt] && !typeIDs[src]:
			owner, member = tgt, src
		default:
			continue
		}
		name := bare(text(byID[member]["label"]))
		if name != "" {
			key := owner + "\x00" + name
			members[rel][key] = append(members[rel][key], member)
		}
	}

	for _, caller := range doc.Nodes {
		entries := unresolvedCalls(caller)
		callerRepo := text(caller["repo"])
		if callerRepo == "" || len(entries) == 0 {
			continue
		}

		for _, entry := range entries {
			lang := text(entry["lang"])
			suffixes := languageSuffixes[lang]
			receiver, callee := bare(text(entry["receiver_type"])), bare(text(entry["callee"]))
			if len(suffixes) == 0 || receiver == "" || callee == "" {
				continue
			}

			var candidates []map[string]any
			for _, candidate := range typesByName[receiver] {
				if text(candidate["repo"]) != callerRepo && suffixes[strings.ToLower(filepath.Ext(text(candidate["source_file"])))] {
					candidates = append(candidates, candidate)
				}
			}
			if len(candidates) != 1 || (lang == "csharp" && !csharpVisible(doc, caller, candidates[0], receiver)) {
				continue
			}

			owner := text(candidates[0]["id"])
			relations := []string{"method"}
			if lang == "cpp" {
				relations = append(relations, "defines")
			}
			var targets []string
			for _, rel := range relations {
				targets = members[rel][owner+"\x00"+callee]
				if len(targets) > 0 {
					break
				}
			}
			if len(targets) != 1 {
				continue
			}

			src, tgt := text(caller["id"]), targets[0]
			if src == tgt {
				continue
			}
			if _, exists := edgeAt[pairKey(src, tgt)]; exists {
				continue
			}
			putEdge(doc, edgeAt, map[string]any{
				"source": src, "target": tgt, "relation": "calls", "context": "cross_repo",
				"confidence": "INFERRED", "confidence_score": 0.8,
				"source_file": text(caller["source_file"]), "source_location": entry["line"],
				"weight": 1.0, "_cross_repo_call": true,
			})
		}
	}
}

func unresolvedCalls(n map[string]any) []map[string]any {
	md, _ := n["metadata"].(map[string]any)
	raw, _ := md["unresolved_calls"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if entry, ok := item.(map[string]any); ok {
			out = append(out, entry)
		}
	}
	return out
}

func csharpVisible(doc *document, caller, candidate map[string]any, receiver string) bool {
	callerMD, _ := caller["metadata"].(map[string]any)
	candidateMD, _ := candidate["metadata"].(map[string]any)
	callerNS, candidateNS := text(callerMD["namespace"]), text(candidateMD["namespace"])
	if candidateNS != "" && callerNS == candidateNS {
		return true
	}

	wantFQN := candidateNS
	if wantFQN != "" {
		wantFQN += "."
	}
	wantFQN += bare(text(candidate["label"]))
	callerFile, callerRepo := text(caller["source_file"]), text(caller["repo"])

	for _, e := range doc.Links {
		if text(e["relation"]) != "imports" {
			continue
		}
		md, _ := e["metadata"].(map[string]any)
		kind, target := text(md["using_kind"]), text(md["target_fqn"])
		global := text(md["scope_kind"]) == "global"
		source := nodeByID(doc.Nodes, text(e["source"]))
		if source == nil || text(source["repo"]) != callerRepo || (!global && text(source["source_file"]) != callerFile) {
			continue
		}
		if kind == "namespace" && target == candidateNS {
			return true
		}
		if kind == "alias" && text(md["alias"]) == receiver && target == wantFQN {
			return true
		}
	}
	return false
}

func nodeByID(nodes []map[string]any, id string) map[string]any {
	for _, n := range nodes {
		if text(n["id"]) == id {
			return n
		}
	}
	return nil
}

func bare(s string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s), "."), "()")
}
