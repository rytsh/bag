package graph

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// Entity deduplication for non-code nodes (documents, rationale, concepts).
// Code nodes are identified by id only and never merged by label.
//
// Adapted from Graphify's graphify/dedup.py (Apache-2.0), including its
// MinHash/LSH candidate stage (see minhash.go).

const (
	entropyThreshold = 2.5
	mergeThreshold   = 92.0
)

var (
	chunkSuffix = regexp.MustCompile(`_c\d+$`)
	variantSfx  = regexp.MustCompile(`^(.*[a-z])([0-9]+[a-z]*|[a-z]{2,})$`)
	digitRun    = regexp.MustCompile(`\d+`)
	caseFolder  = cases.Fold()
	stopwords   = map[string]bool{
		"a": true, "an": true, "the": true, "and": true, "or": true, "of": true, "for": true, "to": true,
		"in": true, "on": true, "at": true, "by": true, "with": true, "from": true, "as": true, "is": true,
		"are": true, "be": true, "this": true, "that": true, "its": true,
	}
	fileAnchoredNonCode = map[string]bool{"rationale": true, "document": true}
)

func normLabel(s string) string {
	s = caseFolder.String(norm.NFKC.String(s))

	var b strings.Builder

	prevSep := false

	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)

			prevSep = false

			continue
		}

		if !prevSep {
			b.WriteByte(' ')

			prevSep = true
		}
	}

	return strings.TrimSpace(b.String())
}

func entropy(label string) float64 {
	s := []rune(normLabel(label))
	if len(s) == 0 {
		return 0
	}

	freq := map[rune]int{}
	for _, r := range s {
		freq[r]++
	}

	n := float64(len(s))
	e := 0.0

	for _, c := range freq {
		p := float64(c) / n
		e -= p * math.Log2(p)
	}

	return e
}

func richness(n *model.Node) int {
	score := 0
	if n.Type != "" {
		score++
	}

	if len(n.Metadata) > 0 {
		score++
	}

	for k, v := range n.Extra {
		if k == "norm_label" || v == nil || v == "" {
			continue
		}

		score++
	}

	return score
}

func pickWinner(ns []*model.Node) *model.Node {
	key := func(n *model.Node) [5]int {
		suf := 0
		if chunkSuffix.MatchString(n.ID) {
			suf = 1
		}

		noSrc := 1
		if n.SourceFile != "" {
			noSrc = 0
		}

		noLoc := 1
		if n.SourceFile != "" && n.SourceLocation != "" {
			noLoc = 0
		}

		return [5]int{suf, noSrc, noLoc, -richness(n), len(n.ID)}
	}

	best := ns[0]
	bk := key(best)

	for _, n := range ns[1:] {
		k := key(n)
		for i := range k {
			if k[i] != bk[i] {
				if k[i] < bk[i] {
					best, bk = n, k
				}

				break
			}
		}
	}

	return best
}

type unionFind struct{ parent map[string]string }

func (u *unionFind) find(x string) string {
	if _, ok := u.parent[x]; !ok {
		u.parent[x] = x
	}

	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]]
		x = u.parent[x]
	}

	return x
}

func (u *unionFind) union(x, y string) {
	rx, ry := u.find(x), u.find(y)
	if rx != ry {
		u.parent[ry] = rx
	}
}

