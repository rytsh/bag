package langs

import (
	"regexp"
	"sort"
	"strings"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/model"
)

// rubyNewClassName returns ClassName for a bare `ClassName.new(...)` call.
func rubyNewClassName(n *tsx.Node) string {
	if n == nil || n.Type() != "call" {
		return ""
	}

	recv, meth := n.Field("receiver"), n.Field("method")
	if recv == nil || meth == nil || recv.Type() != "constant" || meth.Text() != "new" {
		return ""
	}

	return recv.Text()
}

// rubyLocalClassBindings maps local var -> ClassName for single, unambiguous
// `var = ClassName.new` bindings in one method body ("" = poisoned).
//
// Adapted from Graphify's _ruby_local_class_bindings (Apache-2.0).
func rubyLocalClassBindings(body *tsx.Node) map[string]string {
	out := map[string]string{}

	var visit func(n *tsx.Node)
	visit = func(n *tsx.Node) {
		for _, c := range n.Children() {
			if c.Type() == "method" || c.Type() == "singleton_method" {
				continue
			}

			if c.Type() == "assignment" {
				left, right := c.Field("left"), c.Field("right")
				if left != nil && left.Type() == "identifier" {
					v := left.Text()
					cls := rubyNewClassName(right)

					prev, seen := out[v]

					switch {
					case cls == "":
						if seen {
							out[v] = ""
						}
					case seen:
						if prev != cls {
							out[v] = ""
						}
					default:
						out[v] = cls
					}
				}
			}

			visit(c)
		}
	}

	visit(body)

	return out
}

func rubyEnclosingMethodBody(n *tsx.Node) *tsx.Node {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Type() == "method" || p.Type() == "singleton_method" {
			return p
		}
	}

	return nil
}

func rubyDecorateRawCall(x *generic.Ctx, n *tsx.Node, rc *model.RawCall) {
	if !rc.IsMemberCall || rc.Receiver == "" {
		return
	}

	m := rubyEnclosingMethodBody(n)
	if m == nil {
		return
	}

	cache, _ := x.Data["rb_bindings"].(map[uint32]map[string]string)
	if cache == nil {
		cache = map[uint32]map[string]string{}
		x.Data["rb_bindings"] = cache
	}

	b, ok := cache[m.StartByte()]
	if !ok {
		body := x.Cfg.FindBody(m)
		if body == nil {
			body = m
		}

		b = rubyLocalClassBindings(body)
		cache[m.StartByte()] = b
	}

	rc.ReceiverType = b[rc.Receiver]
}

var rubyBareConstRe = regexp.MustCompile(`^[A-Z][A-Za-z0-9_]*(?:::[A-Z][A-Za-z0-9_]*)*$`)

func isRubyFile(sf string) bool {
	return strings.HasSuffix(sf, ".rb") || strings.HasSuffix(sf, ".rake")
}

func rubyMethodName(label string) string {
	return strings.TrimLeft(strings.Trim(label, "()"), ".")
}

func rubySegments(label string) []string {
	var out []string

	for _, s := range strings.Split(label, "::") {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}

	return out
}

