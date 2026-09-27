// Package langs registers every built-in language extractor.
package langs

import (
	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/extract/golang"
)

func init() {
	extract.Register(&extract.Language{Name: "go", Extensions: []string{".go"}, Extract: golang.Extract})
	extract.Register(&extract.Language{Name: "python", Extensions: []string{".py"}, Extract: ExtractPython})
	extract.Register(&extract.Language{Name: "java", Extensions: []string{".java"}, Extract: ExtractJava})
	extract.Register(&extract.Language{Name: "javascript", Extensions: []string{".js", ".jsx", ".mjs", ".cjs"}, Extract: ExtractJS})
	extract.Register(&extract.Language{Name: "typescript", Extensions: []string{".ts", ".mts", ".cts"}, Extract: ExtractTS})
	extract.Register(&extract.Language{Name: "tsx", Extensions: []string{".tsx"}, Extract: ExtractTSX})
	extract.Register(&extract.Language{Name: "groovy", Extensions: []string{".groovy", ".gradle"}, Extract: ExtractGroovy})
}
