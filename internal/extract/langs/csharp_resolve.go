package langs

import (
	"html"
	"sort"
	"strings"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

var dotnetSourceExts = []string{".cs", ".razor", ".cshtml"}

func isDotnetSource(sf string) bool {
	for _, e := range dotnetSourceExts {
		if strings.HasSuffix(sf, e) {
			return true
		}
	}

	return false
}

func mdString(md map[string]any, k string) string {
	s, _ := md[k].(string)

	return s
}

func nodeNamespace(n *model.Node) string {
	if n == nil {
		return ""
	}

	return mdString(n.Metadata, "namespace")
}

func nodeScopeChain(n *model.Node) []string {
	if n == nil {
		return nil
	}

	switch v := n.Metadata["scope_chain"].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}

		return out
	}

	return nil
}

func sortBySourceKey(ns []*model.Node) {
	sort.SliceStable(ns, func(i, j int) bool {
		a, b := ns[i], ns[j]
		if a.SourceFile != b.SourceFile {
			return a.SourceFile < b.SourceFile
		}

		if a.SourceLocation != b.SourceLocation {
			return a.SourceLocation < b.SourceLocation
		}

		return a.ID < b.ID
	})
}

// csTypeDefIndex maps (namespace, name) -> the deterministic C# type
// definition node id.
//
// Adapted from Graphify's _build_csharp_type_def_index (Apache-2.0).
func csTypeDefIndex(nodes []*model.Node, edges []*model.Edge) map[[2]string]string {
	nonType := map[string]bool{}

	for _, e := range edges {
		switch e.Relation {
		case "case_of", "defines", "method":
			nonType[e.Target] = true
		}
	}

	cands := map[[2]string][]*model.Node{}

	for _, n := range nodes {
		if n.Type == "namespace" || n.ID == "" || n.Label == "" {
			continue
		}

		if b, _ := n.Metadata["is_nested_type"].(bool); b {
			continue
		}

		if nonType[n.ID] || !strings.HasSuffix(n.SourceFile, ".cs") || n.FileType != model.FileTypeCode {
			continue
		}

		if strings.HasSuffix(n.Label, ")") || strings.HasPrefix(n.Label, ".") || strings.Contains(n.Label, ".") {
			continue
		}

		k := [2]string{nodeNamespace(n), n.Label}
		cands[k] = append(cands[k], n)
	}

	out := make(map[[2]string]string, len(cands))

	for k, ns := range cands {
		sortBySourceKey(ns)
		out[k] = ns[0].ID
	}

	return out
}

func stripTrailingCSGenericArgs(fqn string) string {
	fqn = strings.TrimSpace(fqn)
	if !strings.HasSuffix(fqn, ">") {
		return fqn
	}

	depth := 0

	for i := len(fqn) - 1; i >= 0; i-- {
		switch fqn[i] {
		case '>':
			depth++
		case '<':
			depth--
			if depth == 0 {
				return strings.TrimSpace(fqn[:i])
			}
		}
	}

	return fqn
}

type csUsing struct {
	target, scopeKind, scopeID string
}

// csNameResolver is the namespace/using/alias-aware C# simple-name resolver.
//
// Adapted from Graphify's CsharpNameResolver (Apache-2.0).
type csNameResolver struct {
	byID       map[string]*model.Node
	typeDefs   map[[2]string]string
	namespaces map[string]bool
	usings     map[string][]csUsing
	aliases    map[string]map[string][]csUsing
}

