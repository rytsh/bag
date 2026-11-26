package extract

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// repointGoImports turns intra-module go_pkg_<path> sinks into edges to the
// imported package's file nodes.
func (c *corpus) repointGoImports() {
	goMods := newGoModCache()
	pkgFiles := map[string][]string{}

	var sfs []string

	sfToFile := map[string]string{}

	for _, n := range c.nodes {
		if n.SourceFile != "" && n.Label == filepath.Base(n.SourceFile) {
			if _, ok := sfToFile[n.SourceFile]; !ok {
				sfToFile[n.SourceFile] = n.ID
				sfs = append(sfs, n.SourceFile)
			}
		}
	}

	for _, sf := range sfs {
		if !strings.HasSuffix(sf, ".go") {
			continue
		}

		if ip := goMods.importPathForFile(c.root, sf); ip != "" {
			pkgFiles[ip] = append(pkgFiles[ip], sfToFile[sf])
		}
	}

	if len(pkgFiles) == 0 {
		return
	}

	sinks := map[string]string{}
	for ip := range pkgFiles {
		sinks[ids.MakeID("go", "pkg", ip)] = ip
	}

	existing := map[[2]string]bool{}
	for _, e := range c.edges {
		existing[[2]string{e.Source, e.Target}] = true
	}

	out := make([]*model.Edge, 0, len(c.edges))

	for _, e := range c.edges {
		ip, ok := sinks[e.Target]
		if e.Relation != "imports_from" || !ok {
			out = append(out, e)

			continue
		}

		for _, f := range pkgFiles[ip] {
			if f == e.Source || existing[[2]string{e.Source, f}] {
				continue
			}

			cp := *e
			cp.Target = f
			out = append(out, &cp)
			existing[[2]string{e.Source, f}] = true
		}
	}

	c.edges = out
}

// goPackageKey identifies a Go package: its directory and declared name, so
// a package foo_test beside package foo is a package of its own.
type goPackageKey struct{ dir, pkg string }

// goPackages maps each extracted Go file to its package.
func (c *corpus) goPackages() map[string]goPackageKey {
	out := map[string]goPackageKey{}

	for _, fr := range c.per {
		if fr.ex != nil && strings.HasSuffix(fr.path, ".go") {
			out[fr.path] = goPackageKey{filepath.Dir(fr.path), fr.ex.Package}
		}
	}

	return out
}

// goTypeDeclarations indexes the Go type declarations of the corpus by
// package and key: the file's contains edge into a type-like node. File
// nodes, functions and methods are not types, whatever their id.
func (c *corpus) goTypeDeclarations(pkgs map[string]goPackageKey, key func(*model.Node) string) map[goPackageKey]map[string][]*model.Node {
	contained := map[string]bool{}
	for _, e := range c.edges {
		if e.Relation == "contains" {
			contained[e.Target] = true
		}
	}

	out := map[goPackageKey]map[string][]*model.Node{}

	for _, n := range c.nodes {
		pk, ok := pkgs[n.SourceFile]
		if !ok || n.ID == "" || n.GoReceiver || !contained[n.ID] || !isTypeLikeDefinition(n) ||
			n.Label == filepath.Base(n.SourceFile) {
			continue
		}

		if out[pk] == nil {
			out[pk] = map[string][]*model.Node{}
		}

		k := key(n)
		out[pk][k] = append(out[pk][k], n)
	}

	return out
}

// foldedReceiver is a Go receiver node dropped in favour of its type's
// declaration: edges from its file that named it must follow the
// declaration's final id.
type foldedReceiver struct {
	id   string
	file string
	decl *model.Node
}

// foldGoReceivers drops each Go receiver node (see model.Node.GoReceiver)
// whose type has exactly one declaration in the same package, so a Go type is
// one node carrying all of its methods however many files declare them, and a
// reference to it reaches the declaration.
//
// It runs before disambiguateCollidingIDs, while a receiver still shares its
// id with the declaration: without the receivers the declaration is often the
// only node left with that id and keeps it unsalted. A receiver whose type is
// not declared in the package (an excluded or generated file) or is declared
// more than once (build-tagged variants) is kept.
func (c *corpus) foldGoReceivers() []foldedReceiver {
	pkgs := c.goPackages()
	decls := c.goTypeDeclarations(pkgs, func(n *model.Node) string { return n.ID })

	var folded []foldedReceiver

	nodes := c.nodes[:0]

	for _, n := range c.nodes {
		if n.GoReceiver {
			if d := decls[pkgs[n.SourceFile]][n.ID]; len(d) == 1 {
				folded = append(folded, foldedReceiver{id: n.ID, file: c.sourceKey(n.SourceFile), decl: d[0]})

				continue
			}
		}

		nodes = append(nodes, n)
	}

	c.nodes = nodes

	return folded
}

