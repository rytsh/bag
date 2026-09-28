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
	reg("rust", ExtractRust, ".rs")
	reg("zig", ExtractZig, ".zig")
	reg("bash", ExtractBash, ".sh", ".bash")
	reg("elixir", ExtractElixir, ".ex", ".exs")
	reg("julia", ExtractJulia, ".jl")
	reg("json", ExtractJSON, ".json")
	reg("groovy", ExtractGroovy, ".groovy", ".gradle")
	extract.Register(&extract.Language{Name: "markdown", Extensions: []string{".md", ".mdx", ".qmd", ".skill"}, Extract: ExtractMarkdown, Document: true})

	registerManifests()
	extract.Register(&extract.Language{Name: "mcp", Filenames: []string{".mcp.json", "claude_desktop_config.json", "mcp.json", "mcp_servers.json"}, Extract: ExtractMCPConfig})
	registerTagsLanguages()

	extract.RegisterTypeResolver(resolvePHPTypeReferences)
	extract.RegisterTypeResolver(resolveJavaTypeReferences)
	extract.RegisterPreRewireResolver(resolvePythonCrossFileImports)
	extract.RegisterSymbolResolver(guardAmbiguousPythonImports)
	extract.RegisterSymbolResolver(resolvePythonSymbols)
	extract.RegisterSymbolResolver(resolveJSSymbols)
	extract.RegisterPostRewireResolver(resolveKotlinImportTargets)
	extract.RegisterPostRewireResolver(resolveCSharpTypeReferences)
	extract.RegisterPostRewireResolver(resolveCSharpImports)
	extract.RegisterResolver(resolveSwiftMemberCalls)
	extract.RegisterResolver(resolvePythonMemberCalls)
	extract.RegisterResolver(resolveRubyMemberCalls)
	extract.RegisterResolver(resolveTSMemberCalls)
	extract.RegisterResolver(resolveCppMemberCalls)
	extract.RegisterResolver(resolveObjCMemberCalls)
	extract.RegisterResolver(resolveCSharpMemberCalls)
	extract.RegisterResolver(resolveJavaMemberCalls)
	extract.RegisterResolver(resolveRustSelfMemberCalls)
	extract.RegisterResolver(resolveElixirImportTargets)
	extract.RegisterResolver(resolveKotlinQualifiedCalls)
	extract.RegisterResolver(resolveCSharpQualifiedCalls)
	extract.RegisterResolver(resolveCSharpInterfaceDispatch)
}
