package graph

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

var (
	genericRelations      = base.NewSet("references", "uses", "mentions")
	externalStubRelations = base.NewSet("imports", "imports_from", "re_exports")
	confidenceRank        = map[string]int{model.Extracted: 3, model.Inferred: 2, model.Ambiguous: 1}
	validFileTypes        = base.NewSet("code", "document", "paper", "image", "rationale", "concept")
	edgeLangFamily        = map[string]string{
		".py": "py", ".pyi": "py",
		".js": "js", ".mjs": "js", ".cjs": "js", ".jsx": "js",
		".ts": "js", ".tsx": "js", ".mts": "js", ".cts": "js",
		".go": "go", ".rs": "rs",
		".java": "jvm", ".kt": "jvm", ".scala": "jvm", ".groovy": "jvm",
		".c": "c", ".h": "c", ".cc": "c", ".cpp": "c", ".hpp": "c",
		".cxx": "c", ".hh": "c", ".hxx": "c",
		".cu": "c", ".cuh": "c", ".metal": "c", ".m": "c", ".mm": "c",
		".rb": "rb", ".rake": "rb", ".php": "php", ".cs": "cs", ".swift": "swift", ".lua": "lua",
	}
)

// Build merges extractions into a graph (Graphify's build + build_from_json).
func Build(nodes []*model.Node, edges []*model.Edge, hyper []*model.Hyperedge, root string) *Graph {
	nodes = dedupeNodes(nodes, root)
	nodes, edges = DedupeEntities(nodes, edges)

	g := New()

	for _, n := range nodes {
		if n.ID == "" {
			continue
		}

		if n.FileType == "" {
			n.FileType = model.FileTypeConcept
		} else if !validFileTypes.Has(n.FileType) {
			n.FileType = model.FileTypeConcept
		}

		n.SourceFile = normSourceFile(n.SourceFile, root)
		g.AddNode(n)
	}

	normToID := map[string]string{}
	for _, id := range g.order {
		normToID[ids.NormalizeID(id)] = id
	}

	// Legacy stem aliases: an edge target minted with an older, shorter file
	// stem (parent_stem or bare stem) resolves to the unique node that owns
	// that stem today (Graphify's _alias_candidates).
	aliasCands := map[string]map[string]bool{}
	addAlias := func(k, id string) {
		if aliasCands[k] == nil {
			aliasCands[k] = map[string]bool{}
		}

		aliasCands[k][id] = true
	}

	for _, n := range g.Nodes() {
		sf := n.SourceFile
		if sf == "" || filepath.IsAbs(sf) {
			continue
		}

		newStem := ids.MakeID(base.FileStem(sf))
		norm := ids.NormalizeID(n.ID)

		suffix := ""
		if n.Label != path.Base(sf) && strings.HasPrefix(norm, newStem) {
			suffix = norm[len(newStem):]
		}

		for _, old := range oldFileStems(sf) {
			if old == newStem {
				continue
			}

			addAlias(ids.NormalizeID(old+suffix), n.ID)
			addAlias(old+suffix, n.ID)
		}

		if strings.HasPrefix(n.Label, ".") && extraOrigin(n) == "ast" {
			m := ids.MakeID(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(n.Label), "."), "()"))
			alias := newStem + "_" + m

			if norm != ids.NormalizeID(alias) {
				addAlias(ids.NormalizeID(alias), n.ID)
				addAlias(alias, n.ID)
			}
		}
	}

	for k, c := range aliasCands {
		if len(c) != 1 {
			continue
		}

		if _, ok := normToID[k]; ok {
			continue
		}

		for id := range c {
			normToID[k] = id
		}
	}

	sorted := append([]*model.Edge(nil), edges...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}

		if a.Target != b.Target {
			return a.Target < b.Target
		}

		return a.Relation < b.Relation
	})

	for _, e := range sorted {
		src, tgt := e.Source, e.Target
		if !g.Has(src) {
			if v, ok := normToID[ids.NormalizeID(src)]; ok {
				src = v
			}
		}

		if !g.Has(tgt) {
			if v, ok := normToID[ids.NormalizeID(tgt)]; ok {
				tgt = v
			}
		}

		if !g.Has(src) || !g.Has(tgt) {
			if externalStubRelations.Has(e.Relation) && g.Has(src) && !g.Has(tgt) && tgt != "" {
				g.AddNode(&model.Node{
					ID: tgt, Label: tgt, FileType: model.FileTypeConcept, Type: "external",
					Extra: map[string]any{"external": true},
				})
			} else {
				continue
			}
		}

		edge := *e
		edge.Source, edge.Target = src, tgt

		if edge.Weight < 0 {
			edge.Weight = 1
		}

		if edge.SourceFile == "" {
			edge.SourceFile = g.Node(src).SourceFile
			if edge.SourceFile == "" {
				edge.SourceFile = g.Node(tgt).SourceFile
			}
		}

		edge.SourceFile = normSourceFile(edge.SourceFile, root)

		if skipCrossLanguage(g, &edge) {
			continue
		}

		if src == tgt && (edge.Relation == "imports" || edge.Relation == "imports_from" || edge.Relation == "re_exports") {
			continue
		}

		if ex := g.Edge(src, tgt); ex != nil {
			if ex.Relation == edge.Relation && ex.Source == tgt && ex.Target == src {
				continue
			}

			if genericRelations.Has(edge.Relation) && ex.Relation != "" && !genericRelations.Has(ex.Relation) {
				continue
			}

			if ex.Relation == edge.Relation {
				er, ir := confidenceRank[ex.Confidence], confidenceRank[edge.Confidence]
				if er > ir {
					continue
				}

				if er == ir {
					if ex.SourceLocation != "" && edge.SourceLocation == "" {
						continue
					}

					if ex.ConfidenceScore != nil && edge.ConfidenceScore != nil && *ex.ConfidenceScore > *edge.ConfidenceScore {
						continue
					}
				}
			}
		}

		g.SetEdge(&edge)
	}

	for _, he := range hyper {
		var members []string

		for _, m := range he.Nodes {
			if !g.Has(m) {
				m = normToID[ids.NormalizeID(m)]
			}

			if m != "" && g.Has(m) {
				members = append(members, m)
			}
		}

		if len(members) == 0 {
			continue
		}

		he.Nodes = members
		g.Hyperedges = append(g.Hyperedges, he)
	}

	disambiguateFileLabels(g)

	return g
}

