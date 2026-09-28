package extract

import (
	"strings"

	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// Resolver is a corpus-level, language-specific resolution pass. It may
// append nodes/edges; ids are final when it runs.
type Resolver func(root string, nodes *[]*model.Node, edges *[]*model.Edge, per []FileResult)

// FileResult exposes per-file extraction output to resolvers.
type FileResult struct {
	Path string
	Ex   *model.Extraction
}

var (
	resolvers          []Resolver
	typeResolvers      []Resolver
	preRewireResolvers []Resolver
)

var symbolResolvers []Resolver

// RegisterSymbolResolver adds a resolver that runs first, while ids and
// source files are still absolute-path derived (symbol-level imports).
func RegisterSymbolResolver(r Resolver) { symbolResolvers = append(symbolResolvers, r) }

func (c *corpus) runSymbolResolvers() {
	per := c.fileResults()
	for _, r := range symbolResolvers {
		r(c.root, &c.nodes, &c.edges, per)
	}
}

// RegisterPreRewireResolver adds a resolver that runs after the Go type
// pass and immediately before the unique-stub rewire.
func RegisterPreRewireResolver(r Resolver) { preRewireResolvers = append(preRewireResolvers, r) }

func (c *corpus) runPreRewireResolvers() {
	per := c.fileResults()
	for _, r := range preRewireResolvers {
		r(c.root, &c.nodes, &c.edges, per)
	}
}

var postRewireResolvers []Resolver

// RegisterPostRewireResolver adds a resolver that runs after the unique-stub
// rewire and before the shared call pass.
func RegisterPostRewireResolver(r Resolver) { postRewireResolvers = append(postRewireResolvers, r) }

func (c *corpus) runPostRewireResolvers() {
	per := c.fileResults()
	for _, r := range postRewireResolvers {
		r(c.root, &c.nodes, &c.edges, per)
	}
}

// RegisterResolver adds a resolver run after the shared call pass.
func RegisterResolver(r Resolver) { resolvers = append(resolvers, r) }

// RegisterTypeResolver adds a resolver that runs after id disambiguation and
// before the bare-label stub rewire (type-reference disambiguation).
func RegisterTypeResolver(r Resolver) { typeResolvers = append(typeResolvers, r) }

func (c *corpus) fileResults() []FileResult {
	per := make([]FileResult, 0, len(c.per))
	for _, fr := range c.per {
		if fr.ex != nil {
			per = append(per, FileResult{Path: fr.path, Ex: fr.ex})
		}
	}

	return per
}

func (c *corpus) runTypeResolvers() {
	per := c.fileResults()
	for _, r := range typeResolvers {
		r(c.root, &c.nodes, &c.edges, per)
	}
}

func (c *corpus) runLanguageResolvers() {
	per := c.fileResults()
	for _, r := range resolvers {
		r(c.root, &c.nodes, &c.edges, per)
	}
}

// resolveGoTypeReferences resolves pkg.Type references to their exact
// in-module definition, or parks them on an FQN-labeled external stub.
func (c *corpus) resolveGoTypeReferences() {
	importsByFile := map[string]map[string]string{}

	for _, fr := range c.per {
		if fr.ex == nil || !strings.HasSuffix(fr.path, ".go") {
			continue
		}

		importsByFile[fr.path] = fr.ex.GoImports
	}

	if len(importsByFile) == 0 {
		return
	}

	contained := map[string]bool{}

	for _, e := range c.edges {
		if e.Relation == "contains" {
			contained[e.Target] = true
		}
	}

	goMods := newGoModCache()
	fqn := map[string][]string{}

	for _, n := range c.nodes {
		if n.SourceFile == "" || n.Label == "" || !contained[n.ID] || !isTypeLikeDefinition(n) {
			continue
		}

		if pp := goMods.importPathForFile(c.root, n.SourceFile); pp != "" {
			k := pp + "." + n.Label
			fqn[k] = append(fqn[k], n.ID)
		}
	}

	qualified := map[string]string{}

	for _, n := range c.nodes {
		if n.ID != "" && n.SourceFile == "" && strings.Contains(n.Label, ".") {
			qualified[n.ID] = n.Label
		}
	}

	if len(qualified) == 0 {
		return
	}

	nodeIDs := map[string]bool{}
	for _, n := range c.nodes {
		nodeIDs[n.ID] = true
	}

	externals := map[string]string{}
	repointed := map[string]bool{}

	external := func(f string) string {
		if id, ok := externals[f]; ok {
			return id
		}

		id := ids.MakeID("go", "type", f)
		if !nodeIDs[id] {
			c.nodes = append(c.nodes, &model.Node{ID: id, Label: f, FileType: model.FileTypeCode})
			nodeIDs[id] = true
		}

		externals[f] = id

		return id
	}

	for _, e := range c.edges {
		if e.Relation != "references" && e.Relation != "embeds" {
			continue
		}

		q, ok := qualified[e.Target]
		if !ok {
			continue
		}

		i := strings.LastIndexByte(q, '.')
		alias, typeName := q[:i], q[i+1:]

		ip := importsByFile[e.SourceFile][alias]
		if ip == "" || typeName == "" {
			continue
		}

		f := ip + "." + typeName
		old := e.Target

		if cands := fqn[f]; len(cands) == 1 {
			e.Target = cands[0]
		} else {
			e.Target = external(f)
		}

		repointed[old] = true
	}

	if len(repointed) == 0 {
		return
	}

	referenced := map[string]bool{}
	for _, e := range c.edges {
		referenced[e.Source] = true
		referenced[e.Target] = true
	}

	kept := c.nodes[:0]

	for _, n := range c.nodes {
		if repointed[n.ID] && !referenced[n.ID] {
			continue
		}

		kept = append(kept, n)
	}

	c.nodes = kept
}
