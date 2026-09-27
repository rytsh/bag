package detect

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ignoreRule is one parsed gitignore line anchored at a directory
// (slash-separated, relative to the scan root; "" for the root).
type ignoreRule struct {
	base     string
	pattern  string
	negate   bool
	dirOnly  bool
	anchored bool
}

// ignoreSet evaluates gitignore rules with last-match-wins semantics.
type ignoreSet struct {
	rules []ignoreRule
}

// parseIgnoreLine parses one raw ignore line per the gitignore spec.
func parseIgnoreLine(raw string) string {
	line := strings.TrimRight(raw, "\r\n")
	line = strings.TrimLeft(line, " \t")
	if line == "" || strings.HasPrefix(line, "#") {
		return ""
	}

	line = strings.ReplaceAll(line, `\#`, "#")

	for strings.HasSuffix(line, " ") && !strings.HasSuffix(line, `\ `) {
		line = line[:len(line)-1]
	}

	return line
}

func (s *ignoreSet) add(base, raw string) {
	line := parseIgnoreLine(raw)
	if line == "" {
		return
	}

	r := ignoreRule{base: base}
	if strings.HasPrefix(line, "!") {
		r.negate = true
		line = line[1:]
	}

	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimRight(line, "/")
	}

	if strings.HasPrefix(line, "/") {
		r.anchored = true
		line = strings.TrimLeft(line, "/")
	} else if strings.Contains(line, "/") {
		r.anchored = true
	}

	if line == "" {
		return
	}

	r.pattern = line
	s.rules = append(s.rules, r)
}

func (s *ignoreSet) loadFile(base, file string) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return
	}

	for _, l := range strings.Split(string(raw), "\n") {
		s.add(base, l)
	}
}

func (s *ignoreSet) clone() *ignoreSet {
	return &ignoreSet{rules: append([]ignoreRule(nil), s.rules...)}
}

// match reports whether rel (slash-separated, relative to the scan root)
// is ignored. Parent-directory exclusion is handled by the walker, which
// never descends into ignored directories.
func (s *ignoreSet) match(rel string, isDir bool) bool {
	ignored := false
	for _, r := range s.rules {
		if r.dirOnly && !isDir {
			continue
		}

		sub := rel
		if r.base != "" {
			if !strings.HasPrefix(rel, r.base+"/") {
				continue
			}

			sub = rel[len(r.base)+1:]
		}

		if r.matches(sub) {
			ignored = !r.negate
		}
	}

	return ignored
}

func (r ignoreRule) matches(sub string) bool {
	if !r.anchored {
		return globMatch(r.pattern, path.Base(sub))
	}

	return globMatch(r.pattern, sub)
}

// globMatch matches gitignore globs, supporting "**" segments.
func globMatch(pattern, name string) bool {
	if !strings.Contains(pattern, "**") {
		ok, _ := path.Match(pattern, name)

		return ok
	}

	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pat, parts []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			rest := pat[1:]
			if len(rest) == 0 {
				return true
			}

			for i := 0; i <= len(parts); i++ {
				if matchSegments(rest, parts[i:]) {
					return true
				}
			}

			return false
		}

		if len(parts) == 0 {
			return false
		}

		ok, _ := path.Match(pat[0], parts[0])
		if !ok {
			return false
		}

		pat, parts = pat[1:], parts[1:]
	}

	return len(parts) == 0
}

// relSlash returns p relative to root with forward slashes.
func relSlash(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return filepath.ToSlash(p)
	}

	if rel == "." {
		return ""
	}

	return filepath.ToSlash(rel)
}
