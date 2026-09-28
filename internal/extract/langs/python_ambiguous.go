package langs

import (
	"crypto/sha1" //nolint:gosec // id salt, mirrors Graphify
	"encoding/hex"
	"path/filepath"
	"strings"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// pythonModuleAliasFiles maps every importable absolute module id to the
// scanned files it names, probing each in-root, non-package ancestor as a
// sys.path root. The second result holds ids that resolve from the scan root
// itself, which the shared resolver prefers.
//
// Adapted from Graphify's _python_absolute_import_alias_files (Apache-2.0).
func pythonModuleAliasFiles(paths []string, root string) (map[string]map[string]bool, map[string]bool) {
	isPkg := map[string]bool{}
	pkg := func(dir string) bool {
		v, ok := isPkg[dir]
		if !ok {
			v = isFile(filepath.Join(dir, "__init__.py"))
			isPkg[dir] = v
		}

		return v
	}

	aliases := map[string]map[string]bool{}
	scanRoot := map[string]bool{}

	for _, p := range paths {
		if strings.ToLower(filepath.Ext(p)) != ".py" {
			continue
		}

		if r, err := filepath.Rel(root, p); err != nil || strings.HasPrefix(r, "..") {
			continue
		}

		modPath := strings.TrimSuffix(p, filepath.Ext(p))
		if filepath.Base(p) == "__init__.py" {
			modPath = filepath.Dir(p)
		}

		roots := []string{root}

		for anc := filepath.Dir(p); anc != root; anc = filepath.Dir(anc) {
			if r, err := filepath.Rel(root, anc); err != nil || strings.HasPrefix(r, "..") || filepath.Dir(anc) == anc {
				break
			}

			if !pkg(anc) {
				roots = append(roots, anc)
			}
		}

		for _, cr := range roots {
			rel, err := filepath.Rel(cr, modPath)
			if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
				continue
			}

			parts := strings.Split(filepath.ToSlash(rel), "/")

			ok := true

			for _, part := range parts {
				if !isIdentifier(part) {
					ok = false

					break
				}
			}

			if !ok || probePy(filepath.Join(cr, filepath.Join(parts...))) != p {
				continue
			}

			id := ids.MakeID(strings.Join(parts, "."))
			if aliases[id] == nil {
				aliases[id] = map[string]bool{}
			}

			aliases[id][p] = true

			if cr == root {
				scanRoot[id] = true
			}
		}
	}

	return aliases, scanRoot
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}

	for i, r := range s {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 127 {
			continue
		}

		if i > 0 && r >= '0' && r <= '9' {
			continue
		}

		return false
	}

	return true
}

// pyAmbiguousModules returns the module alias files of the corpus' Python
// files and the absolute module ids that name more than one of them.
func pyAmbiguousModules(root string, per []extract.FileResult) (map[string]map[string]bool, map[string]bool) {
	var paths []string

	for _, fr := range per {
		if strings.HasSuffix(fr.Path, ".py") {
			paths = append(paths, fr.Path)
		}
	}

	if len(paths) == 0 {
		return nil, nil
	}

	aliasFiles, scanRoot := pythonModuleAliasFiles(paths, root)

	amb := map[string]bool{}
	for id, files := range aliasFiles {
		if len(files) > 1 && !scanRoot[id] {
			amb[id] = true
		}
	}

	return aliasFiles, amb
}

// guardAmbiguousPythonImports keeps imports of an absolute module name that
// identifies several scanned files dangling instead of picking one, and
// marks raw calls bound through such imports so no pass resolves them.
//
// Adapted from Graphify's _suppress_ambiguous_python_imports (Apache-2.0).
func guardAmbiguousPythonImports(root string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	edges := *edgesP

	aliasFiles, amb := pyAmbiguousModules(root, per)
	if aliasFiles == nil {
		return
	}

	nodeIDs := map[string]bool{}
	for _, n := range *nodesP {
		nodeIDs[n.ID] = true
	}

	looseSibling := func(pi *model.PyImport, sf, id string) bool {
		tf := pi.TargetFile
		if tf == "" || sf == "" || !aliasFiles[id][tf] || filepath.Dir(tf) != filepath.Dir(sf) {
			return false
		}

		dir := filepath.Dir(sf)

		return !isFile(filepath.Join(dir, "__init__.py")) && !isFile(filepath.Join(dir, "__init__.pyi"))
	}

	ambBindings := map[string]map[string]bool{}
	ambAllCalls := map[string]bool{}
	bind := func(file, local string) {
		if ambBindings[file] == nil {
			ambBindings[file] = map[string]bool{}
		}

		ambBindings[file][local] = true
	}

	kept := edges[:0]

	for _, e := range edges {
		pi := e.PyImport
		if pi == nil {
			kept = append(kept, e)

			continue
		}

		e.PyImport = nil

		id := ids.MakeID(pi.Module)
		ambiguous := amb[id]

		if ambiguous && pi.ModuleBinding && !strings.Contains(pi.Module, ".") && looseSibling(pi, e.SourceFile, id) {
			ambiguous = false
		}

		if pi.ModuleBinding {
			if ambiguous {
				for _, b := range pi.Bindings {
					bind(e.SourceFile, b[1])
				}
			}
		} else {
			for _, b := range pi.Bindings {
				if b[0] == "*" {
					prefix := id + "_"

					hit := ambiguous
					for m := range amb {
						if hit {
							break
						}

						hit = strings.HasPrefix(m, prefix)
					}

					if hit {
						ambAllCalls[e.SourceFile] = true
					}

					continue
				}

				if ambiguous || amb[ids.MakeID(pi.Module+"."+b[0])] {
					bind(e.SourceFile, b[1])
				}
			}
		}

		if pi.MarkerOnly {
			continue
		}

		if ambiguous {
			// A node-less id so the builder leaves the import dangling.
			sum := sha1.Sum([]byte(strings.Join([]string{pi.Module, e.SourceFile, e.SourceLocation, e.Source}, "\x00"))) //nolint:gosec // id salt
			tgt := ids.MakeID("ambiguous_python_import_" + hex.EncodeToString(sum[:])[:12])

			for nodeIDs[tgt] {
				tgt += "_"
			}

			e.Target = tgt
		}

		kept = append(kept, e)
	}

	*edgesP = kept

	for _, fr := range per {
		if fr.Ex == nil {
			continue
		}

		for _, rc := range fr.Ex.RawCalls {
			if ambAllCalls[rc.SourceFile] {
				rc.AmbiguousPyImport = true

				continue
			}

			b := ambBindings[rc.SourceFile]
			if b == nil {
				continue
			}

			recvRoot, _, _ := strings.Cut(rc.Receiver, ".")
			if b[rc.Callee] || b[recvRoot] {
				rc.AmbiguousPyImport = true
			}
		}
	}
}
