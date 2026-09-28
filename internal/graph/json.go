package graph

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	"golang.org/x/text/unicode/norm"

	"github.com/rytsh/bag/internal/model"
)

var confidenceScoreDefaults = map[string]float64{model.Extracted: 1.0, model.Inferred: 0.55, model.Ambiguous: 0.2}

// Communities maps community id -> member node ids.
type Communities map[int][]string

// NodeCommunity inverts communities.
func (c Communities) NodeCommunity() map[string]int {
	out := map[string]int{}

	for cid, members := range c {
		for _, n := range members {
			out[n] = cid
		}
	}

	return out
}

// WriteOptions control graph.json output.
type WriteOptions struct {
	Communities Communities
	Labels      map[int]string
	Commit      string
}

// NodeMap returns the JSON object for a node, including extras.
func NodeMap(n *model.Node) map[string]any {
	m := map[string]any{}
	for k, v := range n.Extra {
		m[k] = v
	}

	m["id"] = n.ID
	m["label"] = n.Label
	m["source_file"] = n.SourceFile

	if n.FileType != "" {
		m["file_type"] = n.FileType
	}

	if n.SourceLocation != "" || (n.EmptyLocation && n.SourceFile == "") {
		m["source_location"] = n.SourceLocation
	}

	if n.Type != "" {
		m["type"] = n.Type
	}

	if len(n.Metadata) > 0 {
		m["metadata"] = orderedMeta(n.Metadata, n.MetaOrder)
	}

	if n.Callable {
		m["_callable"] = true
	}

	// Extraction markers Graphify leaves on its nodes.
	if n.TopModule {
		m["_elixir_module"] = true
	}

	if n.RustDeclCount > 0 {
		m["_rust_declaration_count"] = n.RustDeclCount
	}

	if n.RustImplKey != "" {
		m["_rust_impl_key"] = n.RustImplKey
	}

	if n.CallableClass {
		m["_callable_class"] = true
	}

	return m
}

// EdgeMap returns the JSON object for an edge.
func EdgeMap(e *model.Edge) map[string]any {
	m := map[string]any{}
	for k, v := range e.Extra {
		m[k] = v
	}

	m["source"] = e.Source
	m["target"] = e.Target
	m["relation"] = e.Relation
	m["confidence"] = e.Confidence
	m["source_file"] = e.SourceFile

	if !e.NoWeight {
		m["weight"] = e.Weight
	}

	if e.SourceLocation != "" {
		m["source_location"] = e.SourceLocation
	}

	if e.Context != "" {
		m["context"] = e.Context
	}

	if len(e.Metadata) > 0 {
		m["metadata"] = orderedMeta(e.Metadata, e.MetaOrder)
	}

	if e.ConfidenceScore != nil {
		m["confidence_score"] = *e.ConfidenceScore
	} else {
		score, ok := confidenceScoreDefaults[e.Confidence]
		if !ok {
			score = 1.0
		}

		m["confidence_score"] = score
	}

	return m
}

// MarshalJSON renders the graph in Graphify's graph.json format.
func (g *Graph) MarshalGraphJSON(opt WriteOptions) ([]byte, error) {
	nc := opt.Communities.NodeCommunity()

	nodes := make([]orderedMap, 0, g.NumNodes())

	for _, n := range g.Nodes() {
		m := NodeMap(n)
		if n.Extra["external"] != true {
			m["_origin"] = originOf(n.Extra)
		}

		if cid, ok := nc[n.ID]; ok {
			m["community"] = cid
			if len(opt.Labels) > 0 {
				name, ok := opt.Labels[cid]
				if !ok {
					name = fmt.Sprintf("Community %d", cid)
				}

				m["community_name"] = name
			}
		} else {
			m["community"] = nil
		}

		m["norm_label"] = strings.ToLower(stripDiacritics(n.Label))
		nodes = append(nodes, canonical(m, "id", "label"))
	}

	links := make([]orderedMap, 0)

	for _, e := range g.Edges() {
		m := EdgeMap(e)
		m["_origin"] = originOf(e.Extra)
		links = append(links, canonical(m, "source", "target", "relation"))
	}

	sortBySortedJSON(nodes)
	sortBySortedJSON(links)

	hyper := make([]orderedMap, 0, len(g.Hyperedges))
	for _, h := range g.Hyperedges {
		m := map[string]any{}
		for k, v := range h.Extra {
			m[k] = v
		}

		m["nodes"] = h.Nodes
		setIf(m, "id", h.ID)
		setIf(m, "label", h.Label)
		setIf(m, "relation", h.Relation)
		setIf(m, "source_file", h.SourceFile)
		hyper = append(hyper, canonical(m))
	}

	sortBySortedJSON(hyper)

	top := orderedMap{
		{"directed", false},
		{"multigraph", false},
		{"graph", orderedMap{}},
		{"nodes", nodes},
		{"links", links},
		{"hyperedges", hyper},
	}

	if opt.Commit != "" {
		top = append(top, kv{"built_at_commit", opt.Commit})
	}

	var buf bytes.Buffer
	writeValue(&buf, top, 0, true)
	buf.WriteByte('\n')

	return buf.Bytes(), nil
}

