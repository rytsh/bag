package langs

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/model"
)

var pyKeyRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func pyKey(label string) string { return strings.ToLower(pyKeyRe.ReplaceAllString(label, "")) }

// resolvePythonMemberCalls binds qualified Python member calls the shared
// pass skips: `ClassName.method()` to the single class owning that method,
// and `module.func()` (or an `as` alias) to the single callable an imported
// module contains.
//
// Adapted from Graphify's _resolve_python_member_calls (Apache-2.0).
func resolvePythonMemberCalls(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	// Graphify runs this pass whenever the corpus has a .py file, over every
	// language's raw calls (receivers are language-neutral here).
	hasPy := false

	for _, fr := range per {
		if strings.HasSuffix(fr.Path, ".py") {
			hasPy = true

			break
		}
	}

	if !hasPy {
		return
	}

	var calls []*model.RawCall

	for _, fr := range per {
		if fr.Ex == nil {
			continue
		}

		for _, rc := range fr.Ex.RawCalls {
			if !rc.AmbiguousPyImport && rc.IsMemberCall && rc.Receiver != "" && rc.Callee != "" && rc.CallerID != "" {
				calls = append(calls, rc)
			}
		}
	}

	if len(calls) == 0 {
		return
	}

	nodes, edges := *nodesP, *edgesP

	byID := make(map[string]*model.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}

	classDefs := map[string][]string{}
	methodIndex := map[[2]string]string{}
	children := map[string]map[string][]string{}
	fileOf := map[string]string{}
	imported := map[string]map[string]bool{}
	aliasOf := map[string]map[string]string{}

	for _, e := range edges {
		switch e.Relation {
		case "method":
			if c := byID[e.Source]; c != nil {
				classDefs[pyKey(c.Label)] = append(classDefs[pyKey(c.Label)], e.Source)
			}

			if t := byID[e.Target]; t != nil {
				methodIndex[[2]string{e.Source, pyKey(t.Label)}] = e.Target
			}
		case "contains":
			if t := byID[e.Target]; t != nil {
				if children[e.Source] == nil {
					children[e.Source] = map[string][]string{}
				}

				k := pyKey(t.Label)
				children[e.Source][k] = append(children[e.Source][k], e.Target)
				fileOf[e.Target] = e.Source
			}
		case "imports", "imports_from":
			if imported[e.Source] == nil {
				imported[e.Source] = map[string]bool{}
			}

			imported[e.Source][e.Target] = true

			if e.LocalAlias != "" {
				if aliasOf[e.Source] == nil {
					aliasOf[e.Source] = map[string]string{}
				}

				aliasOf[e.Source][e.Target] = pyKey(e.LocalAlias)
			}
		}
	}

	for k, v := range classDefs {
		classDefs[k] = uniqueSorted(v)
	}

	moduleStemKey := func(id string) string {
		n := byID[id]
		if n == nil {
			return ""
		}

		stem := ""
		if n.SourceFile != "" {
			b := filepath.Base(n.SourceFile)
			stem = strings.TrimSuffix(b, filepath.Ext(b))
		}

		if stem == "" {
			stem = n.Label
		}

		return pyKey(stem)
	}

	existing := map[[2]string]bool{}
	for _, e := range edges {
		existing[[2]string{e.Source, e.Target}] = true
	}

	emit := func(caller, target string, rc *model.RawCall) {
		if target == "" || target == caller || existing[[2]string{caller, target}] {
			return
		}

		existing[[2]string{caller, target}] = true
		score := 1.0
		edges = append(edges, &model.Edge{
			Source: caller, Target: target, Relation: "calls", Context: "call",
			Confidence: model.Extracted, ConfidenceScore: &score,
			SourceFile: rc.SourceFile, SourceLocation: rc.SourceLocation, Weight: 1,
		})
	}

	for _, rc := range calls {
		recv := strings.TrimPrefix(rc.Receiver, "this.")

		if upperStart(recv) {
			defs := classDefs[pyKey(recv)]
			if len(defs) != 1 {
				continue
			}

			emit(rc.CallerID, methodIndex[[2]string{defs[0], pyKey(rc.Callee)}], rc)

			continue
		}

		rkey := pyKey(recv)
		callerFile := fileOf[rc.CallerID]
		aliases := aliasOf[callerFile]

		var mods []string

		for t := range imported[callerFile] {
			if _, contained := fileOf[t]; contained {
				continue
			}
			if _, ok := children[t]; ok && (moduleStemKey(t) == rkey || aliases[t] == rkey) {
				mods = append(mods, t)
			}
		}

		if len(mods) != 1 {
			continue
		}

		if ch := children[mods[0]][pyKey(rc.Callee)]; len(ch) == 1 {
			emit(rc.CallerID, ch[0], rc)
		}
	}

	*nodesP, *edgesP = nodes, edges
}

func uniqueSorted(v []string) []string {
	seen := map[string]bool{}

	var out []string

	for _, s := range v {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}

	sort.Strings(out)

	return out
}
