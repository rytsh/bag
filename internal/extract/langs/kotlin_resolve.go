package langs

import (
	"path/filepath"
	"strings"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/model"
)

// ktNavSegments flattens a navigation_expression chain of plain identifiers
// (`com.pkg.Foo.bar` -> [com pkg Foo bar]); nil when any segment is not a
// plain identifier. Handles both the `<recv> . <ident>` and the
// `<recv> navigation_suffix(. <ident>)` grammar shapes.
//
// Adapted from Graphify's _kotlin_nav_identifier_segments (Apache-2.0).
func ktNavSegments(nav *tsx.Node) []string {
	var segs []string

	n := nav
	for n != nil && n.Type() == "navigation_expression" {
		named := n.NamedChildren()
		if len(named) != 2 {
			return nil
		}

		head, tail := named[0], named[1]
		if tail.Type() == "navigation_suffix" {
			tail = tail.ChildOfType("simple_identifier", "identifier")
			if tail == nil {
				return nil
			}
		}

		if tail.Type() != "simple_identifier" && tail.Type() != "identifier" {
			return nil
		}

		segs = append(segs, tail.Text())
		n = head
	}

	if n == nil || (n.Type() != "simple_identifier" && n.Type() != "identifier") {
		return nil
	}

	segs = append(segs, n.Text())

	for i, j := 0, len(segs)-1; i < j; i, j = i+1, j-1 {
		segs[i], segs[j] = segs[j], segs[i]
	}

	return segs
}

// ktPackageName returns the dotted package from the file's package_header.
func ktPackageName(root *tsx.Node) string {
	for _, c := range root.Children() {
		if c.Type() != "package_header" {
			continue
		}

		if id := c.ChildOfType("qualified_identifier", "identifier"); id != nil {
			return strings.Join(strings.Fields(strings.ReplaceAll(id.Text(), "\n", " ")), "")
		}

		return ""
	}

	return ""
}

func ktDecorateRawCall(_ *generic.Ctx, n *tsx.Node, rc *model.RawCall) {
	ch := n.Children()
	if len(ch) == 0 || ch[0].Type() != "navigation_expression" {
		return
	}

	segs := ktNavSegments(ch[0])

	switch {
	case len(segs) >= 3:
		rc.QualifiedPrefix = strings.Join(segs[:len(segs)-1], ".")
	case len(segs) == 2 && upperStart(segs[0]):
		rc.ReceiverType = segs[0]
	}
}

func ktPostProcess(x *generic.Ctx, res *model.Extraction) {
	if pkg := ktPackageName(x.Tree.Root); pkg != "" {
		res.Package = pkg
	}
}

func isKotlinFile(p string) bool {
	return strings.HasSuffix(p, ".kt") || strings.HasSuffix(p, ".kts")
}

// resolveKotlinImportTargets rewrites Kotlin `imports` edges from the bare
// last segment to the node the written FQN names.
//
// Adapted from Graphify's _resolve_kotlin_import_targets (Apache-2.0).
func resolveKotlinImportTargets(_ string, _ *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	pkgSymbols := map[string]map[string][]string{}

	for _, fr := range per {
		if fr.Ex.Package == "" || !isKotlinFile(fr.Path) {
			continue
		}

		by := pkgSymbols[fr.Ex.Package]
		if by == nil {
			by = map[string][]string{}
			pkgSymbols[fr.Ex.Package] = by
		}

		for _, n := range fr.Ex.Nodes {
			if n.SourceFile == "" || n.Type == "namespace" || n.Label == "" || strings.HasPrefix(n.Label, ".") {
				continue
			}

			k := strings.Trim(n.Label, "()")
			by[k] = append(by[k], n.ID)
		}
	}

	if len(pkgSymbols) == 0 {
		return
	}

	for _, e := range *edgesP {
		if e.Relation != "imports" || !isKotlinFile(e.SourceFile) {
			continue
		}

		fqn := mdString(e.Metadata, "target_fqn")

		i := strings.LastIndex(fqn, ".")
		if i <= 0 || i == len(fqn)-1 {
			continue
		}

		if c := pkgSymbols[fqn[:i]][fqn[i+1:]]; len(c) == 1 {
			e.Target = c[0]
		}
	}
}