func newCSNameResolver(nodes []*model.Node, edges []*model.Edge) *csNameResolver {
	r := &csNameResolver{
		byID:       make(map[string]*model.Node, len(nodes)),
		typeDefs:   csTypeDefIndex(nodes, edges),
		namespaces: map[string]bool{},
		usings:     map[string][]csUsing{},
		aliases:    map[string]map[string][]csUsing{},
	}

	for _, n := range nodes {
		if n.ID != "" {
			r.byID[n.ID] = n
		}

		if n.Type == "namespace" {
			r.namespaces[n.Label] = true
		}
	}

	for _, e := range edges {
		if e.Relation != "imports" {
			continue
		}

		src := r.byID[e.Source]
		if src == nil || !isDotnetSource(src.Label) || !isDotnetSource(src.SourceFile) {
			continue
		}

		target := mdString(e.Metadata, "target_fqn")
		if target == "" {
			continue
		}

		kind := mdString(e.Metadata, "scope_kind")
		if kind == "" {
			kind = "file"
		}

		u := csUsing{target, kind, mdString(e.Metadata, "scope_id")}
		sf := src.SourceFile

		switch mdString(e.Metadata, "using_kind") {
		case "namespace":
			if !containsUsing(r.usings[sf], u) {
				r.usings[sf] = append(r.usings[sf], u)
			}
		case "alias":
			alias := mdString(e.Metadata, "alias")
			if alias == "" {
				continue
			}

			if r.aliases[sf] == nil {
				r.aliases[sf] = map[string][]csUsing{}
			}

			if !containsUsing(r.aliases[sf][alias], u) {
				r.aliases[sf][alias] = append(r.aliases[sf][alias], u)
			}
		}
	}

	return r
}

func containsUsing(xs []csUsing, u csUsing) bool {
	for _, x := range xs {
		if x == u {
			return true
		}
	}

	return false
}

func (r *csNameResolver) inScope(u csUsing, src *model.Node) bool {
	if u.scopeKind == "file" {
		return true
	}

	if u.scopeID == "" {
		return false
	}

	for _, s := range nodeScopeChain(src) {
		if s == u.scopeID {
			return true
		}
	}

	return false
}

func (r *csNameResolver) scopesFor(src *model.Node, sf string) []string {
	var out []string

	add := func(s string) {
		for _, x := range out {
			if x == s {
				return
			}
		}

		out = append(out, s)
	}

	add(nodeNamespace(src))
	add("")

	for _, u := range r.usings[sf] {
		if r.inScope(u, src) {
			add(u.target)
		}
	}

	return out
}

func (r *csNameResolver) resolveAlias(label string, src *model.Node, sf string) string {
	hits := map[string]bool{}

	var hit string

	for _, u := range r.aliases[sf][label] {
		if !r.inScope(u, src) {
			continue
		}

		fqn := stripTrailingCSGenericArgs(html.UnescapeString(u.target))

		ns, simple := "", fqn
		if i := strings.LastIndex(fqn, "."); i >= 0 {
			ns, simple = fqn[:i], fqn[i+1:]
		}

		if simple == "" {
			continue
		}

		if h, ok := r.typeDefs[[2]string{ns, simple}]; ok {
			hits[h] = true
			hit = h
		}
	}

	if len(hits) == 1 {
		return hit
	}

	return ""
}

// resolveTypeName returns (id, decisive); see Graphify's
// CsharpNameResolver.resolve_type_name.
func (r *csNameResolver) resolveTypeName(label string, src *model.Node, sf string) (string, bool) {
	if _, ok := r.aliases[sf][label]; ok {
		return r.resolveAlias(label, src, sf), true
	}

	var cands []string

	for _, ns := range r.scopesFor(src, sf) {
		if h, ok := r.typeDefs[[2]string{ns, label}]; ok && !containsString(cands, h) {
			cands = append(cands, h)
		}
	}

	if len(cands) == 1 {
		return cands[0], true
	}

	return "", len(cands) > 0
}

func (r *csNameResolver) resolveQualified(label, qualifier string, src *model.Node, sf string) string {
	if qualifier == "" {
		return ""
	}

	var inScope []csUsing

	for _, u := range r.aliases[sf][qualifier] {
		if r.inScope(u, src) {
			inScope = append(inScope, u)
		}
	}

	if len(inScope) > 0 {
		hits := map[string]bool{}

		var hit string

		for _, u := range inScope {
			ns := stripTrailingCSGenericArgs(html.UnescapeString(u.target))
			if h, ok := r.typeDefs[[2]string{ns, label}]; ok {
				hits[h] = true
				hit = h
			}
		}

		if len(hits) == 1 {
			return hit
		}

		return ""
	}

	if r.namespaces[qualifier] {
		return r.typeDefs[[2]string{qualifier, label}]
	}

	return ""
}

func dropUnreferenced(nodes []*model.Node, edges []*model.Edge, from map[string]bool) []*model.Node {
	if len(from) == 0 {
		return nodes
	}

	ref := map[string]bool{}
	for _, e := range edges {
		ref[e.Source] = true
		ref[e.Target] = true
	}

	kept := nodes[:0]

	for _, n := range nodes {
		if !from[n.ID] || ref[n.ID] {
			kept = append(kept, n)
		}
	}

	return kept
}

