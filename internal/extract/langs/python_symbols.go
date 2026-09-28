package langs

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// Python symbol-level import resolution: `from pkg.mod import name` becomes
// an exact file --imports--> symbol edge, and a top-level function calling an
// imported name gets a --calls--> edge to the definition, following
// re-exports through package __init__ files.
//
// Adapted from Graphify's _collect_python_symbol_resolution_facts /
// _apply_symbol_resolution_facts (Apache-2.0).

type pyImportFact struct {
	file, local, target, imported string
	line                          int
}

type pyModuleImportFact struct {
	file, target, local string
	line                int
}

type pyUseFact struct {
	file, sourceID, local string
	line                  int
}

func pyImportFromModule(n *tsx.Node) (int, string, bool) {
	level, module := 0, ""

	for _, c := range n.Children() {
		if c.Type() == "import" {
			break
		}

		switch c.Type() {
		case "relative_import":
			raw := c.Text()
			level = len(raw) - len(strings.TrimLeft(raw, "."))

			if rest := strings.TrimLeft(raw, "."); rest != "" {
				module = rest
			}

			if d := c.ChildOfType("dotted_name"); d != nil {
				module = d.Text()
			}
		case "dotted_name":
			module = c.Text()
		}
	}

	if level == 0 && module == "" {
		return 0, "", false
	}

	return level, module, true
}

func pyImportedNames(n *tsx.Node) [][2]string {
	var out [][2]string

	past := false

	for _, c := range n.Children() {
		if c.Type() == "import" {
			past = true

			continue
		}

		if !past {
			continue
		}

		switch c.Type() {
		case "dotted_name":
			nm := c.Text()
			out = append(out, [2]string{nm, lastSeg(nm, ".")})
		case "aliased_import":
			nn := c.Field("name")
			if nn == nil {
				continue
			}

			nm := nn.Text()
			local := lastSeg(nm, ".")

			if al := c.Field("alias"); al != nil {
				local = al.Text()
			}

			out = append(out, [2]string{nm, local})
		}
	}

	return out
}

func resolvePythonSymbols(root string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	nodes, edges := *nodesP, *edgesP

	_, ambiguous := pyAmbiguousModules(root, per)

	var (
		imports    []pyImportFact
		exports    = map[string]map[string][2]string{} // file -> exported -> (target, name)
		reExports  []pyImportFact
		modImports []pyModuleImportFact
		uses       []pyUseFact
		files      []string
	)

	for _, fr := range per {
		if !strings.HasSuffix(fr.Path, ".py") {
			continue
		}

		src, err := os.ReadFile(fr.Path)
		if err != nil {
			continue
		}

		tree, err := tsx.Parse("python", src)
		if err != nil {
			continue
		}

		files = append(files, fr.Path)
		stem := base.FileStem(fr.Path)

		tree.Root.Walk(func(n *tsx.Node) bool {
			if n.Type() != "import_from_statement" {
				return true
			}

			level, module, ok := pyImportFromModule(n)
			if !ok || (level == 0 && ambiguous[ids.MakeID(module)]) {
				return false
			}

			target := resolvePyModule(module, fr.Path, root, level)

			// `from pkg import submod` names a submodule file: a package's
			// __init__.py or a PEP 420 namespace directory.
			pkgDir := ""
			if target != "" {
				if filepath.Base(target) == "__init__.py" {
					pkgDir = filepath.Dir(target)
				}
			} else if pkgDir = resolvePyNamespaceDir(module, fr.Path, root, level); pkgDir == "" {
				return false
			}

			for _, nm := range pyImportedNames(n) {
				if pkgDir != "" {
					sub := ""
					if p := filepath.Join(pkgDir, nm[0]+".py"); isFile(p) {
						sub = p
					} else if p := filepath.Join(pkgDir, nm[0], "__init__.py"); isFile(p) {
						sub = p
					}

					if sub != "" {
						imported := nm[0]
						if module != "" {
							imported = module + "." + nm[0]
						}

						if level == 0 && ambiguous[ids.MakeID(imported)] {
							continue
						}

						modImports = append(modImports, pyModuleImportFact{fr.Path, sub, nm[1], n.Line()})

						continue
					}
				}

				if target == "" {
					continue
				}

				f := pyImportFact{fr.Path, nm[1], target, nm[0], n.Line()}
				imports = append(imports, f)

				if filepath.Base(fr.Path) == "__init__.py" {
					if exports[fr.Path] == nil {
						exports[fr.Path] = map[string][2]string{}
					}

					exports[fr.Path][nm[1]] = [2]string{target, nm[0]}
					reExports = append(reExports, f)
				}
			}

			return false
		})

		for _, c := range tree.Root.Children() {
			if c.Type() != "function_definition" {
				continue
			}

			nn, bd := c.Field("name"), c.Field("body")
			if nn == nil || bd == nil {
				continue
			}

			sid := ids.MakeID(stem, nn.Text())

			bd.Walk(func(x *tsx.Node) bool {
				if x.Type() == "call" {
					if fn := x.Field("function"); fn != nil && fn.Type() == "identifier" {
						uses = append(uses, pyUseFact{fr.Path, sid, fn.Text(), x.Line()})
					}
				}

				return true
			})
		}

		tree.Release()
	}

	if len(imports) == 0 && len(modImports) == 0 {
		return
	}

	fileID := map[string]string{}
	for _, f := range files {
		fileID[f] = ids.MakeID(f)
	}

	symbols := map[[2]string]string{}
	members := map[[2]string]bool{}

	for _, n := range nodes {
		if n.SourceFile == "" || n.ID == "" {
			continue
		}

		raw := strings.TrimSpace(n.Label)
		label := strings.TrimLeft(strings.Trim(raw, "()"), ".")

		if label == "" {
			continue
		}

		k := [2]string{n.SourceFile, label}

		if strings.HasPrefix(raw, ".") {
			if _, ok := symbols[k]; ok {
				continue
			}

			members[k] = true
		} else {
			delete(members, k)
		}

		symbols[k] = n.ID
	}

	var resolve func(target, name string, seen map[[2]string]bool) [2]string
	resolve = func(target, name string, seen map[[2]string]bool) [2]string {
		k := [2]string{target, name}
		if seen[k] {
			return k
		}

		seen[k] = true

		if o, ok := exports[target][name]; ok {
			return resolve(o[0], o[1], seen)
		}

		return k
	}

	existing := map[[4]string]bool{}
	for _, e := range edges {
		existing[[4]string{e.Source, e.Target, e.Relation, e.Context}] = true
	}

	add := func(src, tgt, rel, ctx string, line int, file string) *model.Edge {
		k := [4]string{src, tgt, rel, ctx}
		if existing[k] {
			return nil
		}

		existing[k] = true
		e := &model.Edge{
			Source: src, Target: tgt, Relation: rel, Context: ctx, Confidence: model.Extracted,
			SourceFile: file, SourceLocation: base.Loc(line), Weight: 1,
		}
		edges = append(edges, e)

		return e
	}

	// An __init__.py re-export of a name defined in another file.
	for _, f := range reExports {
		if f.target == f.file {
			continue
		}

		if src := fileID[f.file]; src != "" {
			add(src, ids.MakeID(f.target), "re_exports", "export", f.line, f.file)
		}
	}

	aliases := map[string]map[string][2]string{}

	for _, f := range imports {
		if aliases[f.file] == nil {
			aliases[f.file] = map[string][2]string{}
		}

		aliases[f.file][f.local] = [2]string{f.target, f.imported}

		src := fileID[f.file]
		o := resolve(f.target, f.imported, map[[2]string]bool{})

		if tgt, ok := symbols[o]; ok && src != "" {
			add(src, tgt, "imports", "import", f.line, f.file)
		}
	}

	if retracted := pyModuleImportEdges(root, edges, modImports, existing, add); len(retracted) > 0 {
		kept := edges[:0]

		for _, e := range edges {
			if !retracted[e] {
				kept = append(kept, e)
			}
		}

		edges = kept
	}

	owned := map[string]bool{}
	for _, n := range nodes {
		owned[n.ID] = true
	}

	for _, u := range uses {
		a, ok := aliases[u.file][u.local]
		if !ok {
			continue
		}

		o := resolve(a[0], a[1], map[[2]string]bool{})

		tgt, ok := symbols[o]
		if !ok {
			continue
		}

		src := u.sourceID
		if !owned[src] {
			src = fileID[u.file]
			if src == "" {
				continue
			}
		}

		add(src, tgt, "calls", "call", u.line, u.file)
	}

	*nodesP, *edgesP = nodes, edges
}

