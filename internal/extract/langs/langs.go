// Package langs registers every built-in language extractor.
package langs

import (
	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/extract/golang"
)

func init() {
	extract.Register(&extract.Language{Name: "go", Extensions: []string{".go"}, Extract: golang.Extract})
}