// resolveCSharpTypeReferences arbitrates C# inherits/implements/references
// targets with namespace/using/alias scoping, leaving unresolved ones on a
// dangling stub.
//
// Adapted from Graphify's _resolve_csharp_type_references (Apache-2.0).
func resolveCSharpTypeReferences(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	if !anyFileSuffix(per, dotnetSourceExts...) {
		return
	}

	nodes, edges := *nodesP, *edgesP
	r := newCSNameResolver(nodes, edges)

	placeholder := func(n *model.Node) bool { return n != nil && n.SourceFile == "" }

	labelFor := func(t *model.Node, sf string) string {
		if t.Label == "" {
			return ""
		}

		if !strings.HasSuffix(t.Label, ".cs") {
			return t.Label
		}

		stem := strings.TrimSuffix(t.Label, ".cs")
		for alias := range r.aliases[sf] {
			if strings.EqualFold(alias, stem) || ids.MakeID(alias) == ids.MakeID(stem) {
				return alias
			}
		}

		return stem
	}

	dangling := func(label, current string) string {
		if cur := r.byID[current]; placeholder(cur) && cur.Label == label {
			return current
		}

		for _, n := range nodes {
			if n.ID != "" && n.Label == label && placeholder(n) {
				return n.ID
			}
		}

		id := ids.MakeID(label)
		if _, ok := r.byID[id]; ok {
			id = ids.MakeID("csharp_type_ref", label)

			for i := 2; ; i++ {
				if _, ok := r.byID[id]; !ok {
					break
				}

				id = ids.MakeID("csharp_type_ref", label, itoa(i))
			}
		}

		n := &model.Node{ID: id, Label: label, FileType: model.FileTypeCode}
		nodes = append(nodes, n)
		r.byID[id] = n

		return id
	}

	repointed := map[string]bool{}

	for _, e := range edges {
		if e.Relation != "implements" && e.Relation != "inherits" && e.Relation != "references" {
			continue
		}

		if !isDotnetSource(e.SourceFile) {
			continue
		}

		src, tgt := r.byID[e.Source], r.byID[e.Target]
		if src == nil || tgt == nil {
			continue
		}

		if tgt.Type != "namespace" && tgt.SourceFile != "" && !strings.HasSuffix(tgt.SourceFile, ".cs") {
			continue
		}

		label := mdString(e.Metadata, "ref_token")
		if label == "" {
			label = labelFor(tgt, e.SourceFile)
		}

		if label == "" {
			continue
		}

		var resolved string
		if q, _ := e.Metadata["qualified"].(bool); q {
			resolved = r.resolveQualified(label, mdString(e.Metadata, "ref_qualifier"), src, e.SourceFile)
		} else {
			resolved, _ = r.resolveTypeName(label, src, e.SourceFile)
		}

		desired := resolved
		if desired == "" {
			desired = dangling(label, e.Target)
		}

		if desired != e.Target {
			if placeholder(tgt) {
				repointed[e.Target] = true
			}

			e.Target = desired
		}
	}

	*nodesP, *edgesP = dropUnreferenced(nodes, edges, repointed), edges
}

// resolveCSharpImports re-points `using` edges to canonical namespace nodes
// (namespace usings) or type definitions (alias usings).
//
// Adapted from Graphify's _resolve_cross_file_csharp_imports (Apache-2.0).
func resolveCSharpImports(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	if !anyFileSuffix(per, dotnetSourceExts...) {
		return
	}

	nodes, edges := *nodesP, *edgesP

	var nsNodes []*model.Node

	for _, n := range nodes {
		if n.Type == "namespace" && n.Label != "" && n.ID != "" {
			nsNodes = append(nsNodes, n)
		}
	}

	sortBySourceKey(nsNodes)

	nsByLabel := map[string]string{}
	for _, n := range nsNodes {
		if _, ok := nsByLabel[n.Label]; !ok {
			nsByLabel[n.Label] = n.ID
		}
	}

	typeDefs := csTypeDefIndex(nodes, edges)
	if len(nsByLabel) == 0 && len(typeDefs) == 0 {
		return
	}

	repointed := map[string]bool{}

	for _, e := range edges {
		if e.Relation != "imports" {
			continue
		}

		kind, fqn := mdString(e.Metadata, "using_kind"), mdString(e.Metadata, "target_fqn")
		if kind == "" || fqn == "" {
			continue
		}

		var resolved string

		switch kind {
		case "namespace":
			resolved = nsByLabel[fqn]
		case "alias":
			b := stripTrailingCSGenericArgs(html.UnescapeString(fqn))
			if i := strings.LastIndex(b, "."); i >= 0 {
				if _, ok := nsByLabel[b[:i]]; ok {
					resolved = typeDefs[[2]string{b[:i], b[i+1:]}]
				}
			}
		}

		if resolved != "" && resolved != e.Target {
			if e.Target != "" {
				repointed[e.Target] = true
			}

			e.Target = resolved
		}
	}

	*nodesP = dropUnreferenced(nodes, edges, repointed)
}

