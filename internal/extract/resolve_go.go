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
