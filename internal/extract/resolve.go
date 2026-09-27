package extract

import (
	"crypto/sha1" //nolint:gosec // non-cryptographic salt, mirrors Graphify
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/golang"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

type corpus struct {
	root     string
	per      []fileResult
	nodes    []*model.Node
	edges    []*model.Edge
	rawCalls []*model.RawCall
}

func resolve(root string, per []fileResult, res *Result) {
	c := &corpus{root: root, per: per}

	for _, fr := range per {
		if fr.ex == nil {
			continue
		}

		c.nodes = append(c.nodes, fr.ex.Nodes...)
		c.edges = append(c.edges, fr.ex.Edges...)
		c.rawCalls = append(c.rawCalls, fr.ex.RawCalls...)
	}

	c.canonicalizeFileIDs()
	c.disambiguateCollidingIDs()
	c.canonicalizeCSharpNamespaces()
	c.resolveGoTypeReferences()
	c.rewireUniqueStubs()
	c.resolveCalls()
	c.repointGoImports()
	c.runLanguageResolvers()
	c.relativize()

	res.Nodes = c.nodes
	res.Edges = c.edges
}

// canonicalizeFileIDs rewrites absolute-path-derived file ids and symbol
// prefixes to the canonical root-relative form.
func (c *corpus) canonicalizeFileIDs() {
	idRemap := map[string]string{}
	prefixRemap := map[string][2]string{}

	for _, fr := range c.per {
		if fr.ex == nil {
			continue
		}

		rel := relPath(c.root, fr.path)
		if rel == "" {
			continue
		}

		newID := base.FileNodeID(rel)
		if old := ids.MakeID(fr.path); old != newID {
			idRemap[old] = newID
		}

		if oldPref := base.FileNodeID(fr.path); oldPref != newID {
			prefixRemap[fr.path] = [2]string{oldPref, newID}
		}
	}

	symRemap := map[string]string{}

	for _, n := range c.nodes {
		if nid, ok := idRemap[n.ID]; ok {
			n.ID = nid

			continue
		}

		if n.SourceFile == "" || n.Type == "package" {
			continue
		}

		pr, ok := prefixRemap[n.SourceFile]
		if !ok {
			continue
		}

		if strings.HasPrefix(n.ID, pr[0]+"_") {
			nid := pr[1] + n.ID[len(pr[0]):]
			symRemap[n.ID] = nid
			n.ID = nid
		}
	}

	remap := func(s string) string {
		if v, ok := idRemap[s]; ok {
			return v
		}

		if v, ok := symRemap[s]; ok {
			return v
		}

		return s
	}

	for _, e := range c.edges {
		e.Source = remap(e.Source)
		e.Target = remap(e.Target)
	}

	for _, rc := range c.rawCalls {
		rc.CallerID = remap(rc.CallerID)
	}
}

func (c *corpus) sourceKey(sf string) string {
	if sf == "" {
		return ""
	}

	if rel := relPath(c.root, sf); rel != "" {
		return rel
	}

	return sf
}

func (c *corpus) nodeSourceKey(n *model.Node) string {
	if n.SourceFile != "" {
		return c.sourceKey(n.SourceFile)
	}

	return c.sourceKey(n.OriginFile)
}

var headerSuffixes = base.NewSet(".h", ".hpp", ".hh", ".hxx")

// disambiguateCollidingIDs salts ids shared by nodes from different files
// with the source path.
func (c *corpus) disambiguateCollidingIDs() {
	type group struct {
		nodes []*model.Node
	}

	byID := map[string]*group{}
	order := []string{}

	for _, n := range c.nodes {
		if n.Type == "module" || n.Type == "namespace" || n.ID == "" {
			continue
		}

		g := byID[n.ID]
		if g == nil {
			g = &group{}
			byID[n.ID] = g
			order = append(order, n.ID)
		}

		g.nodes = append(g.nodes, n)
	}

	type key struct{ id, sk string }

	remap := map[key]string{}
	ambiguous := map[string]bool{}

	for _, oldID := range order {
		g := byID[oldID]

		keys := map[string]bool{}
		for _, n := range g.nodes {
			keys[c.nodeSourceKey(n)] = true
		}

		if len(g.nodes) < 2 || len(keys) < 2 {
			continue
		}

		ambiguous[oldID] = true

		naive := map[string]string{}
		for sk := range keys {
			if sk != "" {
				naive[sk] = ids.MakeID(sk, oldID)
			}
		}

		seen := map[string]int{}
		for _, v := range naive {
			seen[v]++
		}

		for _, n := range g.nodes {
			sk := c.nodeSourceKey(n)
			if sk == "" {
				continue
			}

			var newID string

			if seen[naive[sk]] > 1 {
				sum := sha1.Sum([]byte(sk)) //nolint:gosec // id salt
				newID = ids.MakeID(sk, oldID, hex.EncodeToString(sum[:])[:6])
			} else {
				newID = naive[sk]
			}

			remap[key{oldID, sk}] = newID
			n.ID = newID
		}
	}

	if len(remap) == 0 {
		return
	}

	headerRemaps := map[string]string{}

	for oldID := range ambiguous {
		for _, n := range byID[oldID].nodes {
			sk := c.nodeSourceKey(n)
			if sk != "" && headerSuffixes.Has(strings.ToLower(base.Suffix(sk))) {
				if nid, ok := remap[key{oldID, sk}]; ok {
					headerRemaps[oldID] = nid

					break
				}
			}
		}
	}

	for _, e := range c.edges {
		sk := c.sourceKey(e.SourceFile)
		if nid, ok := remap[key{e.Source, sk}]; ok {
			e.Source = nid
		}

		if (e.Relation == "imports" || e.Relation == "imports_from") && headerRemaps[e.Target] != "" {
			e.Target = headerRemaps[e.Target]
		} else if nid, ok := remap[key{e.Target, sk}]; ok {
			e.Target = nid
		}
	}

	for _, rc := range c.rawCalls {
		if nid, ok := remap[key{rc.CallerID, c.sourceKey(rc.SourceFile)}]; ok {
			rc.CallerID = nid
		}
	}
}

// canonicalizeCSharpNamespaces collapses duplicate namespace nodes (one per
// file) onto a single canonical node per label.
func (c *corpus) canonicalizeCSharpNamespaces() {
	byLabel := map[string][]*model.Node{}

	var order []string

	for _, n := range c.nodes {
		if n.Type != "namespace" {
			continue
		}

		if _, ok := byLabel[n.Label]; !ok {
			order = append(order, n.Label)
		}

		byLabel[n.Label] = append(byLabel[n.Label], n)
	}

	remap := map[string]string{}
	drop := map[*model.Node]bool{}

	for _, l := range order {
		g := byLabel[l]
		if len(g) < 2 {
			continue
		}

		canon := g[0]
		for _, n := range g[1:] {
			if n.SourceFile < canon.SourceFile ||
				(n.SourceFile == canon.SourceFile && n.SourceLocation < canon.SourceLocation) ||
				(n.SourceFile == canon.SourceFile && n.SourceLocation == canon.SourceLocation && n.ID < canon.ID) {
				canon = n
			}
		}

		for _, n := range g {
			if n != canon {
				drop[n] = true
				remap[n.ID] = canon.ID
			}
		}
	}

	if len(drop) == 0 {
		return
	}

	for _, e := range c.edges {
		if v, ok := remap[e.Source]; ok {
			e.Source = v
		}

		if v, ok := remap[e.Target]; ok {
			e.Target = v
		}
	}

	kept := c.nodes[:0]

	for _, n := range c.nodes {
		if !drop[n] {
			kept = append(kept, n)
		}
	}

	c.nodes = kept
}

func isTypeLikeDefinition(n *model.Node) bool {
	if n.Type == "namespace" {
		return false
	}

	label := strings.TrimSpace(n.Label)
	if label == "" || strings.HasSuffix(label, ")") || strings.HasPrefix(label, ".") || strings.Contains(label, ".") {
		return false
	}

	return n.FileType == model.FileTypeCode
}

func isTopLevelFunctionDefinition(n *model.Node) bool {
	label := strings.TrimSpace(n.Label)

	return n.FileType == model.FileTypeCode && strings.HasSuffix(label, ")") &&
		!strings.HasPrefix(label, ".") && !strings.Contains(label, ".")
}

var nonAlnumASCII = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func labelKey(n *model.Node, fold bool) string {
	k := nonAlnumASCII.ReplaceAllString(strings.TrimSpace(n.Label), "")
	if fold {
		return strings.ToLower(k)
	}

	return k
}

var supertypeRelations = base.NewSet("inherits", "implements", "extends")

// rewireUniqueStubs maps sourceless stubs to a unique same-label definition.
func (c *corpus) rewireUniqueStubs() {
	realByLabel := map[string][]*model.Node{}
	realByLabelCI := map[string][]*model.Node{}
	funcByLabel := map[string][]*model.Node{}

	var stubs []*model.Node

	for _, n := range c.nodes {
		k := labelKey(n, false)
		if k == "" {
			continue
		}

		if n.SourceFile != "" {
			switch {
			case isTypeLikeDefinition(n):
				realByLabel[k] = append(realByLabel[k], n)
				if base.LangIsCaseInsensitive(n.SourceFile) {
					fk := labelKey(n, true)
					realByLabelCI[fk] = append(realByLabelCI[fk], n)
				}
			case isTopLevelFunctionDefinition(n):
				funcByLabel[k] = append(funcByLabel[k], n)
			}

			continue
		}

		stubs = append(stubs, n)
	}

	stubIDs := map[string]bool{}
	for _, s := range stubs {
		stubIDs[s.ID] = true
	}

	stubFamilies := map[string]map[string]bool{}
	supertypeStubs := map[string]bool{}

	for _, e := range c.edges {
		for i, nid := range []string{e.Source, e.Target} {
			if !stubIDs[nid] {
				continue
			}

			if fam := base.LangFamily(e.SourceFile); fam != "" {
				if stubFamilies[nid] == nil {
					stubFamilies[nid] = map[string]bool{}
				}

				stubFamilies[nid][fam] = true
			}

			if i == 1 && supertypeRelations.Has(e.Relation) {
				supertypeStubs[nid] = true
			}
		}
	}

	remap := map[string]string{}

	for _, s := range stubs {
		if s.ID == "" {
			continue
		}

		cands := realByLabel[labelKey(s, false)]
		if len(cands) != 1 {
			cands = realByLabelCI[labelKey(s, true)]
		}

		if len(cands) != 1 {
			f := funcByLabel[labelKey(s, false)]
			if len(f) == 1 && !supertypeStubs[s.ID] {
				fams := stubFamilies[s.ID]
				cf := base.LangFamily(f[0].SourceFile)

				if len(fams) == 0 || cf == "" || fams[cf] {
					cands = f
				}
			}
		}

		if len(cands) != 1 {
			continue
		}

		if t := cands[0].ID; t != "" && t != s.ID {
			remap[s.ID] = t
		}
	}

	if len(remap) == 0 {
		return
	}

	byID := map[string]*model.Node{}
	for _, n := range c.nodes {
		byID[n.ID] = n
	}

	csharpScoped := base.NewSet("inherits", "implements", "references", "imports")
	isCS := func(sf string) bool {
		return strings.HasSuffix(sf, ".cs") || strings.HasSuffix(sf, ".razor") || strings.HasSuffix(sf, ".cshtml")
	}

	namesOwnBuiltinBase := func(e *model.Edge, stubID, remapped string) bool {
		if !supertypeRelations.Has(e.Relation) {
			return false
		}

		fam := base.LangFamily(e.SourceFile)
		if fam == "" {
			return false
		}

		label := ""
		if n := byID[stubID]; n != nil {
			label = strings.TrimSpace(n.Label)
		}

		bs := builtinBaseClasses[fam]
		if base.LangIsCaseInsensitive(e.SourceFile) {
			label = strings.ToLower(label)
			bs = lowerSet(bs)
		}

		if !bs.Has(label) {
			return false
		}

		tf := ""
		if n := byID[remapped]; n != nil {
			tf = base.LangFamily(n.SourceFile)
		}

		return tf != "" && tf != fam
	}

	for _, e := range c.edges {
		scoped := isCS(e.SourceFile) && csharpScoped.Has(e.Relation)

		if r, ok := remap[e.Source]; ok {
			if !(scoped && byID[r] != nil && strings.HasSuffix(byID[r].SourceFile, ".cs")) {
				e.Source = r
			}
		}

		if r, ok := remap[e.Target]; ok {
			if !(scoped && byID[r] != nil && strings.HasSuffix(byID[r].SourceFile, ".cs")) &&
				!namesOwnBuiltinBase(e, e.Target, r) {
				e.Target = r
			}
		}
	}

	referenced := map[string]bool{}
	for _, e := range c.edges {
		referenced[e.Source] = true
		referenced[e.Target] = true
	}

	kept := c.nodes[:0]

	for _, n := range c.nodes {
		if _, ok := remap[n.ID]; ok && !referenced[n.ID] {
			continue
		}

		kept = append(kept, n)
	}

	c.nodes = kept
}

func lowerSet(s base.Set) base.Set {
	out := make(base.Set, len(s))
	for k := range s {
		out[strings.ToLower(k)] = struct{}{}
	}

	return out
}

var builtinBaseClasses = map[string]base.Set{
	"php": base.NewSet("Throwable", "Exception", "ErrorException", "Error", "TypeError",
		"ValueError", "ArgumentCountError", "ArithmeticError",
		"DivisionByZeroError", "RuntimeException", "LogicException",
		"InvalidArgumentException", "DomainException", "LengthException",
		"OutOfRangeException", "OutOfBoundsException", "RangeException",
		"OverflowException", "UnderflowException", "UnexpectedValueException",
		"BadFunctionCallException", "BadMethodCallException", "JsonException"),
	"jvm": base.NewSet("Throwable", "Exception", "RuntimeException", "Error",
		"IllegalArgumentException", "IllegalStateException",
		"UnsupportedOperationException", "IndexOutOfBoundsException",
		"NullPointerException", "IOException"),
	"python": base.NewSet("BaseException", "Exception", "ValueError", "TypeError", "KeyError",
		"IndexError", "RuntimeError", "NotImplementedError", "AttributeError",
		"OSError", "IOError", "StopIteration", "Warning", "UserWarning",
		"DeprecationWarning"),
	"jsts": base.NewSet("Error", "TypeError", "RangeError", "SyntaxError", "ReferenceError",
		"EvalError", "URIError", "AggregateError"),
	"dotnet": base.NewSet("Exception", "ApplicationException", "SystemException",
		"ArgumentException", "ArgumentNullException",
		"ArgumentOutOfRangeException", "InvalidOperationException",
		"NotImplementedException", "NotSupportedException"),
	"ruby": base.NewSet("Exception", "StandardError", "RuntimeError", "ArgumentError",
		"TypeError", "NameError", "NoMethodError", "IOError"),
}

var jsTSCallSuffixes = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}