// repointGoReceiverEdges moves the edges a folded receiver's file made to it
// onto the declaration, whose id disambiguateCollidingIDs may have salted.
// Those edges were remapped by their own file, which no longer has a node
// with that id.
func (c *corpus) repointGoReceiverEdges(folded []foldedReceiver) {
	if len(folded) == 0 {
		return
	}

	type key struct{ id, file string }

	remap := make(map[key]string, len(folded))
	for _, f := range folded {
		remap[key{f.id, f.file}] = f.decl.ID
	}

	for _, e := range c.edges {
		sk := c.sourceKey(e.SourceFile)
		if v, ok := remap[key{e.Source, sk}]; ok {
			e.Source = v
		}

		if v, ok := remap[key{e.Target, sk}]; ok {
			e.Target = v
		}
	}

	for _, rc := range c.rawCalls {
		if v, ok := remap[key{rc.CallerID, c.sourceKey(rc.SourceFile)}]; ok {
			rc.CallerID = v
		}
	}
}

var goModuleRe = regexp.MustCompile(`(?m)^\s*module\s+(\S+)`)

type goModCache struct {
	dirs map[string]string
}

func newGoModCache() *goModCache { return &goModCache{dirs: map[string]string{}} }

// importPathForFile returns the canonical Go import path of the package
// containing source file sf.
func (g *goModCache) importPathForFile(root, sf string) string {
	if sf == "" {
		return ""
	}

	p := sf
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}

	dir := filepath.Dir(p)

	modDir, modPath := "", ""

	for cand := dir; ; {
		if v, ok := g.dirs[cand]; ok {
			if v != "" {
				modDir, modPath = cand, v
			}

			break
		}

		raw, err := os.ReadFile(filepath.Join(cand, "go.mod"))
		if err == nil {
			m := goModuleRe.FindSubmatch(raw)

			v := ""
			if m != nil {
				v = string(m[1])
			}

			g.dirs[cand] = v
			modDir, modPath = cand, v

			break
		}

		parent := filepath.Dir(cand)
		if parent == cand {
			break
		}

		cand = parent
	}

	if modDir == "" || modPath == "" {
		return ""
	}

	rel, err := filepath.Rel(modDir, dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}

	if rel == "." {
		return modPath
	}

	return modPath + "/" + filepath.ToSlash(rel)
}

// resolveGoPackageTypeStubs points an unqualified type reference to its
// declaration in another file of the same package. The extractor sees one
// file at a time, so `func f() Config` in config_test.go names a stub; and as
// many files of a repository declare a "Config", the label-only rewire cannot
// pick one. In Go the package decides: an unqualified type name is the one
// type of that name declared in it.
//
// Only the edges of the stub's own file move, so a stub that two files share
// by id is resolved for each in its own package. A type parameter of the same
// name (`func F[Node any](n Node)`) shadows the package type and stays a stub.
func (c *corpus) resolveGoPackageTypeStubs() {
	pkgs := c.goPackages()
	decls := c.goTypeDeclarations(pkgs, func(n *model.Node) string { return n.Label })

	typeParams := c.goTypeParameters()

	type key struct{ id, file string }

	remap := map[key]string{}
	targets := map[string]bool{}

	for _, n := range c.nodes {
		if n.ID == "" || n.SourceFile != "" || strings.Contains(n.Label, ".") {
			continue
		}

		pk, ok := pkgs[n.OriginFile]
		if !ok || typeParams[n.OriginFile][n.Label] {
			continue
		}

		if d := decls[pk][n.Label]; len(d) == 1 && d[0].ID != n.ID {
			remap[key{n.ID, c.sourceKey(n.OriginFile)}] = d[0].ID
			targets[n.ID] = true
		}
	}

	if len(remap) == 0 {
		return
	}

	for _, e := range c.edges {
		sk := c.sourceKey(e.SourceFile)
		if v, ok := remap[key{e.Source, sk}]; ok {
			e.Source = v
		}

		if v, ok := remap[key{e.Target, sk}]; ok {
			e.Target = v
		}
	}

	referenced := map[string]bool{}
	for _, e := range c.edges {
		referenced[e.Source] = true
		referenced[e.Target] = true
	}

	nodes := c.nodes[:0]

	for _, n := range c.nodes {
		if targets[n.ID] && !referenced[n.ID] {
			continue
		}

		nodes = append(nodes, n)
	}

	c.nodes = nodes
}

// goTypeParameters returns, per Go file, the type parameter names its
// declarations introduce; the extractor records them on the extraction.
func (c *corpus) goTypeParameters() map[string]map[string]bool {
	out := map[string]map[string]bool{}

	for _, fr := range c.per {
		if fr.ex != nil && len(fr.ex.GoTypeParams) > 0 {
			out[fr.path] = fr.ex.GoTypeParams
		}
	}

	return out
}
