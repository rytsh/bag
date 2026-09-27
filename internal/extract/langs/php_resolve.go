package langs

import (
	"os"
	"strings"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

var (
	phpSupertypeRelations = map[string]bool{"inherits": true, "implements": true, "mixes_in": true}
	phpRepointRelations   = map[string]bool{"inherits": true, "implements": true, "mixes_in": true, "imports": true, "references": true}
)

func phpFQNFromRaw(raw, ns string, uses map[string]string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, `\`) {
		return raw[1:]
	}

	if strings.Contains(raw, `\`) {
		first, rest, _ := strings.Cut(raw, `\`)
		if m, ok := uses[strings.ToLower(first)]; ok {
			return m + `\` + rest
		}

		if ns != "" {
			return ns + `\` + raw
		}

		return raw
	}

	if m, ok := uses[strings.ToLower(raw)]; ok {
		return m
	}

	if ns != "" {
		return ns + `\` + raw
	}

	return raw
}

// resolvePHPTypeReferences disambiguates PHP heritage/import/reference
// targets using namespace + use declarations.
//
// Adapted from Graphify's _resolve_php_type_references (Apache-2.0).
func resolvePHPTypeReferences(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	nodes, edges := *nodesP, *edgesP

	type rawKey struct{ rel, bare string }

	nsByFile := map[string]string{}
	usesByFile := map[string]map[string]string{}
	rawByFile := map[string]map[rawKey]*string{}

	for _, fr := range per {
		if !strings.HasSuffix(strings.ToLower(fr.Path), ".php") || strings.HasSuffix(strings.ToLower(fr.Path), ".blade.php") {
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

		src, err := os.ReadFile(fr.Path)
		if err != nil {
			continue
		}

		tree, err := tsx.Parse("php", src)
		if err != nil {
			continue
		}

		var namespaces []string

		uses := map[string]string{}
		raws := map[rawKey]*string{}

		record := func(rel, raw string) {
			bare := strings.ToLower(strings.TrimSpace(lastSeg(raw, `\`)))
			if bare == "" {
				return
			}

			k := rawKey{rel, bare}
			if prev, ok := raws[k]; ok {
				if prev != nil && *prev != raw {
					raws[k] = nil
				}

				return
			}

			r := raw
			raws[k] = &r
		}

		useClause := func(c *tsx.Node, prefix string) {
			target, alias, sawAs := "", "", false

			for _, s := range c.Children() {
				switch s.Type() {
				case "function", "const":
					return
				case "as":
					sawAs = true
				case "qualified_name", "name":
					if sawAs {
						alias = s.Text()
					} else if target == "" {
						target = s.Text()
					}
				}
			}

			if target == "" {
				return
			}

			fqn := target
			if prefix != "" {
				fqn = prefix + `\` + target
			}

			fqn = strings.TrimLeft(fqn, `\`)

			key := alias
			if key == "" {
				key = lastSeg(fqn, `\`)
			}

			key = strings.ToLower(strings.TrimSpace(key))
			if _, ok := uses[key]; key != "" && !ok {
				uses[key] = fqn
			}
		}

		var walk func(n *tsx.Node)
		walk = func(n *tsx.Node) {
			switch n.Type() {
			case "namespace_definition":
				if c := n.ChildOfType("namespace_name"); c != nil {
					namespaces = append(namespaces, c.Text())
				}
			case "namespace_use_declaration":
				prefix := ""

				var group *tsx.Node

				for _, c := range n.Children() {
					switch c.Type() {
					case "namespace_name":
						prefix = c.Text()
					case "namespace_use_group":
						group = c
					case "namespace_use_clause":
						useClause(c, "")
					}
				}

				if group != nil {
					for _, c := range group.Children() {
						if c.Type() == "namespace_use_clause" {
							useClause(c, prefix)
						}
					}
				}

				return
			case "class_declaration":
				for _, c := range n.Children() {
					switch c.Type() {
					case "base_clause":
						for _, s := range c.Children() {
							if s.Type() == "name" || s.Type() == "qualified_name" {
								record("inherits", s.Text())
							}
						}
					case "class_interface_clause":
						for _, s := range c.Children() {
							if s.Type() == "name" || s.Type() == "qualified_name" {
								record("implements", s.Text())
							}
						}
					case "declaration_list":
						for _, m := range c.Children() {
							if m.Type() != "use_declaration" {
								continue
							}

							for _, s := range m.Children() {
								if s.Type() == "name" || s.Type() == "qualified_name" {
									record("mixes_in", s.Text())
								}
							}
						}
					}
				}
			}

			for _, c := range n.Children() {
				walk(c)
			}
		}

		walk(tree.Root)
		tree.Release()

		distinct := map[string]bool{}
		for _, ns := range namespaces {
			distinct[ns] = true
		}

		if len(distinct) > 1 {
			continue
		}

		ns := ""
		if len(namespaces) > 0 {
			ns = namespaces[0]
		}

		for s := range srcs {
			nsByFile[s] = ns
			usesByFile[s] = uses
			rawByFile[s] = raws
		}
	}

	if len(nsByFile) == 0 {
		return
	}

	fqnToID := map[string]string{}

	for _, n := range nodes {
		ns, ok := nsByFile[n.SourceFile]
		if n.Label == "" || n.SourceFile == "" || n.ID == "" || !ok {
			continue
		}

		if strings.HasSuffix(n.Label, ")") || strings.Contains(n.Label, ".") {
			continue
		}

		f := n.Label
		if ns != "" {
			f = ns + `\` + n.Label
		}

		if _, ok := fqnToID[strings.ToLower(f)]; !ok {
			fqnToID[strings.ToLower(f)] = n.ID
		}
	}

	nodeIDs := map[string]bool{}
	stubLabel := map[string]string{}

	for _, n := range nodes {
		nodeIDs[n.ID] = true
		if n.ID != "" && n.SourceFile == "" && n.Label != "" {
			stubLabel[n.ID] = n.Label
		}
	}

	externals := map[string]string{}
	external := func(f string) string {
		k := strings.ToLower(f)
		if id, ok := externals[k]; ok {
			return id
		}

		id := ids.MakeID(f)
		if !nodeIDs[id] {
			nodes = append(nodes, &model.Node{ID: id, Label: f, FileType: model.FileTypeCode})
			nodeIDs[id] = true
		}

		externals[k] = id

		return id
	}

	repointed := map[string]bool{}

	for _, e := range edges {
		if !phpRepointRelations[e.Relation] {
			continue
		}

		ns, ok := nsByFile[e.SourceFile]
		if !ok {
			continue
		}

		tgt := e.Target
		label := stubLabel[tgt]
		uses := usesByFile[e.SourceFile]

		if label == "" && e.Relation == "imports" {
			for alias := range uses {
				if ids.MakeID(alias) == tgt {
					label = alias

					break
				}
			}
		}

		if label == "" {
			continue
		}

		bare := strings.ToLower(strings.TrimSpace(label))

		var raw string

		if phpSupertypeRelations[e.Relation] {
			if r := rawByFile[e.SourceFile][rawKey{e.Relation, bare}]; r != nil {
				raw = *r
			}
		}

		explicit := false

		var f string

		switch {
		case raw != "" && strings.Contains(raw, `\`):
			f, explicit = phpFQNFromRaw(raw, ns, uses), true
		case uses[bare] != "":
			f, explicit = uses[bare], true
		case ns != "":
			f = ns + `\` + label
		default:
			continue
		}

		resolved, found := fqnToID[strings.ToLower(f)]

		switch {
		case found && resolved != tgt:
			e.Target = resolved
			repointed[tgt] = true
		case explicit && !found:
			e.Target = external(f)
			repointed[tgt] = true
		}
	}

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
