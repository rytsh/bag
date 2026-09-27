package extract

import (
	"path/filepath"
	"strings"
	"sync"
)

// Language describes one registered extractor.
type Language struct {
	Name       string
	Extensions []string
	Filenames  []string
	Extract    Extractor
}

var (
	regMu      sync.RWMutex
	byExt      = map[string]*Language{}
	byFilename = map[string]*Language{}
	languages  []*Language
)

// Register adds a language. Later registrations win for shared extensions,
// so dedicated extractors should register after generic ones.
func Register(l *Language) {
	regMu.Lock()
	defer regMu.Unlock()

	languages = append(languages, l)

	for _, e := range l.Extensions {
		byExt[strings.ToLower(e)] = l
	}

	for _, f := range l.Filenames {
		byFilename[f] = l
	}
}

// Lookup returns the extractor for path, or nil.
func Lookup(path string) Extractor {
	if l := LookupLanguage(path); l != nil {
		return l.Extract
	}

	return nil
}

// LookupLanguage returns the language registered for path.
func LookupLanguage(path string) *Language {
	regMu.RLock()
	defer regMu.RUnlock()

	name := filepath.Base(path)
	if l, ok := byFilename[name]; ok {
		return l
	}

	lower := strings.ToLower(name)
	// Longest multi-dot suffix first (".blade.php" before ".php").
	for i := 0; i < len(lower); i++ {
		if lower[i] != '.' || i == 0 {
			continue
		}

		if l, ok := byExt[lower[i:]]; ok {
			return l
		}
	}

	return nil
}

// IsCode reports whether bag can extract path.
func IsCode(path string) bool { return LookupLanguage(path) != nil }

// Languages lists registered languages.
func Languages() []*Language {
	regMu.RLock()
	defer regMu.RUnlock()

	return append([]*Language(nil), languages...)
}
