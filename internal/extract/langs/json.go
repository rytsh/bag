package langs

import (
	"path/filepath"
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// Config/manifest JSON extraction: key structure plus dependency, extends and
// $ref edges. Data JSON (fixtures, datasets) is skipped.
//
// Adapted from Graphify's extractors/json_config.py (Apache-2.0).

const jsonMaxBytes = 1 << 20

var (
	configJSONNames = base.NewSet(
		"package.json", "tsconfig.json", "jsconfig.json", "composer.json",
		"deno.json", "deno.jsonc", "bower.json", "manifest.json",
		"app.json", "now.json", "vercel.json", "angular.json", "nest-cli.json",
		"biome.json", "biome.jsonc", "renovate.json", ".babelrc", ".babelrc.json",
		".eslintrc.json", ".prettierrc.json", ".prettierrc", "babel.config.json",
	)
	configJSONKeys = base.NewSet(
		"dependencies", "devDependencies", "peerDependencies",
		"optionalDependencies", "bundleDependencies", "bundledDependencies",
		"extends", "$ref", "$schema", "compilerOptions",
	)
	jsonDepKeys = base.NewSet(
		"dependencies", "devDependencies", "peerDependencies",
		"optionalDependencies", "bundleDependencies", "bundledDependencies",
	)
)

func jsonText(n *tsx.Node) string {
	if n == nil {
		return ""
	}

	if n.Type() == "string" {
		return strings.Trim(n.Text(), `"'`)
	}

	return n.Text()
}

func isConfigJSON(path string, obj *tsx.Node) bool {
	name := strings.ToLower(filepath.Base(path))
	if configJSONNames.Has(name) {
		return true
	}

	for _, suf := range []string{".eslintrc.json", ".prettierrc.json", ".babelrc.json", "tsconfig.json", "jsconfig.json"} {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}

	for _, p := range obj.Children() {
		if p.Type() == "pair" && configJSONKeys.Has(jsonText(p.Field("key"))) {
			return true
		}
	}

	return false
}

// ExtractJSON extracts a config/manifest .json file.
func ExtractJSON(path, _ string, src []byte) *model.Extraction {
	if len(src) > jsonMaxBytes {
		return &model.Extraction{Error: "json file too large to index"}
	}

	tree, err := tsx.Parse("json", src)
	if err != nil {
		return &model.Extraction{Error: err.Error()}
	}
	defer tree.Release()

	doc := tree.Root
	if doc.Type() == "document" && len(doc.Children()) > 0 {
		doc = doc.Children()[0]
	}

	if doc.Type() != "object" || !isConfigJSON(path, doc) {
		return &model.Extraction{Skipped: true}
	}

	b := base.NewBuilder(path)
	stem := b.Stem
	file := b.FileID
	b.AddNode(file, filepath.Base(path), 1)

	addNode := func(id, label string, line int, fileType string) {
		if id == "" || b.Has(id) {
			return
		}

		n := b.AddNode(id, label, line)
		n.FileType = fileType
	}

	addEdge := func(src, tgt, rel string, line int, ctx string) {
		if src == "" || tgt == "" || src == tgt {
			return
		}

		b.AddEdgeCtx(src, tgt, rel, line, ctx)
	}

	count := 0

	var walk func(obj *tsx.Node, parent, parentKey string, depth int) bool
	walk = func(obj *tsx.Node, parent, parentKey string, depth int) bool {
		if depth > 6 {
			return true
		}

		for _, pair := range obj.Children() {
			if pair.Type() != "pair" {
				continue
			}

			if count >= 500 {
				return false
			}

			count++

			key := jsonText(pair.Field("key"))
			if key == "" || ids.NormalizeID(key) == "" {
				continue
			}

			parts := []string{stem}
			if parentKey != "" {
				parts = append(parts, parentKey)
			}

			keyID := ids.MakeID(append(parts, key)...)
			if keyID == "" {
				continue
			}

			line := pair.Line()
			addNode(keyID, key, line, model.FileTypeCode)
			addEdge(parent, keyID, "contains", line, "")

			val := pair.Field("value")
			if val == nil {
				continue
			}

			switch {
			case val.Type() == "object":
				if !walk(val, keyID, key, depth+1) {
					return false
				}
			case val.Type() == "array" && key == "extends":
				for _, it := range val.Children() {
					if it.Type() != "string" {
						continue
					}

					if ref := jsonText(it); ref != "" {
						rid := ids.MakeID("ref", ref)
						addNode(rid, ref, line, model.FileTypeConcept)
						addEdge(keyID, rid, "extends", line, "import")
					}
				}
			case val.Type() == "string":
				v := jsonText(val)
				if v == "" {
					continue
				}

				switch {
				case key == "extends":
					rid := ids.MakeID("ref", v)
					addNode(rid, v, line, model.FileTypeConcept)
					addEdge(file, rid, "extends", line, "import")
				case key == "$ref":
					addEdge(parent, ids.MakeID("ref", v), "references", line, "")
				case jsonDepKeys.Has(parentKey):
					did := ids.MakeID("ref", key)
					addNode(did, key, line, model.FileTypeConcept)
					addEdge(file, did, "imports", line, "import")
				}
			}
		}

		return true
	}

	walk(doc, file, "", 0)

	return b.ResultUnfiltered()
}