func anyFileSuffix(per []extract.FileResult, exts ...string) bool {
	for _, fr := range per {
		for _, e := range exts {
			if strings.HasSuffix(strings.ToLower(fr.Path), e) {
				return true
			}
		}
	}

	return false
}

// resolveCSharpMemberCalls binds C# member calls to the receiver's declared
// type (this/base/Type/typed receiver), walking the inherits chain.
//
// Adapted from Graphify's _resolve_csharp_member_calls (Apache-2.0).
func resolveCSharpMemberCalls(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	nodes, edges := *nodesP, *edgesP

	contained := map[string]bool{}
	for _, e := range edges {
		if e.Relation == "contains" {
			contained[e.Target] = true
		}
	}

	byID := make(map[string]*model.Node, len(nodes))
	typeDefs := map[string][]string{}

	for _, n := range nodes {
		byID[n.ID] = n
		if n.SourceFile != "" && contained[n.ID] && isTypeLikeDef(n) {
			k := memberKey(n.Label)
			typeDefs[k] = append(typeDefs[k], n.ID)
		}
	}

	var resolver *csNameResolver

	methods := map[[2]string]string{}
	enclosing := map[string]string{}

	for _, e := range edges {
		if e.Relation != "method" {
			continue
		}

		t := byID[e.Target]
		if t == nil {
			continue
		}

		if _, ok := enclosing[e.Target]; !ok {
			enclosing[e.Target] = e.Source
		}

		methods[[2]string{e.Source, memberKey(t.Label)}] = e.Target
	}

	bases := map[string][]string{}
	unresolved := map[string]bool{}

	for _, e := range edges {
		if e.Relation != "inherits" || !strings.HasSuffix(e.SourceFile, ".cs") {
			continue
		}

		if t := byID[e.Target]; t == nil || t.SourceFile == "" {
			unresolved[e.Source] = true
		} else if !containsString(bases[e.Source], e.Target) {
			bases[e.Source] = append(bases[e.Source], e.Target)
		}
	}

	methodOn := func(typeID, key string) string {
		hits := map[string]bool{}
		seen := map[string]bool{}
		frontier := []string{typeID}

		var hit string

		for len(frontier) > 0 {
			id := frontier[len(frontier)-1]
			frontier = frontier[:len(frontier)-1]

			if seen[id] {
				continue
			}

			seen[id] = true

			if m, ok := methods[[2]string{id, key}]; ok {
				hits[m] = true
				hit = m

				continue
			}

			if unresolved[id] {
				return ""
			}

			frontier = append(frontier, bases[id]...)
		}

		if len(hits) == 1 {
			return hit
		}

		return ""
	}

	resolveType := func(name string, caller *model.Node, sf string) string {
		if name == "" {
			return ""
		}

		if caller != nil {
			if resolver == nil {
				resolver = newCSNameResolver(nodes, edges)
			}

			if id, decisive := resolver.resolveTypeName(name, caller, sf); id != "" {
				return id
			} else if decisive {
				return ""
			}
		}

		if d := typeDefs[memberKey(name)]; len(d) == 1 {
			return d[0]
		}

		return ""
	}

	// Only an absent type is a cross-repo candidate; ambiguous or scoped-out
	// names stay unparked.
	parkIfAbsent := func(name string, caller *model.Node, rc *model.RawCall) {
		if name != "" && len(typeDefs[memberKey(name)]) == 0 {
			parkUnresolvedMemberCall(caller, rc.Callee, name, "csharp", rc)
		}
	}

	existing := map[[2]string]bool{}
	for _, e := range edges {
		existing[[2]string{e.Source, e.Target}] = true
	}

	for _, fr := range per {
		for _, rc := range fr.Ex.RawCalls {
			if rc.Language != "csharp" || !rc.IsMemberCall || rc.Receiver == "" || rc.Callee == "" || rc.CallerID == "" {
				continue
			}

			caller := byID[rc.CallerID]

			var (
				typeID    string
				qualified bool
			)

			switch {
			case rc.Receiver == "this":
				typeID, qualified = enclosing[rc.CallerID], true
			case rc.Receiver == "base":
				enc := enclosing[rc.CallerID]
				if enc == "" || unresolved[enc] || len(bases[enc]) != 1 {
					continue
				}

				typeID, qualified = bases[enc][0], true
			case upperStart(rc.Receiver):
				typeID = resolveType(rc.Receiver, caller, rc.SourceFile)
				if typeID == "" {
					typeID = resolveType(rc.ReceiverType, caller, rc.SourceFile)
					if typeID == "" {
						name := rc.ReceiverType
						if name == "" {
							name = rc.Receiver
						}

						parkIfAbsent(name, caller, rc)
					}
				}

				qualified = true
			default:
				typeID = resolveType(rc.ReceiverType, caller, rc.SourceFile)
				if typeID == "" {
					parkIfAbsent(rc.ReceiverType, caller, rc)
				}
			}

			if typeID == "" {
				continue
			}

			target := methodOn(typeID, memberKey(rc.Callee))
			if target == "" || target == rc.CallerID || existing[[2]string{rc.CallerID, target}] {
				continue
			}

			existing[[2]string{rc.CallerID, target}] = true

			conf, score := model.Inferred, 0.8
			if qualified {
				conf, score = model.Extracted, 1.0
			}

			edges = append(edges, &model.Edge{
				Source: rc.CallerID, Target: target, Relation: "calls", Context: "call",
				Confidence: conf, ConfidenceScore: model.Score(score),
				SourceFile: rc.SourceFile, SourceLocation: rc.SourceLocation, Weight: 1,
			})
		}
	}

	*edgesP = edges
}