// resolveCalls binds raw_calls to definitions in other files.
func (c *corpus) resolveCalls() {
	global := map[string][]string{}
	globalCI := map[string][]string{}
	callable := map[string]bool{}
	classIDs := map[string]bool{}

	for _, n := range c.nodes {
		if n.Callable {
			callable[n.ID] = true
		}

		if n.CallableClass {
			classIDs[n.ID] = true
		}

		if n.FileType == model.FileTypeRationale || n.Type == "namespace" {
			continue
		}

		norm := strings.TrimLeft(strings.Trim(n.Label, "()"), ".")
		if norm == "" {
			continue
		}

		global[norm] = append(global[norm], n.ID)
		if base.LangIsCaseInsensitive(n.SourceFile) {
			globalCI[strings.ToLower(norm)] = append(globalCI[strings.ToLower(norm)], n.ID)
		}
	}

	symImports := map[string]map[string]bool{}
	modImports := map[string]map[string]bool{}

	for _, e := range c.edges {
		switch e.Relation {
		case "imports":
			addSet(symImports, e.Source, e.Target)
		case "imports_from":
			addSet(modImports, e.Source, e.Target)
		}
	}

	sfToFile := map[string]string{}

	for _, n := range c.nodes {
		if n.SourceFile != "" && n.Label == filepath.Base(n.SourceFile) {
			if _, ok := sfToFile[n.SourceFile]; !ok {
				sfToFile[n.SourceFile] = n.ID
			}
		}
	}

	nidToFile := map[string]string{}
	nidToSF := map[string]string{}

	for _, n := range c.nodes {
		if n.SourceFile == "" {
			continue
		}

		nidToSF[n.ID] = n.SourceFile
		if f, ok := sfToFile[n.SourceFile]; ok {
			nidToFile[n.ID] = f
		} else if rel := relPath(c.root, n.SourceFile); rel != "" {
			nidToFile[n.ID] = base.FileNodeID(rel)
		}
	}

	existing := map[[2]string]bool{}
	callLike := map[[2]string]bool{}

	for _, e := range c.edges {
		existing[[2]string{e.Source, e.Target}] = true
		if e.Relation == "calls" || e.Relation == "indirect_call" {
			callLike[[2]string{e.Source, e.Target}] = true
		}
	}

	goMods := newGoModCache()

	for _, rc := range c.rawCalls {
		callee := rc.Callee
		if callee == "" || base.BuiltinGlobals.Has(callee) || rc.IsMemberCall {
			continue
		}

		if rc.Language == "bash" || rc.Language == "markdown" || rc.Language == "mixin" {
			continue
		}

		if rc.Language == "go" && golang.PredeclaredFuncs.Has(callee) {
			continue
		}

		cands := global[callee]
		if len(cands) == 0 && base.LangIsCaseInsensitive(rc.SourceFile) {
			cands = globalCI[strings.ToLower(callee)]
		}

		if len(cands) == 0 {
			continue
		}

		if fam := base.LangFamily(rc.SourceFile); fam != "" {
			var f []string

			for _, cand := range cands {
				cf := base.LangFamily(nidToSF[cand])
				if cf == "" || cf == fam {
					f = append(f, cand)
				}
			}

			cands = f
			if len(cands) == 0 {
				continue
			}
		}

		goExact := false

		if rc.Language == "go" && rc.ImportPath != "" {
			var f []string

			for _, cand := range cands {
				if goMods.importPathForFile(c.root, nidToSF[cand]) == rc.ImportPath {
					f = append(f, cand)
				}
			}

			cands = f
			if len(cands) == 0 {
				continue
			}

			goExact = true
		}

		caller := rc.CallerID

		callerFile := sfToFile[rc.SourceFile]
		if callerFile == "" {
			callerFile = nidToFile[caller]
		}

		imported := symImports[callerFile]
		modules := modImports[callerFile]

		hasEvidence := func(cand string) bool {
			if imported[cand] {
				return true
			}

			cf, ok := nidToFile[cand]

			return ok && modules[cf]
		}

		var (
			tgt      string
			evidence bool
		)

		if len(cands) == 1 {
			tgt = cands[0]
			evidence = goExact || hasEvidence(tgt)
		} else {
			var symM, modM []string

			for _, cand := range cands {
				if imported[cand] {
					symM = append(symM, cand)
				}

				if cf, ok := nidToFile[cand]; ok && modules[cf] {
					modM = append(modM, cand)
				}
			}

			switch {
			case len(symM) == 1:
				tgt, evidence = symM[0], true
			case len(modM) == 1:
				tgt, evidence = modM[0], true
			default:
				files := map[string]string{}
				for _, cand := range cands {
					files[cand] = nidToSF[cand]
				}

				tgt = base.DisambiguateCandidates(cands, files, rc.SourceFile)
				if tgt == "" {
					continue
				}
			}
		}

		if nidToSF[tgt] == "" {
			continue
		}

		pair := [2]string{caller, tgt}

		if rc.Indirect {
			if tgt != caller && !callLike[pair] && callable[tgt] && !classIDs[tgt] {
				callLike[pair] = true
				ctx := rc.Context
				if ctx == "" {
					ctx = "argument"
				}

				c.edges = append(c.edges, &model.Edge{
					Source: caller, Target: tgt, Relation: "indirect_call", Context: ctx,
					Confidence: model.Inferred, ConfidenceScore: model.Score(0.85),
					SourceFile: rc.SourceFile, SourceLocation: rc.SourceLocation, Weight: 1,
				})
			}

			continue
		}

		if !evidence && hasAnySuffix(rc.SourceFile, jsTSCallSuffixes) {
			continue
		}

		if tgt == caller || existing[pair] {
			continue
		}

		existing[pair] = true

		conf, score := model.Inferred, 0.85
		if evidence {
			conf, score = model.Extracted, 1.0
		}

		c.edges = append(c.edges, &model.Edge{
			Source: caller, Target: tgt, Relation: "calls", Context: "call",
			Confidence: conf, ConfidenceScore: model.Score(score),
			SourceFile: rc.SourceFile, SourceLocation: rc.SourceLocation, Weight: 1,
		})
	}
}