// resolveRubyMemberCalls resolves Ruby mixins, `Const.new` / `Const.method`
// and `var.method` calls whose receiver is typed by a local `Const.new`
// binding, under single-definition guards.
//
// Implicit-self bare calls promote the existing INFERRED edge to EXTRACTED
// when method ownership and a safe inheritance chain prove the same target.
//
// Adapted from Graphify's resolve_ruby_member_calls (Apache-2.0).
func resolveRubyMemberCalls(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	var calls []*model.RawCall

	for _, fr := range per {
		for _, rc := range fr.Ex.RawCalls {
			if isRubyFile(rc.SourceFile) {
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
		if n.ID != "" {
			byID[n.ID] = n
		}
	}

	var rubyFiles []*model.Node

	for _, n := range nodes {
		if isRubyFile(n.SourceFile) && isFileNodeLabel(n.Label, n.SourceFile) {
			rubyFiles = append(rubyFiles, n)
		}
	}

	contextComplete := len(rubyFiles) > 0
	unsafeFiles := map[string]bool{}

	var externalOwners []string

	for _, f := range rubyFiles {
		if v, _ := f.Metadata["ruby_resolution_schema"].(int); v != 1 {
			if fv, ok := f.Metadata["ruby_resolution_schema"].(float64); !ok || fv != 1 {
				contextComplete = false
			}
		}

		if rubyLookupUnsafe(f) {
			unsafeFiles[f.SourceFile] = true
		}

		if owners, ok := f.Metadata["ruby_external_method_owners"].([]any); ok {
			for _, o := range owners {
				if s, ok := o.(string); ok {
					externalOwners = append(externalOwners, s)
				}
			}
		}
	}

	classDefs := map[string][]string{}
	methods := map[[2]string]string{}
	methodOwners := map[string]map[string]bool{}

	type inhEdge struct {
		src, tgt, raw string
		scopes        []string
		hasScopes     bool
	}

	var inheritEdges []inhEdge

	for _, e := range edges {
		if e.Relation != "inherits" || e.Source == "" || e.Target == "" {
			continue
		}

		ie := inhEdge{src: e.Source, tgt: e.Target}
		if r, ok := e.Metadata["ruby_superclass_ref"].(string); ok {
			ie.raw = r
		}

		if sc, ok := e.Metadata["ruby_lexical_scopes"].([]any); ok {
			ie.hasScopes = true

			for _, x := range sc {
				s, ok := x.(string)
				if !ok {
					ie.hasScopes = false

					break
				}

				ie.scopes = append(ie.scopes, s)
			}
		}

		inheritEdges = append(inheritEdges, ie)
	}

	addClass := func(label, id string) {
		k := memberKey(label)
		classDefs[k] = append(classDefs[k], id)

		if strings.Contains(label, "::") {
			parts := strings.Split(label, "::")
			k = memberKey(parts[len(parts)-1])
			classDefs[k] = append(classDefs[k], id)
		}
	}

	for _, e := range edges {
		if e.Relation != "method" {
			continue
		}

		c := byID[e.Source]
		if c == nil {
			continue
		}

		t := byID[e.Target]
		if !isRubyFile(c.SourceFile) || t == nil || !isRubyFile(t.SourceFile) {
			continue
		}

		addClass(c.Label, e.Source)
		methods[[2]string{e.Source, rubyMethodName(t.Label)}] = e.Target

		if methodOwners[e.Target] == nil {
			methodOwners[e.Target] = map[string]bool{}
		}

		methodOwners[e.Target][e.Source] = true
	}

	for _, n := range nodes {
		if n.ID != "" && isRubyFile(n.SourceFile) && rubyBareConstRe.MatchString(n.Label) {
			addClass(n.Label, n.ID)
		}
	}

	for k, v := range classDefs {
		sort.Strings(v)
		classDefs[k] = uniqSorted(v)
	}

	fqLabel := map[string][]string{}
	lastSeg := map[string][]string{}

	var allClass []string

	seenClass := map[string]bool{}

	for _, v := range classDefs {
		for _, id := range v {
			if !seenClass[id] {
				seenClass[id] = true
				allClass = append(allClass, id)
			}
		}
	}

	sort.Strings(allClass)

	for _, id := range allClass {
		n := byID[id]
		if n == nil {
			continue
		}

		segs := rubySegments(n.Label)
		if len(segs) == 0 {
			continue
		}

		k := strings.Join(segs, "::")
		fqLabel[k] = append(fqLabel[k], id)
		lastSeg[segs[len(segs)-1]] = append(lastSeg[segs[len(segs)-1]], id)
	}

	rubyClassIDs := map[string]bool{}
	classLabels := map[string]map[string]bool{}

	for _, v := range classDefs {
		for _, id := range v {
			rubyClassIDs[id] = true
		}
	}

	for id := range rubyClassIDs {
		l := ""
		if n := byID[id]; n != nil {
			l = n.Label
		}

		if classLabels[l] == nil {
			classLabels[l] = map[string]bool{}
		}

		classLabels[l][id] = true
	}

	resolveBase := func(raw string, scopes []string, hasScopes bool) string {
		if raw == "" || !hasScopes {
			return ""
		}

		var refParts []string

		for _, p := range strings.Split(strings.TrimPrefix(raw, "::"), "::") {
			if p != "" {
				refParts = append(refParts, p)
			}
		}

		if len(refParts) == 0 {
			return ""
		}

		lookup := []string{""}
		if !strings.HasPrefix(raw, "::") && len(scopes) > 0 {
			lookup = scopes
		}

		splitScope := func(scope string) []string {
			var out []string

			for _, p := range strings.Split(scope, "::") {
				if p != "" {
					out = append(out, p)
				}
			}

			return out
		}

		for _, scope := range lookup {
			cand := strings.Join(append(splitScope(scope), refParts...), "::")
			ids := classLabels[cand]

			if len(ids) == 1 {
				for id := range ids {
					return id
				}
			}

			if len(ids) > 1 {
				return ""
			}

			if scope != "" && len(refParts) > 1 {
				if len(classLabels[strings.Join(append(splitScope(scope), refParts[0]), "::")]) > 0 {
					return ""
				}
			}
		}

		return ""
	}

	inheritance := map[string]map[string]bool{}

	for _, ie := range inheritEdges {
		if !rubyClassIDs[ie.src] {
			continue
		}

		resolved := resolveBase(ie.raw, ie.scopes, ie.hasScopes)
		tn := byID[ie.tgt]
		tLabel, tSource := "", ""

		if tn != nil {
			tLabel, tSource = tn.Label, tn.SourceFile
		}

		rawTail := lastSegOf(strings.TrimPrefix(ie.raw, "::"), "::")
		matches := tn != nil && rawTail != "" && memberKey(lastSegOf(tLabel, "::")) == memberKey(rawTail)

		if resolved != "" && (resolved == ie.tgt || (tSource == "" && matches)) {
			if inheritance[ie.src] == nil {
				inheritance[ie.src] = map[string]bool{}
			}

			inheritance[ie.src][resolved] = true
		}
	}

	byKind := map[[3]string]map[string]bool{}
	ambiguousNames := map[[2]string]bool{}

	for mid, owners := range methodOwners {
		name := rubyMethodName(byID[mid].Label)
		kind := rubyMethodKindOf(byID[mid])

		if len(owners) != 1 || kind == "" {
			for o := range owners {
				ambiguousNames[[2]string{o, name}] = true
			}

			continue
		}

		for o := range owners {
			k := [3]string{o, name, kind}
			if byKind[k] == nil {
				byKind[k] = map[string]bool{}
			}

			byKind[k][mid] = true
		}
	}

	hasExternalOwner := func(label string) bool {
		labelParts := rubySplitConst(label)

		for _, raw := range externalOwners {
			prefix := strings.HasSuffix(raw, "::*")
			ref := strings.TrimSuffix(raw, "::*")
			parts := rubySplitConst(strings.TrimPrefix(ref, "::"))

			if len(parts) == 0 {
				continue
			}

			if prefix {
				if strings.HasPrefix(ref, "::") {
					if len(labelParts) >= len(parts) && equalStrs(labelParts[:len(parts)], parts) {
						return true
					}
				} else {
					for i := 0; i+len(parts) <= len(labelParts); i++ {
						if equalStrs(labelParts[i:i+len(parts)], parts) {
							return true
						}
					}
				}

				continue
			}

			if strings.HasPrefix(ref, "::") && equalStrs(labelParts, parts) {
				return true
			}

			if !strings.HasPrefix(ref, "::") && len(labelParts) >= len(parts) &&
				equalStrs(labelParts[len(labelParts)-len(parts):], parts) {
				return true
			}
		}

		return false
	}

	inheritedMethod := func(owner, name, kind string) string {
		cur, first := owner, true
		seen := map[string]bool{}

		for !seen[cur] {
			seen[cur] = true

			n := byID[cur]
			label := ""

			if n != nil {
				label = n.Label
			}

			if !rubyClassIDs[cur] || len(classLabels[label]) != 1 || rubyLookupUnsafe(n) || hasExternalOwner(label) {
				return ""
			}

			if ambiguousNames[[2]string{cur, name}] {
				return ""
			}

			if cands := byKind[[3]string{cur, name, kind}]; len(cands) > 0 {
				if first || len(cands) != 1 {
					return ""
				}

				for c := range cands {
					return c
				}
			}

			bs := inheritance[cur]
			if len(bs) != 1 {
				return ""
			}

			for bb := range bs {
				cur = bb
			}

			first = false
		}

		return ""
	}

	existing := map[[2]string]bool{}
	for _, e := range edges {
		existing[[2]string{e.Source, e.Target}] = true
	}

	emit := func(caller, target, rel, ctx string, rc *model.RawCall) {
		if caller == "" || target == "" || caller == target || existing[[2]string{caller, target}] {
			return
		}

		existing[[2]string{caller, target}] = true
		edges = append(edges, &model.Edge{
			Source: caller, Target: target, Relation: rel, Context: ctx,
			Confidence: model.Extracted, ConfidenceScore: model.Score(1.0),
			SourceFile: rc.SourceFile, SourceLocation: rc.SourceLocation, Weight: 1,
		})
	}

	uniqueClass := func(name string) string {
		if v := classDefs[memberKey(name)]; len(v) == 1 {
			return v[0]
		}

		return ""
	}

	byConstPath := func(raw string) string {
		segs := rubySegments(raw)
		if len(segs) == 0 {
			return ""
		}

		if strings.HasPrefix(strings.TrimSpace(raw), "::") {
			if v := fqLabel[strings.Join(segs, "::")]; len(v) == 1 {
				return v[0]
			}

			return ""
		}

		hits := map[string]bool{}

		var hit string

		for path, ids := range fqLabel {
			p := strings.Split(path, "::")
			if len(p) < len(segs) || strings.Join(p[len(p)-len(segs):], "::") != strings.Join(segs, "::") {
				continue
			}

			for _, id := range ids {
				hits[id] = true
				hit = id
			}
		}

		if len(hits) == 1 {
			return hit
		}

		return ""
	}

	for _, rc := range calls {
		if rc.Language != "mixin" || rc.CallerID == "" || rc.Callee == "" {
			continue
		}

		abs := strings.HasPrefix(rc.Callee, "::")
		ref := rubySegments(rc.Callee)

		var callerSegs []string
		if !abs {
			if c := byID[rc.CallerID]; c != nil {
				callerSegs = rubySegments(c.Label)
			}
		}

		target := ""

		for i := len(callerSegs); i >= 0; i-- {
			path := append(append([]string{}, callerSegs[:i]...), ref...)
			ids := fqLabel[strings.Join(path, "::")]

			if len(ids) == 1 {
				target = ids[0]

				break
			}

			if len(ids) > 1 {
				break
			}
		}

		if target == "" && len(ref) == 1 && !abs {
			if ids := lastSeg[ref[0]]; len(ids) == 1 {
				target = ids[0]
			}
		}

		if target != "" {
			emit(rc.CallerID, target, "mixes_in", "mixin", rc)
		}
	}

	if contextComplete {
		for _, rc := range calls {
			if rc.Language == "mixin" || rc.IsMemberCall || rc.CallerID == "" || rc.Callee == "" {
				continue
			}

			caller := byID[rc.CallerID]
			kind := rubyMethodKindOf(caller)
			owners := methodOwners[rc.CallerID]

			if caller == nil || kind == "" || len(owners) != 1 || unsafeFiles[caller.SourceFile] {
				continue
			}

			var owner string
			for o := range owners {
				owner = o
			}

			target := inheritedMethod(owner, rc.Callee, kind)
			if target == "" || target == rc.CallerID {
				continue
			}

			var matches []*model.Edge

			for _, e := range edges {
				if e.Source == rc.CallerID && e.Target == target && e.Relation == "calls" && e.Context == "call" &&
					e.Confidence == model.Inferred && e.SourceFile == rc.SourceFile &&
					e.SourceLocation == rc.SourceLocation && rubyMethodName(byID[target].Label) == rc.Callee {
					matches = append(matches, e)
				}
			}

			if len(matches) == 1 {
				matches[0].Confidence = model.Extracted
				matches[0].ConfidenceScore = model.Score(1.0)
			}
		}
	}

	for _, rc := range calls {
		if rc.Language == "mixin" || !rc.IsMemberCall || rc.CallerID == "" || rc.Callee == "" {
			continue
		}

		if r := strings.TrimLeft(rc.Receiver, ":"); r != "" && upperStart(r) {
			var cls string
			if strings.Contains(rc.Receiver, "::") {
				cls = byConstPath(rc.Receiver)
			} else {
				cls = uniqueClass(rc.Receiver)
			}

			if cls == "" {
				continue
			}

			if rc.Callee == "new" {
				emit(rc.CallerID, cls, "calls", "call", rc)
			} else if m, ok := methods[[2]string{cls, rc.Callee}]; ok {
				emit(rc.CallerID, m, "calls", "call", rc)
			} else {
				emit(rc.CallerID, cls, "calls", "call", rc)
			}

			continue
		}

		if rc.ReceiverType == "" {
			continue
		}

		cls := uniqueClass(rc.ReceiverType)
		if cls == "" {
			continue
		}

		m, ok := methods[[2]string{cls, rc.Callee}]
		if !ok && contextComplete && len(unsafeFiles) == 0 && len(externalOwners) == 0 {
			m = inheritedMethod(cls, rc.Callee, "instance")
			ok = m != ""
		}

		if ok {
			emit(rc.CallerID, m, "calls", "call", rc)
		}
	}

	*edgesP = edges
}

func uniqSorted(v []string) []string {
	out := v[:0]

	for i, s := range v {
		if i == 0 || s != v[i-1] {
			out = append(out, s)
		}
	}

	return out
}

func rubyMethodKindOf(n *model.Node) string {
	if n == nil {
		return ""
	}

	if k, _ := n.Metadata["ruby_method_kind"].(string); k == "instance" || k == "singleton" {
		return k
	}

	return ""
}

func rubyLookupUnsafe(n *model.Node) bool {
	if n == nil {
		return false
	}

	a, _ := n.Metadata["ruby_lookup_unsafe"].(bool)
	b, _ := n.Metadata["ruby_reopened"].(bool)

	return a || b
}

func rubySplitConst(s string) []string {
	var out []string

	for _, p := range strings.Split(s, "::") {
		if p != "" {
			out = append(out, p)
		}
	}

	return out
}

func lastSegOf(s, sep string) string {
	if i := strings.LastIndex(s, sep); i >= 0 {
		return s[i+len(sep):]
	}

	return s
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// isFileNodeLabel mirrors graph.IsFileNodeLabel for the resolver.
func isFileNodeLabel(label, sf string) bool {
	if label == "" || sf == "" {
		return false
	}

	sf = strings.ReplaceAll(sf, `\`, "/")
	if label == baseName(sf) {
		return true
	}

	return strings.Contains(label, "/") && (sf == label || strings.HasSuffix(sf, "/"+label))
}