// DedupeEntities merges near-identical non-code nodes and rewires edges.
func DedupeEntities(nodes []*model.Node, edges []*model.Edge) ([]*model.Node, []*model.Edge) {
	uf := &unionFind{parent: map[string]string{}}

	byNorm := map[string][]*model.Node{}

	var normOrder []string

	var candidates []*model.Node

	seenNorm := map[string]bool{}

	for _, n := range nodes {
		if n.FileType == model.FileTypeCode {
			continue
		}

		k := normLabel(labelOr(n))
		if k == "" {
			continue
		}

		if _, ok := byNorm[k]; !ok {
			normOrder = append(normOrder, k)
		}

		byNorm[k] = append(byNorm[k], n)

		if !seenNorm[k] {
			seenNorm[k] = true

			if entropy(n.Label) >= entropyThreshold {
				candidates = append(candidates, n)
			}
		}
	}

	merges := 0

	for _, k := range normOrder {
		group := byNorm[k]
		if len(group) <= 1 {
			continue
		}

		byFile := map[string][]*model.Node{}

		var files []string

		for _, n := range group {
			if _, ok := byFile[n.SourceFile]; !ok {
				files = append(files, n.SourceFile)
			}

			byFile[n.SourceFile] = append(byFile[n.SourceFile], n)
		}

		for _, sf := range files {
			fg := byFile[sf]
			if sf == "" || len(fg) < 2 {
				continue
			}

			w := pickWinner(fg)
			for _, n := range fg {
				uf.union(w.ID, n.ID)
			}

			merges += len(fg) - 1
		}

		var mergeable []*model.Node

		for _, n := range group {
			if (n.FileType == model.FileTypeConcept || (fileAnchoredNonCode[n.FileType] && readsAsFileEntity(n))) &&
				n.SourceFile != "" && entropy(n.Label) >= entropyThreshold {
				mergeable = append(mergeable, n)
			}
		}

		sort.Slice(mergeable, func(i, j int) bool { return mergeable[i].ID < mergeable[j].ID })

		if len(mergeable) > 1 {
			w := pickWinner(mergeable)
			for _, n := range mergeable {
				if uf.find(w.ID) != uf.find(n.ID) {
					uf.union(w.ID, n.ID)
					merges++
				}
			}
		}
	}

	if len(candidates) >= 2 {
		index := newLSH()
		hashes := make([]*minHash, len(candidates))
		normCache := make([]string, len(candidates))

		for i, c := range candidates {
			normCache[i] = normLabel(labelOr(c))
			hashes[i] = newMinHash(normCache[i])
			index.insert(i, hashes[i])
		}

		for i, a := range candidates {
			for _, j := range index.query(hashes[i]) {
				b := candidates[j]
				if j == i || uf.find(a.ID) == uf.find(b.ID) {
					continue
				}

				if shouldFuzzyMerge(a, b, normCache[i], normCache[j]) {
					w := pickWinner([]*model.Node{a, b})
					uf.union(w.ID, a.ID)
					uf.union(w.ID, b.ID)
					merges++
				}
			}
		}
	}

	if merges == 0 {
		return nodes, edges
	}

	groups := map[string][]*model.Node{}
	for _, n := range nodes {
		if _, ok := uf.parent[n.ID]; ok {
			r := uf.find(n.ID)
			groups[r] = append(groups[r], n)
		}
	}

	remap := map[string]string{}

	for _, g := range groups {
		if len(g) < 2 {
			continue
		}

		w := pickWinner(g)
		for _, n := range g {
			if n.ID != w.ID {
				mergeMissing(w, n)
				remap[n.ID] = w.ID
			}
		}
	}

	var outNodes []*model.Node

	for _, n := range nodes {
		if _, gone := remap[n.ID]; !gone {
			outNodes = append(outNodes, n)
		}
	}

	var outEdges []*model.Edge

	for _, e := range edges {
		src, tgt := e.Source, e.Target
		ns, nt := src, tgt

		if v, ok := remap[src]; ok {
			ns = v
		}

		if v, ok := remap[tgt]; ok {
			nt = v
		}

		if ns == nt && src != tgt {
			continue
		}

		if ns != src || nt != tgt {
			cp := *e
			cp.Source, cp.Target = ns, nt
			e = &cp
		}

		outEdges = append(outEdges, e)
	}

	return outNodes, outEdges
}

func labelOr(n *model.Node) string {
	if n.Label != "" {
		return n.Label
	}

	return n.ID
}

// fileStructureNodeKinds mark nodes that are structure of their file (the
// file's own page, a heading) rather than an entity mentioned inside it.
var fileStructureNodeKinds = map[string]bool{"page": true, "heading": true}

// readsAsFileEntity reports whether n is an entity found inside its file
// rather than the file itself or a stamped structural part of it.
//
// Adapted from Graphify's dedup._reads_as_file_entity (Apache-2.0).
func readsAsFileEntity(n *model.Node) bool {
	if n.ID == "" || n.SourceFile == "" {
		return false
	}

	if k, _ := n.Extra["node_kind"].(string); fileStructureNodeKinds[k] {
		return false
	}

	return !idPrefixes(n.SourceFile)[n.ID]
}

var extSuffix = regexp.MustCompile(`\.[^./]+$`)

