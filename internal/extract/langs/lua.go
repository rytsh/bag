package langs

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

var luaRequireRe = regexp.MustCompile(`require\s*[\('"]\s*['"]?([^'")\s]+)`)

func luaImportTarget(raw, fromPath string) string {
	rel := strings.ReplaceAll(raw, ".", "/")
	probe := filepath.Dir(fromPath)

	for range 6 {
		for _, sfx := range []string{".lua", ".luau"} {
			if c := filepath.Join(probe, rel+sfx); isFile(c) {
				return ids.MakeID(c)
			}
		}

		for _, sfx := range []string{".lua", ".luau"} {
			if c := filepath.Join(probe, rel, "init"+sfx); isFile(c) {
				return ids.MakeID(c)
			}
		}

		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}

		probe = parent
	}

	return ids.MakeID(raw)
}

func luaImport(x *generic.Ctx, n *tsx.Node) [][2]string {
	m := luaRequireRe.FindStringSubmatch(n.Text())
	if m == nil || m[1] == "" {
		return nil
	}

	e := x.B.AddEdgeCtx(x.B.FileID, luaImportTarget(m[1], x.Path), "imports", n.Line(), "import")
	e.SourceLocation = itoa(n.Line())
	e.ConfidenceScore = model.Score(1.0)

	return nil
}

func luaIsRequire(n *tsx.Node) bool {
	if n.Type() != "function_call" {
		return false
	}

	nn := n.Field("name")

	return nn != nil && nn.Text() == "require"
}

func luaExtraWalk(x *generic.Ctx, n *tsx.Node, _ string) bool {
	if luaIsRequire(n) {
		luaImport(x, n)

		return true
	}

	return false
}

var luaConfig = &generic.Config{
	Lang:              "lua",
	Grammar:           "lua",
	FunctionTypes:     base.NewSet("function_declaration"),
	ImportTypes:       base.NewSet("variable_declaration"),
	CallTypes:         base.NewSet("function_call"),
	CallFunctionField: "name",
	CallAccessorTypes: base.NewSet("method_index_expression"),
	CallAccessorField: "name",
	NameFallback:      []string{"identifier", "method_index_expression"},
	BodyFallback:      []string{"block"},
	FunctionBoundary:  base.NewSet("function_declaration"),
	ImportHandler:     luaImport,
	ExtraWalk:         luaExtraWalk,
}

// ExtractLua extracts a Lua file.
func ExtractLua(path, root string, src []byte) *model.Extraction {
	return generic.Extract(luaConfig, path, root, src)
}

// ExtractLuau extracts a Luau file using the Luau grammar.
func ExtractLuau(path, root string, src []byte) *model.Extraction {
	cfg := *luaConfig
	cfg.Grammar = "luau"

	return generic.Extract(&cfg, path, root, src)
}