// repointGoImports turns intra-module go_pkg_<path> sinks into edges to the
// imported package's file nodes.
func (c *corpus) repointGoImports() {
	goMods := newGoModCache()
	pkgFiles := map[string][]string{}

	var sfs []string

	sfToFile := map[string]string{}

	for _, n := range c.nodes {
		if n.SourceFile != "" && n.Label == filepath.Base(n.SourceFile) {
			if _, ok := sfToFile[n.SourceFile]; !ok {
				sfToFile[n.SourceFile] = n.ID
				sfs = append(sfs, n.SourceFile)
			}
		}
	}

	for _, sf := range sfs {
		if !strings.HasSuffix(sf, ".go") {
			continue
		}

		if ip := goMods.importPathForFile(c.root, sf); ip != "" {
			pkgFiles[ip] = append(pkgFiles[ip], sfToFile[sf])
		}
	}

	if len(pkgFiles) == 0 {
		return
	}

	sinks := map[string]string{}
	for ip := range pkgFiles {
		sinks[ids.MakeID("go", "pkg", ip)] = ip
	}

	existing := map[[2]string]bool{}
	for _, e := range c.edges {
		existing[[2]string{e.Source, e.Target}] = true
	}

	out := make([]*model.Edge, 0, len(c.edges))

	for _, e := range c.edges {
		ip, ok := sinks[e.Target]
		if e.Relation != "imports_from" || !ok {
			out = append(out, e)

			continue
		}

		for _, f := range pkgFiles[ip] {
			if f == e.Source || existing[[2]string{e.Source, f}] {
				continue
			}

			cp := *e
			cp.Target = f
			out = append(out, &cp)
			existing[[2]string{e.Source, f}] = true
		}
	}

	c.edges = out
}