// resolveCSharpQualifiedCalls binds `new A.B.Cache()` to the Cache declared
// in namespace A.B.
//
// Adapted from Graphify's _resolve_csharp_qualified_calls (Apache-2.0).
func resolveCSharpQualifiedCalls(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	var raw []*model.RawCall

	for _, fr := range per {
		for _, rc := range fr.Ex.RawCalls {
			if rc.Language == "csharp" && rc.QualifiedPrefix != "" && rc.Callee != "" && rc.CallerID != "" {
				raw = append(raw, rc)
			}
		}
	}

	if len(raw) == 0 {
		return
	}

	byNS := map[[2]string][]string{}

	for _, n := range *nodesP {
		if !n.CallableClass || n.SourceFile == "" {
			continue
		}

		ns, label := nodeNamespace(n), strings.Trim(n.Label, "()")
		if ns != "" && label != "" {
			k := [2]string{ns, label}
			byNS[k] = append(byNS[k], n.ID)
		}
	}

	if len(byNS) == 0 {
		return
	}

	edges := *edgesP

	existing := map[[2]string]bool{}
	for _, e := range edges {
		if e.Relation == "calls" {
			existing[[2]string{e.Source, e.Target}] = true
		}
	}

	for _, rc := range raw {
		c := byNS[[2]string{rc.QualifiedPrefix, rc.Callee}]
		if len(c) != 1 {
			continue
		}

		tgt := c[0]
		if tgt == rc.CallerID || existing[[2]string{rc.CallerID, tgt}] {
			continue
		}

		existing[[2]string{rc.CallerID, tgt}] = true
		edges = append(edges, &model.Edge{
			Source: rc.CallerID, Target: tgt, Relation: "calls", Context: "call",
			Confidence: model.Extracted, ConfidenceScore: model.Score(1.0),
			SourceFile: rc.SourceFile, SourceLocation: rc.SourceLocation, Weight: 1,
		})
	}

	*edgesP = edges
}
