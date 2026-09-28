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

// resolvePythonCrossFileImports turns `from x import Name` into INFERRED
// `uses` edges from each class/function that references Name, and re-points
// sourceless type stubs to the imported definition.
//
// Adapted from Graphify's _resolve_cross_file_imports (Apache-2.0).
func resolvePythonCrossFileImports(root string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	nodes, edges := *nodesP, *edgesP

	var py []extract.FileResult

	for _, fr := range per {
		if strings.HasSuffix(fr.Path, ".py") {
			py = append(py, fr)
		}
	}

	if len(py) == 0 {
		return
	}

	_, ambiguous := pyAmbiguousModules(root, per)

	stemEntities := map[string]map[string]string{}
	bareToQualified := map[string]string{}

	for _, fr := range py {
		for _, n := range fr.Ex.Nodes {
			if n.SourceFile == "" {
				continue
			}

			label := n.Label
			if label == "" || strings.HasSuffix(label, ")") || strings.HasSuffix(label, ".py") ||
				strings.HasPrefix(label, "_") || n.FileType == model.FileTypeRationale {
				continue
			}

			fq := base.FileStem(n.SourceFile)
			if stemEntities[fq] == nil {
				stemEntities[fq] = map[string]string{}
			}

			stemEntities[fq][label] = n.ID

			bare := strings.TrimSuffix(filepath.Base(n.SourceFile), filepath.Ext(n.SourceFile))
			if _, ok := bareToQualified[bare]; !ok {
				bareToQualified[bare] = fq
			}
		}
	}

	byID := map[string]*model.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}

	typeRepoint := map[string]bool{"references": true, "inherits": true, "implements": true, "extends": true}
	repointed := map[string]bool{}

	var newEdges []*model.Edge

	for _, fr := range py {
		nameToID := map[string]string{}

		for _, n := range fr.Ex.Nodes {
			if n.SourceFile != fr.Path || n.FileType == model.FileTypeRationale {
				continue
			}

			if n.Label == "" || strings.HasSuffix(n.Label, ".py") {
				continue
			}

			sym := strings.TrimSuffix(n.Label, "()")
			if _, ok := nameToID[sym]; sym != "" && !ok {
				nameToID[sym] = n.ID
			}
		}

		if len(nameToID) == 0 {
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

		importTargets := map[string]string{}
		type refLoc struct {
			id   string
			line int
		}

		refs := map[string][]refLoc{}
		refSeen := map[string]map[string]bool{}

		resolveImport := func(n *tsx.Node) {
			targetFQ, absModule := "", ""

			for _, c := range n.Children() {
				if c.Type() == "relative_import" {
					prefix, dotted := "", ""

					for _, s := range c.Children() {
						switch s.Type() {
						case "import_prefix":
							prefix = s.Text()
						case "dotted_name":
							dotted = s.Text()
						}
					}

					dots := strings.Count(prefix, ".")
					if prefix == "" {
						dots = 1
					}

					dir := filepath.Dir(fr.Path)
					for i := 0; i < dots-1; i++ {
						dir = filepath.Dir(dir)
					}

					cand := filepath.Join(dir, "__init__.py")
					if dotted != "" {
						cand = filepath.Join(append([]string{dir}, strings.Split(dotted, ".")...)...) + ".py"
					}

					targetFQ = base.FileStem(cand)

					break
				}

				if c.Type() == "dotted_name" && targetFQ == "" {
					dotted := c.Text()
					absModule = dotted

					if ambiguous[ids.MakeID(dotted)] {
						return
					}

					asPath := strings.ReplaceAll(dotted, ".", "/")

					if _, ok := stemEntities[asPath]; ok {
						targetFQ = asPath
					} else {
						var matches []string

						for fq := range stemEntities {
							if strings.HasSuffix(fq, "/"+asPath) {
								matches = append(matches, fq)
							}
						}

						if len(matches) == 1 {
							targetFQ = matches[0]
						} else {
							targetFQ = bareToQualified[lastSeg(dotted, ".")]
						}
					}
				}
			}

			ents, ok := stemEntities[targetFQ]
			if targetFQ == "" || !ok {
				return
			}

			past := false

			for _, c := range n.Children() {
				if c.Type() == "import" {
					past = true

					continue
				}

				if !past {
					continue
				}

				var imported, local string

				switch c.Type() {
				case "dotted_name":
					imported, local = c.Text(), c.Text()
				case "aliased_import":
					if nn := c.Field("name"); nn != nil {
						imported, local = nn.Text(), nn.Text()
						if al := c.Field("alias"); al != nil {
							local = al.Text()
						}
					}
				}

				if imported == "" || (absModule != "" && ambiguous[ids.MakeID(absModule+"."+imported)]) {
					continue
				}

				if tgt := ents[imported]; tgt != "" {
					importTargets[local] = tgt
				}
			}
		}

		var visit func(n *tsx.Node, cur string)
		visit = func(n *tsx.Node, cur string) {
			if n.Type() == "import_from_statement" {
				resolveImport(n)

				return
			}

			if cur == "" && (n.Type() == "class_definition" || n.Type() == "function_definition") {
				if nn := n.Field("name"); nn != nil {
					if m, ok := nameToID[nn.Text()]; ok {
						cur = m
					}
				}
			}

			if n.Type() == "identifier" && cur != "" {
				name := n.Text()
				if refSeen[name] == nil {
					refSeen[name] = map[string]bool{}
				}

				if !refSeen[name][cur] {
					refSeen[name][cur] = true
					refs[name] = append(refs[name], refLoc{cur, n.Line()})
				}
			}

			for _, c := range n.Children() {
				visit(c, cur)
			}
		}

		visit(tree.Root, "")
		tree.Release()

		for name, tgt := range importTargets {
			for _, r := range refs[name] {
				if r.id == tgt {
					continue
				}

				newEdges = append(newEdges, &model.Edge{
					Source: r.id, Target: tgt, Relation: "uses", Confidence: model.Inferred,
					ConfidenceScore: model.Score(0.95), SourceFile: fr.Path, SourceLocation: base.Loc(r.line), Weight: 0.8,
				})
			}
		}

		if len(importTargets) > 0 {
			for _, e := range edges {
				if e.SourceFile != fr.Path || !typeRepoint[e.Relation] {
					continue
				}

				tn := byID[e.Target]
				if tn == nil || tn.SourceFile != "" {
					continue
				}

				if r := importTargets[tn.Label]; r != "" && r != e.Target {
					repointed[e.Target] = true
					e.Target = r
				}
			}
		}
	}

	edges = append(edges, newEdges...)

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
