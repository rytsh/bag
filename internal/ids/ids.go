// Package ids is the single source of truth for node-ID normalization.
//
// Adapted from Graphify's graphify/ids.py (Apache-2.0). The recipe must stay
// byte-for-byte identical to Graphify so graph.json files produced by either
// tool reference the same node IDs.
//
// The recipe: iterate casefold then NFKC to a fixpoint, then replace runs of
// non-word characters with a single underscore, collapse repeated underscores
// and strip leading/trailing underscores.
package ids

import (
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var folder = cases.Fold()

// NormalizeID converts s to its canonical ID form.
//
// It is idempotent, caseless-stable and the result only contains word
// characters (as defined by Python's re \w with re.UNICODE) and '_'.
func NormalizeID(s string) string {
	cur := s
	for range 6 {
		nxt := norm.NFKC.String(folder.String(cur))
		if nxt == cur {
			break
		}
		cur = nxt
	}

	var b strings.Builder
	b.Grow(len(cur))

	pendingSep := false
	for _, r := range cur {
		if IsWord(r) {
			if pendingSep {
				b.WriteByte('_')
				pendingSep = false
			}
			b.WriteRune(r)

			continue
		}

		pendingSep = true
	}

	if pendingSep {
		b.WriteByte('_')
	}

	return collapseUnderscores(b.String())
}

// MakeID builds a canonical node ID from one or more name parts.
//
// Empty parts are skipped, every part has stray '_' / '.' trimmed from its
// edges, the parts are joined with '_' and the result is normalized.
func MakeID(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}

		kept = append(kept, strings.Trim(p, "_."))
	}

	return NormalizeID(strings.Join(kept, "_"))
}

// IsWord reports whether r matches Python's `\w` under re.UNICODE:
// alphanumeric characters (str.isalnum) and the underscore.
func IsWord(r rune) bool {
	if r == '_' {
		return true
	}

	return unicode.IsLetter(r) || unicode.IsNumber(r)
}

func collapseUnderscores(s string) string {
	if !strings.Contains(s, "__") && !strings.HasPrefix(s, "_") && !strings.HasSuffix(s, "_") {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))

	prevUnderscore := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '_' {
			if prevUnderscore {
				continue
			}

			prevUnderscore = true
		} else {
			prevUnderscore = false
		}

		b.WriteByte(c)
	}

	return strings.Trim(b.String(), "_")
}
