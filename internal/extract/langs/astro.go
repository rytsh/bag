package langs

import (
	"bytes"
	"regexp"

	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

var (
	astroFrontmatter    = regexp.MustCompile(`^\s*---\s*\r?\n([\s\S]*?)\r?\n---\s*(?:\r?\n|$)`)
	astroScripts        = regexp.MustCompile(`(?i)<script\b((?:"[^"]*"|'[^']*'|[^>"'])*)>([\s\S]*?)</script\s*>`)
	astroScriptType     = regexp.MustCompile(`(?i)\btype\s*=\s*["']?([^\s"']*)`)
	astroJSType         = regexp.MustCompile(`(?i)^(?:module|text/javascript|application/javascript)\b`)
	astroDynamicImports = regexp.MustCompile(`import\(\s*['"]([^'"]+)['"]\s*\)`)
	astroStaticImports  = regexp.MustCompile("import\\s+(?:[^'\"`;]+?\\s+from\\s+)?['\"]([^'\"]+)['\"]")
)

// maskAstro is adapted from Graphify's _astro_mask_non_script (Apache-2.0).
// Byte-preserving masking keeps both line numbers and UTF-8 source offsets.
func maskAstro(src []byte) []byte {
	masked := bytes.Repeat([]byte{' '}, len(src))
	for i, c := range src {
		if c == '\n' || c == '\r' {
			masked[i] = c
		}
	}
	keep := func(start, end int) {
		copy(masked[start:end], src[start:end])
		if end < len(masked) && masked[end] == ' ' {
			masked[end] = ';'
		}
	}
	start := 0
	if fm := astroFrontmatter.FindSubmatchIndex(src); fm != nil {
		keep(fm[2], fm[3])
		start = fm[1]
	}
	for _, m := range astroScripts.FindAllSubmatchIndex(src[start:], -1) {
		attrs := src[start+m[2] : start+m[3]]
		if typ := astroScriptType.FindSubmatch(attrs); typ != nil {
			if !astroJSType.Match(typ[1]) {
				continue
			}
		}
		keep(start+m[4], start+m[5])
	}
	return masked
}

// ExtractAstro extracts TS setup and JS scripts, not the HTML template.
// Adapted from Graphify's extract_astro (Apache-2.0).
func ExtractAstro(path, root string, src []byte) *model.Extraction {
	res := ExtractTS(path, root, maskAstro(src))
	if res.Error != "" {
		return res
	}
	existing := map[string]bool{}
	for _, n := range res.Nodes {
		existing[n.ID] = true
	}
	rescue := func(raw, relation string) {
		id, stubSF, resolved, ok := jsRescuedSpecifier(path, raw)
		if !ok {
			return
		}
		if resolved == "" && !existing[id] {
			res.Nodes = append(res.Nodes, &model.Node{ID: id, Label: raw, FileType: model.FileTypeCode, SourceFile: stubSF, Extra: map[string]any{"confidence": model.Extracted}})
			existing[id] = true
		}
		res.Edges = append(res.Edges, &model.Edge{Source: ids.MakeID(path), Target: id, Relation: relation, Confidence: model.Extracted, SourceFile: path, TargetFile: resolved, NoWeight: true})
	}
	for _, m := range astroDynamicImports.FindAllSubmatch(src, -1) {
		rescue(string(m[1]), "dynamic_import")
	}
	var regions [][]byte
	if fm := astroFrontmatter.FindSubmatch(src); fm != nil {
		regions = append(regions, fm[1])
	}
	for _, m := range astroScripts.FindAllSubmatch(src, -1) {
		regions = append(regions, m[2])
	}
	for _, region := range regions {
		for _, m := range astroStaticImports.FindAllSubmatch(region, -1) {
			rescue(string(m[1]), "imports_from")
		}
	}
	return res
}
