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

	var (
		imports []pyImportFact
		exports = map[string]map[string][2]string{} // file -> exported -> (target, name)
		uses    []pyUseFact
		files   []string
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
			if !ok {
				return false
			}

			target := resolvePyModule(module, fr.Path, root, level)
			if target == "" {
				return false
			}

			for _, nm := range pyImportedNames(n) {
				if filepath.Base(target) == "__init__.py" {
					pkg := filepath.Dir(target)
					if isFile(filepath.Join(pkg, nm[0]+".py")) || isFile(filepath.Join(pkg, nm[0], "__init__.py")) {
						continue
					}
				}

				imports = append(imports, pyImportFact{fr.Path, nm[1], target, nm[0], n.Line()})

				if filepath.Base(fr.Path) == "__init__.py" {
					if exports[fr.Path] == nil {
						exports[fr.Path] = map[string][2]string{}
					}

					exports[fr.Path][nm[1]] = [2]string{target, nm[0]}
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

	if len(imports) == 0 {
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

	add := func(src, tgt, rel, ctx string, line int, file string) {
		k := [4]string{src, tgt, rel, ctx}
		if existing[k] {
			return
		}

		existing[k] = true
		edges = append(edges, &model.Edge{
			Source: src, Target: tgt, Relation: rel, Context: ctx, Confidence: model.Extracted,
			SourceFile: file, SourceLocation: base.Loc(line), Weight: 1,
		})
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