// oldFileStems returns pre-migration stem forms for a relative path:
// "parent.stem" and bare "stem".
func oldFileStems(rel string) []string {
	rel = filepath.ToSlash(rel)
	stem := strings.TrimSuffix(path.Base(rel), base.Suffix(rel))

	var forms []string

	if parent := path.Base(path.Dir(rel)); parent != "." && parent != "/" && parent != "" {
		forms = append(forms, ids.MakeID(parent+"."+stem))
	}

	forms = append(forms, ids.MakeID(stem))

	seen := map[string]bool{}

	var out []string

	for _, f := range forms {
		if f != "" && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}

	return out
}

func extraOrigin(n *model.Node) string {
	if v, ok := n.Extra["_origin"].(string); ok {
		return v
	}

	return "ast"
}

func skipCrossLanguage(g *Graph, e *model.Edge) bool {
	switch e.Relation {
	case "calls", "imports", "imports_from", "references":
	default:
		return false
	}

	se := strings.ToLower(base.Suffix(g.Node(e.Source).SourceFile))
	te := strings.ToLower(base.Suffix(g.Node(e.Target).SourceFile))
	sf, sok := edgeLangFamily[se]
	tf, tok := edgeLangFamily[te]

	if e.Relation == "calls" {
		return e.Confidence == model.Inferred && se != "" && te != "" && sf != tf
	}

	return sok && tok && sf != tf
}