// pyModuleImportEdges emits file-to-file imports_from edges for package-form
// submodule imports and returns the provisional per-file edges to retract
// (those whose target is only the package's module-file id).
func pyModuleImportEdges(root string, edges []*model.Edge, facts []pyModuleImportFact,
	existing map[[4]string]bool, add func(src, tgt, rel, ctx string, line int, file string) *model.Edge,
) map[*model.Edge]bool {
	if len(facts) == 0 {
		return nil
	}

	provisional := map[[2]string][]*model.Edge{}

	for _, e := range edges {
		if e.Relation == "imports_from" && e.Context == "import" && e.SourceLocation != "" {
			k := [2]string{e.SourceFile, e.SourceLocation}
			provisional[k] = append(provisional[k], e)
		}
	}

	retracted := map[*model.Edge]bool{}

	for _, f := range facts {
		fromRel, err1 := filepath.Rel(root, f.file)
		toRel, err2 := filepath.Rel(root, f.target)

		if err1 != nil || err2 != nil || strings.HasPrefix(fromRel, "..") || strings.HasPrefix(toRel, "..") {
			continue
		}

		src := ids.MakeID(base.FileStem(fromRel))
		tgt := ids.MakeID(base.FileStem(toRel))

		pkgDir := filepath.Dir(f.target)
		cands := map[string]bool{
			ids.MakeID(pkgDir):         true,
			ids.MakeID(pkgDir + ".py"): true,
		}

		if pr, err := filepath.Rel(root, pkgDir); err == nil && !strings.HasPrefix(pr, "..") {
			cands[ids.MakeID(strings.Join(strings.Split(filepath.ToSlash(pr), "/"), "."))] = true
			cands[ids.MakeID(base.FileStem(pr))] = true
			cands[ids.MakeID(pr)] = true
		}

		if fp, err := filepath.Rel(root, filepath.Dir(f.file)); err == nil && !strings.HasPrefix(fp, "..") {
			cands[ids.MakeID(fp+".py")] = true
		}

		for _, e := range provisional[[2]string{f.file, base.Loc(f.line)}] {
			if !retracted[e] && cands[e.Target] {
				retracted[e] = true
				delete(existing, [4]string{e.Source, e.Target, e.Relation, e.Context})

				break
			}
		}

		if e := add(src, tgt, "imports_from", "submodule_import", f.line, f.file); e != nil {
			if stem := strings.TrimSuffix(filepath.Base(f.target), ".py"); f.local != stem {
				e.LocalAlias = f.local
			}
		}
	}

	return retracted
}
