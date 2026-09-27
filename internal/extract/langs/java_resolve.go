package langs

import (
	"os"
	"strings"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// resolveJavaTypeReferences re-points stub-bound inherits/implements/imports/
// references edges to the exact definition using each file's package and
// import statements; external imports park on FQN-labeled stubs so the
// bare-label stub rewire cannot fabricate a collision.
//
// Adapted from Graphify's _resolve_java_type_references (Apache-2.0).
func resolveJavaTypeReferences(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	nodes, edges := *nodesP, *edgesP

	pkgByFile := map[string]string{}
	importsByFile := map[string]map[string]string{}

	for _, fr := range per {
		if !strings.HasSuffix(fr.Path, ".java") {
			continue
		}

		srcs := map[string]bool{}

		for _, n := range fr.Ex.Nodes {
			if n.SourceFile != "" {
				srcs[n.SourceFile] = true
			}
		}

		if len(srcs) == 0 {
			continue
		}

		raw, err := os.ReadFile(fr.Path)
		if err != nil {
			continue
		}

		tree, err := tsx.Parse("java", raw)
		if err != nil {
			continue
		}

		pkg := ""
		imps := map[string]string{}

		tree.Root.Walk(func(n *tsx.Node) bool {
			switch n.Type() {
			case "package_declaration":
				pkg = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(n.Text()), "package")), ";"))
			case "import_declaration":
				body := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(n.Text()), "import")), ";"))
				body = strings.TrimSpace(strings.TrimPrefix(body, "static "))

				if strings.HasSuffix(body, ".*") || !strings.Contains(body, ".") {
					return true
				}

				simple := lastSeg(body, ".")
				if simple != "" && simple[0] >= 'A' && simple[0] <= 'Z' {
					imps[simple] = body
				}
			}

			return true
		})
		tree.Release()

		for s := range srcs {
			pkgByFile[s] = pkg
			importsByFile[s] = imps
		}
	}

	if len(pkgByFile) == 0 {
		return
	}

	byID := map[string]*model.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}

	typeParent := map[string]string{}

	for _, e := range edges {
		if e.Relation != "contains" {
			continue
		}

		child, parent := byID[e.Target], byID[e.Source]
		if child == nil || parent == nil {
			continue
		}

		if child.SourceFile != "" && parent.SourceFile == child.SourceFile && isUpperStart(parent.Label) &&
			!strings.HasSuffix(parent.Label, ".java") {
			typeParent[child.ID] = parent.ID
		}
	}

	fqn := map[string]string{}
	setDefault := func(k, v string) {
		if _, ok := fqn[k]; !ok {
			fqn[k] = v
		}
	}

	for _, n := range nodes {
		pkg, ok := pkgByFile[n.SourceFile]
		if n.Label == "" || n.SourceFile == "" || n.ID == "" || !ok {
			continue
		}

		if !isUpperStart(n.Label) || strings.HasSuffix(n.Label, ")") || strings.HasSuffix(n.Label, ".java") {
			continue
		}

		setDefault(joinPkg(pkg, n.Label), n.ID)

		path := []string{n.Label}
		seen := map[string]bool{n.ID: true}

		for p := typeParent[n.ID]; p != "" && !seen[p]; p = typeParent[p] {
			seen[p] = true
			path = append(path, byID[p].Label)
		}

		if len(path) > 1 {
			for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
				path[i], path[j] = path[j], path[i]
			}

			setDefault(joinPkg(pkg, strings.Join(path, ".")), n.ID)
		}
	}

	stubLabel := map[string]string{}

	for _, n := range nodes {
		if n.ID != "" && n.SourceFile == "" && (isUpperStart(n.Label) || strings.Contains(n.Label, ".")) {
			stubLabel[n.ID] = n.Label
		}
	}

	if len(stubLabel) == 0 {
		return
	}

	repoint := map[string]bool{"implements": true, "inherits": true, "extends": true, "imports": true, "references": true}
	nodeIDs := map[string]bool{}

	for _, n := range nodes {
		nodeIDs[n.ID] = true
	}

	externals := map[string]string{}
	external := func(f string) string {
		if id, ok := externals[f]; ok {
			return id
		}

		id := ids.MakeID(f)
		if !nodeIDs[id] {
			nodes = append(nodes, &model.Node{ID: id, Label: f, FileType: model.FileTypeCode})
			nodeIDs[id] = true
		}

		externals[f] = id

		return id
	}

	repointed := map[string]bool{}

	for _, e := range edges {
		if !repoint[e.Relation] {
			continue
		}

		tgt := e.Target

		label, ok := stubLabel[tgt]
		if !ok {
			continue
		}

		if strings.Contains(label, ".") {
			if r, ok := fqn[label]; ok && r != tgt {
				e.Target = r
				repointed[tgt] = true
			}

			continue
		}

		var resolved string

		if f, ok := importsByFile[e.SourceFile][label]; ok {
			resolved = fqn[f]
			if resolved == "" {
				head := strings.Split(f, ".")
				head = head[:len(head)-1]

				for resolved == "" && len(head) > 0 && isUpperStart(head[len(head)-1]) {
					head = head[:len(head)-1]
					resolved = fqn[strings.Join(append(append([]string(nil), head...), label), ".")]
				}
			}

			if resolved == "" {
				e.Target = external(f)
				repointed[tgt] = true

				continue
			}
		} else {
			resolved = fqn[joinPkg(pkgByFile[e.SourceFile], label)]
		}

		if resolved != "" && resolved != tgt {
			e.Target = resolved
			repointed[tgt] = true
		}
	}

	type attrKey struct{ s, t, rel, ctx, sf, loc string }

	seenAttr := map[attrKey]bool{}
	kept := edges[:0]

	for _, e := range edges {
		if e.Relation == "references" && e.Context == "attribute" {
			if _, ok := pkgByFile[e.SourceFile]; ok {
				k := attrKey{e.Source, e.Target, e.Relation, e.Context, e.SourceFile, e.SourceLocation}
				if seenAttr[k] {
					continue
				}

				seenAttr[k] = true
			}
		}

		kept = append(kept, e)
	}

	edges = kept

	if len(repointed) > 0 {
		ref := map[string]bool{}
		for _, e := range edges {
			ref[e.Source] = true
			ref[e.Target] = true
		}

		kn := nodes[:0]

		for _, n := range nodes {
			if repointed[n.ID] && !ref[n.ID] {
				continue
			}

			kn = append(kn, n)
		}

		nodes = kn
	}

	*nodesP, *edgesP = nodes, edges
}

func isUpperStart(s string) bool { return s != "" && s[0] >= 'A' && s[0] <= 'Z' }

func joinPkg(pkg, label string) string {
	if pkg == "" {
		return label
	}

	return pkg + "." + label
}