func normSourceFile(p, root string) string {
	if p == "" {
		return ""
	}

	if root != "" && filepath.IsAbs(p) {
		if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}

	return filepath.ToSlash(p)
}

// dedupeNodes keeps one node per id (preferring sourced nodes) and merges
// missing attributes from same-source duplicates.
func dedupeNodes(nodes []*model.Node, root string) []*model.Node {
	seen := map[string]*model.Node{}

	var order []string

	for _, n := range nodes {
		if n.ID == "" {
			continue
		}

		inc, ok := seen[n.ID]
		if !ok {
			seen[n.ID] = n
			order = append(order, n.ID)

			continue
		}

		if collisionRank(n) < collisionRank(inc) {
			if n.SourceFile == inc.SourceFile {
				mergeMissing(n, inc)
			}

			seen[n.ID] = n
		} else if n.SourceFile == inc.SourceFile {
			mergeMissing(inc, n)
		}
	}

	out := make([]*model.Node, 0, len(order))
	for _, id := range order {
		out = append(out, seen[id])
	}

	_ = root

	return out
}

func collisionRank(n *model.Node) int {
	r := 0
	if n.SourceFile == "" {
		r += 2
	}

	if n.SourceLocation == "" {
		r++
	}

	return r
}

func mergeMissing(dst, src *model.Node) {
	if dst.SourceLocation == "" {
		dst.SourceLocation = src.SourceLocation
	}

	if dst.Type == "" {
		dst.Type = src.Type
	}

	if dst.Metadata == nil && src.Metadata != nil {
		dst.Metadata = src.Metadata
	}
}

// IsFileNodeLabel reports whether label is the file node label for sf.
func IsFileNodeLabel(label, sf string) bool {
	if label == "" || sf == "" {
		return false
	}

	sf = strings.ReplaceAll(sf, `\`, "/")
	if label == path.Base(sf) {
		return true
	}

	return strings.Contains(label, "/") && (sf == label || strings.HasSuffix(sf, "/"+label))
}

// IsFileNode reports whether n is a file-level node.
func IsFileNode(n *model.Node) bool { return n != nil && IsFileNodeLabel(n.Label, n.SourceFile) }

func disambiguateFileLabels(g *Graph) {
	groups := map[string][]*model.Node{}

	for _, n := range g.Nodes() {
		if n.SourceFile != "" && IsFileNodeLabel(n.Label, n.SourceFile) {
			b := path.Base(n.SourceFile)
			groups[b] = append(groups[b], n)
		}
	}

	for _, members := range groups {
		distinct := map[string]bool{}
		for _, m := range members {
			distinct[m.SourceFile] = true
		}

		if len(distinct) < 2 {
			continue
		}

		for _, m := range members {
			m.Label = shortestUniqueSuffix(m.SourceFile, distinct)
		}
	}
}

func shortestUniqueSuffix(sf string, all map[string]bool) string {
	parts := splitParts(sf)

	var others [][]string

	for o := range all {
		if o != sf {
			others = append(others, splitParts(o))
		}
	}

	for k := 1; k <= len(parts); k++ {
		suf := parts[len(parts)-k:]
		unique := true

		for _, o := range others {
			if len(o) >= k && equalSlices(o[len(o)-k:], suf) {
				unique = false

				break
			}
		}

		if unique {
			return strings.Join(suf, "/")
		}
	}

	return strings.Join(parts, "/")
}

func splitParts(p string) []string {
	var out []string

	for _, s := range strings.Split(strings.ReplaceAll(p, `\`, "/"), "/") {
		if s != "" {
			out = append(out, s)
		}
	}

	return out
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// Summary returns a one-line description.
func (g *Graph) Summary() string {
	return fmt.Sprintf("%d nodes, %d edges", g.NumNodes(), g.NumEdges())
}

var _ = os.Stat