func originOf(extra map[string]any) any {
	if v, ok := extra["_origin"]; ok {
		return v
	}

	return "ast"
}

func setIf(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}

// WriteJSON writes graph.json atomically.
func (g *Graph) WriteJSON(path string, opt WriteOptions) error {
	data, err := g.MarshalGraphJSON(opt)
	if err != nil {
		return err
	}

	return WriteFileAtomic(path, data)
}

// WriteFileAtomic writes data to path via a temp file + rename.
func WriteFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())

		return err
	}

	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())

		return err
	}

	return os.Rename(tmp.Name(), path)
}

func stripDiacritics(s string) string {
	d := norm.NFKD.String(s)

	var b strings.Builder

	for _, r := range d {
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Mc, r) && isCombining(r) {
			continue
		}

		b.WriteRune(r)
	}

	return b.String()
}

// isCombining approximates unicodedata.combining(c) != 0 for spacing marks.
func isCombining(r rune) bool {
	return norm.NFD.PropertiesString(string(r)).CCC() != 0
}

// ---- ordered JSON writer (Python json.dumps compatible) ----

type kv struct {
	k string
	v any
}

type orderedMap []kv

// orderedMeta renders metadata with the keys in order first, the rest
// sorted.
func orderedMeta(md map[string]any, order []string) any {
	if len(order) == 0 {
		return md
	}

	var lead []string

	for _, k := range order {
		if _, ok := md[k]; ok {
			lead = append(lead, k)
		}
	}

	return canonical(md, lead...)
}

func canonical(m map[string]any, lead ...string) orderedMap {
	out := orderedMap{}
	used := map[string]bool{}

	for _, k := range lead {
		if v, ok := m[k]; ok {
			out = append(out, kv{k, v})
			used[k] = true
		}
	}

	var rest []string

	for k := range m {
		if !used[k] {
			rest = append(rest, k)
		}
	}

	sort.Strings(rest)

	for _, k := range rest {
		out = append(out, kv{k, m[k]})
	}

	return out
}

// sortBySortedJSON sorts items by json.dumps(item, sort_keys=True,
// separators=(",", ":"), ensure_ascii=False) like Graphify.
func sortBySortedJSON(items []orderedMap) {
	keys := make([]string, len(items))

	for i, it := range items {
		var b bytes.Buffer
		writeCompactSorted(&b, it)
		keys[i] = b.String()
	}

	idx := make([]int, len(items))
	for i := range idx {
		idx[i] = i
	}

	sort.SliceStable(idx, func(a, b int) bool { return pyLess(keys[idx[a]], keys[idx[b]]) })

	sorted := make([]orderedMap, len(items))
	for i, j := range idx {
		sorted[i] = items[j]
	}

	copy(items, sorted)
}

// pyLess compares strings by code point like Python.
func pyLess(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	for i := 0; i < len(ra) && i < len(rb); i++ {
		if ra[i] != rb[i] {
			return ra[i] < rb[i]
		}
	}

	return len(ra) < len(rb)
}

func writeCompactSorted(b *bytes.Buffer, v any) {
	switch t := v.(type) {
	case orderedMap:
		m := map[string]any{}
		for _, p := range t {
			m[p.k] = p.v
		}

		writeCompactSorted(b, m)
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}

		sort.Slice(keys, func(i, j int) bool { return pyLess(keys[i], keys[j]) })
		b.WriteByte('{')

		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}

			writeString(b, k, false)
			b.WriteByte(':')
			writeCompactSorted(b, t[k])
		}

		b.WriteByte('}')
	case []any:
		b.WriteByte('[')

		for i, x := range t {
			if i > 0 {
				b.WriteByte(',')
			}

			writeCompactSorted(b, x)
		}

		b.WriteByte(']')
	case []string:
		b.WriteByte('[')

		for i, x := range t {
			if i > 0 {
				b.WriteByte(',')
			}

			writeString(b, x, false)
		}

		b.WriteByte(']')
	default:
		writeScalar(b, v, false)
	}
}