// relativize rewrites source_file values to root-relative, slash paths and
// canonicalizes any id still minted from an absolute path (Graphify's final
// ext_id_remap pass).
func (c *corpus) relativize() {
	owned := map[string]bool{}
	for _, n := range c.nodes {
		owned[n.ID] = true
	}

	type entry struct {
		sf, canonical string
		keys          []string
	}

	cache := map[string]entry{}
	get := func(sf string) entry {
		if e, ok := cache[sf]; ok {
			return e
		}

		var e entry

		if rel := relPath(c.root, sf); rel != "" {
			e.sf, e.canonical = rel, base.FileNodeID(rel)
		} else {
			e.sf = portableOutOfRoot(c.root, sf)
			e.canonical = ids.MakeID("ext", e.sf)
		}

		resolved := sf
		if r, err := filepath.EvalSymlinks(sf); err == nil {
			resolved = r
		}

		seen := map[string]bool{}
		for _, k := range []string{ids.MakeID(sf), ids.MakeID(resolved), ids.MakeID(base.FileStem(sf)), ids.MakeID(base.FileStem(resolved))} {
			if !seen[k] {
				seen[k] = true
				e.keys = append(e.keys, k)
			}
		}

		cache[sf] = e

		return e
	}

	remap := map[string]string{}

	for _, n := range c.nodes {
		if n.SourceFile == "" || !filepath.IsAbs(n.SourceFile) {
			n.SourceFile = filepath.ToSlash(n.SourceFile)

			continue
		}

		e := get(n.SourceFile)
		for _, k := range e.keys {
			if k == e.canonical {
				continue
			}

			if _, ok := remap[k]; ok {
				continue
			}

			if owned[k] && n.ID != k {
				continue
			}

			remap[k] = e.canonical
		}

		n.SourceFile = e.sf
		n.OriginFile = ""
	}

	for _, ed := range c.edges {
		if ed.SourceFile != "" && filepath.IsAbs(ed.SourceFile) {
			ed.SourceFile = get(ed.SourceFile).sf
		} else {
			ed.SourceFile = filepath.ToSlash(ed.SourceFile)
		}
	}

	for _, n := range c.nodes {
		n.OriginFile = ""
	}

	if len(remap) == 0 {
		return
	}

	canon := func(nid string) string {
		if v, ok := remap[nid]; ok {
			return v
		}

		if strings.HasSuffix(nid, "__entry") {
			if v, ok := remap[strings.TrimSuffix(nid, "__entry")]; ok {
				return v + "__entry"
			}
		}

		if !owned[nid] {
			for idx := strings.LastIndexByte(nid, '_'); idx > 0; idx = strings.LastIndexByte(nid[:idx], '_') {
				if v, ok := remap[nid[:idx]]; ok {
					return v + nid[idx:]
				}
			}
		}

		return nid
	}

	for _, n := range c.nodes {
		n.ID = canon(n.ID)
	}

	for _, e := range c.edges {
		e.Source = canon(e.Source)
		e.Target = canon(e.Target)
	}
}