// resolveKotlinQualifiedCalls resolves `com.pkg.fn()` and
// `com.pkg.Type.method()` calls, and `Receiver.method()` calls whose
// object/class lives in another file.
//
// Adapted from Graphify's _resolve_kotlin_qualified_calls and
// _resolve_kotlin_member_calls (Apache-2.0).
func resolveKotlinQualifiedCalls(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	nodes, edges := *nodesP, *edgesP

	byID := make(map[string]*model.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}

	contains := map[string][]string{}
	methodsOf := map[string][]string{}

	for _, e := range edges {
		switch e.Relation {
		case "contains":
			contains[e.Source] = append(contains[e.Source], e.Target)
		case "method":
			methodsOf[e.Source] = append(methodsOf[e.Source], e.Target)
		}
	}

	pkgCallables := map[string]map[string][]string{}
	pkgTypes := map[string]map[string][]string{}

	for _, fr := range per {
		pkg := fr.Ex.Package
		if pkg == "" || !isKotlinFile(fr.Path) {
			continue
		}

		if pkgCallables[pkg] == nil {
			pkgCallables[pkg] = map[string][]string{}
			pkgTypes[pkg] = map[string][]string{}
		}

		fileID := ""

		for _, n := range fr.Ex.Nodes {
			if n.SourceFile != "" && n.Label == filepath.Base(n.SourceFile) {
				fileID = n.ID

				break
			}
		}

		if fileID == "" {
			continue
		}

		for _, t := range contains[fileID] {
			n := byID[t]
			if n == nil || n.SourceFile == "" {
				continue
			}

			name := strings.Trim(n.Label, "()")
			if name == "" || strings.HasPrefix(name, ".") {
				continue
			}

			if n.Callable {
				pkgCallables[pkg][name] = append(pkgCallables[pkg][name], t)
			}

			if n.CallableClass {
				pkgTypes[pkg][name] = append(pkgTypes[pkg][name], t)
			}
		}
	}

	typesByName := map[string][]string{}

	for _, n := range nodes {
		if n.CallableClass && isKotlinFile(n.SourceFile) && n.Label != "" {
			typesByName[n.Label] = append(typesByName[n.Label], n.ID)
		}
	}

	methodNamed := func(typeID, callee string) []string {
		var out []string

		for _, m := range methodsOf[typeID] {
			if n := byID[m]; n != nil && strings.Trim(n.Label, "()") == "."+callee {
				out = append(out, m)
			}
		}

		return out
	}

	existing := map[[2]string]bool{}
	for _, e := range edges {
		existing[[2]string{e.Source, e.Target}] = true
	}

	emit := func(rc *model.RawCall, cands []string) {
		if len(cands) != 1 {
			return
		}

		tgt := cands[0]
		if tgt == rc.CallerID || existing[[2]string{rc.CallerID, tgt}] {
			return
		}

		existing[[2]string{rc.CallerID, tgt}] = true
		edges = append(edges, &model.Edge{
			Source: rc.CallerID, Target: tgt, Relation: "calls", Context: "call",
			Confidence: model.Extracted, ConfidenceScore: model.Score(1.0),
			SourceFile: rc.SourceFile, SourceLocation: rc.SourceLocation, Weight: 1,
		})
	}

	var qualified, object []*model.RawCall

	for _, fr := range per {
		for _, rc := range fr.Ex.RawCalls {
			if rc.Language != "kotlin" || rc.Callee == "" || rc.CallerID == "" {
				continue
			}

			switch {
			case rc.QualifiedPrefix != "":
				qualified = append(qualified, rc)
			case rc.ReceiverType != "" && rc.IsMemberCall:
				object = append(object, rc)
			}
		}
	}

	if len(pkgCallables) > 0 {
		for _, rc := range qualified {
			prefix := rc.QualifiedPrefix

			if c, ok := pkgCallables[prefix]; ok {
				emit(rc, c[rc.Callee])

				continue
			}

			i := strings.LastIndex(prefix, ".")
			if i <= 0 {
				continue
			}

			if t := pkgTypes[prefix[:i]][prefix[i+1:]]; len(t) == 1 {
				emit(rc, methodNamed(t[0], rc.Callee))
			}
		}
	}

	for _, rc := range object {
		if t := typesByName[rc.ReceiverType]; len(t) == 1 {
			emit(rc, methodNamed(t[0], rc.Callee))
		}
	}

	*edgesP = edges
}