// idPrefixes returns every id prefix a node extracted from sourceFile may
// mint: each trailing slice of the extension-stripped, slugified path.
//
// Adapted from Graphify's dedup._id_prefixes (Apache-2.0).
func idPrefixes(sourceFile string) map[string]bool {
	stem := extSuffix.ReplaceAllString(strings.ReplaceAll(sourceFile, `\`, "/"), "")

	var segs []string

	for _, p := range strings.Split(stem, "/") {
		if s := ids.NormalizeID(p); s != "" {
			segs = append(segs, s)
		}
	}

	out := make(map[string]bool, len(segs))
	for i := range segs {
		out[strings.Join(segs[i:], "_")] = true
	}

	return out
}

func shouldFuzzyMerge(a, b *model.Node, na, nb string) bool {
	xfile := a.SourceFile != b.SourceFile

	// Cheap rejections first. File-anchored non-code never merges across
	// files, and Jaro-Winkler cannot reach the threshold when the lengths
	// differ too much: jaro <= (2 + short/long)/3, JW adds at most 0.4*(1-j).
	if (fileAnchoredNonCode[a.FileType] || fileAnchoredNonCode[b.FileType]) && xfile {
		return false
	}

	la, lb := runeLen(na), runeLen(nb)
	if la == 0 || lb == 0 {
		return false
	}

	short, long := min(la, lb), max(la, lb)
	if bound := (2 + float64(short)/float64(long)) / 3; bound+0.4*(1-bound) < mergeThreshold/100 {
		return false
	}

	var score float64

	if xfile && max(runeLen(na), runeLen(nb)) >= 12 {
		score = jaro(na, nb) * 100
	} else {
		score = jaroWinkler(na, nb) * 100
	}

	if score < mergeThreshold || isVariantPair(na, nb) || shortLabelBlocked(na, nb, score) {
		return false
	}

	lo, hi := na, nb
	if runeLen(lo) > runeLen(hi) {
		lo, hi = hi, lo
	}

	if strings.HasPrefix(hi, lo) && hi != lo {
		return false
	}

	if numericTokensDiffer(na, nb) || contentTokenSwap(na, nb) {
		return false
	}

	if (fileAnchoredNonCode[a.FileType] || fileAnchoredNonCode[b.FileType]) && xfile {
		return false
	}

	if score < mergeThreshold {
		return false
	}

	if na == nb && xfile {
		return false
	}

	return true
}

func runeLen(s string) int { return len([]rune(s)) }

func isVariantPair(a, b string) bool {
	if a == b || max(runeLen(a), runeLen(b)) >= 12 {
		return false
	}

	ma, mb := variantSfx.FindStringSubmatch(a), variantSfx.FindStringSubmatch(b)

	return ma != nil && mb != nil && ma[1] == mb[1] && ma[2] != mb[2]
}

func shortLabelBlocked(a, b string, score float64) bool {
	if max(runeLen(a), runeLen(b)) >= 12 {
		return false
	}

	return !(score >= 97 && runeLen(a) == runeLen(b) && damerauOSA(a, b) <= 1)
}

func numericTokensDiffer(a, b string) bool {
	if a == b {
		return false
	}

	norm := func(s string) []string {
		var out []string

		for _, t := range digitRun.FindAllString(s, -1) {
			t = strings.TrimLeft(t, "0")
			if t == "" {
				t = "0"
			}

			out = append(out, t)
		}

		sort.Strings(out)

		return out
	}

	x, y := norm(a), norm(b)
	if len(x) != len(y) {
		return true
	}

	for i := range x {
		if x[i] != y[i] {
			return true
		}
	}

	return false
}

func sameWordVariant(x, y string) bool {
	if runeLen(x) == runeLen(y) && damerauOSA(x, y) <= 1 {
		return true
	}

	if min(runeLen(x), runeLen(y)) < 6 {
		return false
	}

	return jaroWinkler(x, y)*100 >= mergeThreshold
}

func contentTokenSwap(a, b string) bool {
	ta, tb := strings.Fields(a), strings.Fields(b)
	if len(ta) != len(tb) {
		return false
	}

	for i := range ta {
		x, y := ta[i], tb[i]
		if x == y || stopwords[x] || stopwords[y] || sameWordVariant(x, y) {
			continue
		}

		return true
	}

	return false
}

// jaro similarity in [0,1].
func jaro(s1, s2 string) float64 {
	a, b := []rune(s1), []rune(s2)
	if len(a) == 0 && len(b) == 0 {
		return 1
	}

	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	window := max(len(a), len(b))/2 - 1
	if window < 0 {
		window = 0
	}

	ma := make([]bool, len(a))
	mb := make([]bool, len(b))
	matches := 0

	for i := range a {
		lo, hi := max(0, i-window), min(len(b)-1, i+window)
		for j := lo; j <= hi; j++ {
			if !mb[j] && a[i] == b[j] {
				ma[i], mb[j] = true, true
				matches++

				break
			}
		}
	}

	if matches == 0 {
		return 0
	}

	t, k := 0, 0

	for i := range a {
		if !ma[i] {
			continue
		}

		for !mb[k] {
			k++
		}

		if a[i] != b[k] {
			t++
		}

		k++
	}

	m := float64(matches)

	// rapidfuzz counts half-transpositions with integer division.
	return (m/float64(len(a)) + m/float64(len(b)) + (m-float64(t/2))/m) / 3
}

func jaroWinkler(s1, s2 string) float64 {
	j := jaro(s1, s2)
	if j <= 0.7 {
		return j
	}

	a, b := []rune(s1), []rune(s2)
	prefix := 0

	for i := 0; i < len(a) && i < len(b) && i < 4; i++ {
		if a[i] != b[i] {
			break
		}

		prefix++
	}

	return j + float64(prefix)*0.1*(1-j)
}

// damerauOSA is the optimal-string-alignment distance.
func damerauOSA(s1, s2 string) int {
	a, b := []rune(s1), []rune(s2)
	d := make([][]int, len(a)+1)

	for i := range d {
		d[i] = make([]int, len(b)+1)
		d[i][0] = i
	}

	for j := 0; j <= len(b); j++ {
		d[0][j] = j
	}

	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}

			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)

			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}

	return d[len(a)][len(b)]
}
