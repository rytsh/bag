// Package langs registers every built-in language extractor.
package langs

import (
	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/extract/golang"
)

func reg(name string, fn extract.Extractor, exts ...string) {
	extract.Register(&extract.Language{Name: name, Extensions: exts, Extract: fn})
}

func init() {
	reg("go", golang.Extract, ".go")
	reg("python", ExtractPython, ".py")
	reg("java", ExtractJava, ".java")
	reg("javascript", ExtractJS, ".js", ".jsx", ".mjs", ".cjs")
	reg("typescript", ExtractTS, ".ts", ".mts", ".cts")
	reg("tsx", ExtractTSX, ".tsx")
	reg("c", ExtractC, ".c")
	reg("c-header", ExtractHeader, ".h")
	reg("cpp", ExtractCPP, ".cpp", ".cc", ".cxx", ".hpp", ".hh", ".hxx", ".cu", ".cuh", ".metal")
	reg("csharp", ExtractCSharp, ".cs")
	reg("kotlin", ExtractKotlin, ".kt", ".kts")
	reg("scala", ExtractScala, ".scala")
	reg("php", ExtractPHP, ".php")
	reg("ruby", ExtractRuby, ".rb", ".rake")
	reg("lua", ExtractLua, ".lua", ".toc")
	reg("luau", ExtractLuau, ".luau")
	reg("swift", ExtractSwift, ".swift")
	reg("objc", ExtractDotM, ".m")
	reg("objcpp", ExtractObjC, ".mm")
}