func writeValue(b *bytes.Buffer, v any, indent int, ascii bool) {
	pad := func(n int) {
		b.WriteByte('\n')

		for range n {
			b.WriteString("  ")
		}
	}

	switch t := v.(type) {
	case orderedMap:
		if len(t) == 0 {
			b.WriteString("{}")

			return
		}

		b.WriteByte('{')

		for i, p := range t {
			if i > 0 {
				b.WriteByte(',')
			}

			pad(indent + 1)
			writeString(b, p.k, ascii)
			b.WriteString(": ")
			writeValue(b, p.v, indent+1, ascii)
		}

		pad(indent)
		b.WriteByte('}')
	case map[string]any:
		writeValue(b, canonical(t), indent, ascii)
	case model.OrderedFields:
		om := make(orderedMap, 0, len(t))
		for _, f := range t {
			if k, ok := f[0].(string); ok {
				om = append(om, kv{k, f[1]})
			}
		}

		writeValue(b, om, indent, ascii)
	case []orderedMap:
		if len(t) == 0 {
			b.WriteString("[]")

			return
		}

		b.WriteByte('[')

		for i, x := range t {
			if i > 0 {
				b.WriteByte(',')
			}

			pad(indent + 1)
			writeValue(b, x, indent+1, ascii)
		}

		pad(indent)
		b.WriteByte(']')
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")

			return
		}

		b.WriteByte('[')

		for i, x := range t {
			if i > 0 {
				b.WriteByte(',')
			}

			pad(indent + 1)
			writeValue(b, x, indent+1, ascii)
		}

		pad(indent)
		b.WriteByte(']')
	case []string:
		if len(t) == 0 {
			b.WriteString("[]")

			return
		}

		b.WriteByte('[')

		for i, x := range t {
			if i > 0 {
				b.WriteByte(',')
			}

			pad(indent + 1)
			writeString(b, x, ascii)
		}

		pad(indent)
		b.WriteByte(']')
	default:
		writeScalar(b, v, ascii)
	}
}

func writeScalar(b *bytes.Buffer, v any, ascii bool) {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		writeString(b, t, ascii)
	case int:
		b.WriteString(strconv.Itoa(t))
	case int64:
		b.WriteString(strconv.FormatInt(t, 10))
	case float64:
		b.WriteString(pyFloat(t))
	case float32:
		b.WriteString(pyFloat(float64(t)))
	case json.Number:
		b.WriteString(t.String())
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			b.WriteString("null")

			return
		}

		var generic any
		if json.Unmarshal(raw, &generic) == nil {
			writeCompactSorted(b, generic)

			return
		}

		b.Write(raw)
	}
}

// pyFloat formats like Python's repr(float).
func pyFloat(f float64) string {
	if f == float64(int64(f)) && f < 1e16 && f > -1e16 {
		return strconv.FormatFloat(f, 'f', 1, 64)
	}

	s := strconv.FormatFloat(f, 'g', -1, 64)
	if strings.Contains(s, "e") {
		mant, exp, _ := strings.Cut(s, "e")
		sign := exp[0]
		digits := strings.TrimLeft(exp[1:], "0")

		if len(digits) < 2 {
			digits = strings.Repeat("0", 2-len(digits)) + digits
		}

		return mant + "e" + string(sign) + digits
	}

	return s
}

func writeString(b *bytes.Buffer, s string, ascii bool) {
	b.WriteByte('"')

	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r < 0x20:
				fmt.Fprintf(b, `\u%04x`, r)
			case ascii && r > 0x7e:
				if r > 0xffff {
					r1, r2 := utf16.EncodeRune(r)
					fmt.Fprintf(b, `\u%04x\u%04x`, r1, r2)
				} else {
					fmt.Fprintf(b, `\u%04x`, r)
				}
			default:
				b.WriteRune(r)
			}
		}
	}

	b.WriteByte('"')
}