// portableOutOfRoot renders a path outside root as a short relative form, or
// its basename when it lives far away.
func portableOutOfRoot(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return filepath.Base(p)
	}

	rel = filepath.ToSlash(rel)
	up := 0

	for _, seg := range strings.Split(rel, "/") {
		if seg != ".." {
			break
		}

		up++
	}

	if up > 3 {
		return filepath.Base(p)
	}

	return rel
}

func addSet(m map[string]map[string]bool, k, v string) {
	s := m[k]
	if s == nil {
		s = map[string]bool{}
		m[k] = s
	}

	s[v] = true
}

func hasAnySuffix(s string, suffixes []string) bool {
	for _, x := range suffixes {
		if strings.HasSuffix(s, x) {
			return true
		}
	}

	return false
}

var goModuleRe = regexp.MustCompile(`(?m)^\s*module\s+(\S+)`)

type goModCache struct {
	dirs map[string]string
}

func newGoModCache() *goModCache { return &goModCache{dirs: map[string]string{}} }

// importPathForFile returns the canonical Go import path of the package
// containing source file sf.
func (g *goModCache) importPathForFile(root, sf string) string {
	if sf == "" {
		return ""
	}

	p := sf
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}

	dir := filepath.Dir(p)

	modDir, modPath := "", ""

	for cand := dir; ; {
		if v, ok := g.dirs[cand]; ok {
			if v != "" {
				modDir, modPath = cand, v
			}

			break
		}

		raw, err := os.ReadFile(filepath.Join(cand, "go.mod"))
		if err == nil {
			m := goModuleRe.FindSubmatch(raw)

			v := ""
			if m != nil {
				v = string(m[1])
			}

			g.dirs[cand] = v
			modDir, modPath = cand, v

			break
		}

		parent := filepath.Dir(cand)
		if parent == cand {
			break
		}

		cand = parent
	}

	if modDir == "" || modPath == "" {
		return ""
	}

	rel, err := filepath.Rel(modDir, dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}

	if rel == "." {
		return modPath
	}

	return modPath + "/" + filepath.ToSlash(rel)
}
