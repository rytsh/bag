package langs

import (
	"strings"
	"unicode"
)

// textwrap.shorten(text, width, placeholder="…") port. Python splits words
// with TextWrapper.wordsep_re (hyphen-aware) and, for a single line, keeps
// whole chunks while they fit with the placeholder appended.

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' }

func isLetterRune(r rune) bool { return unicode.IsLetter(r) || r == '_' }

// splitChunks approximates TextWrapper._split_chunks for text that has
// already been whitespace-collapsed.
func splitChunks(text string) []string {
	var out []string

	for i, tok := range strings.Split(text, " ") {
		if i > 0 {
			out = append(out, " ")
		}

		out = append(out, splitHyphens(tok)...)
	}

	return out
}

// splitHyphens splits a word after hyphens that join letter runs, and
// separates em-dash runs ("--") between words.
func splitHyphens(w string) []string {
	r := []rune(w)

	var (
		out   []string
		start int
	)

	for i := 0; i < len(r); i++ {
		if r[i] != '-' {
			continue
		}

		// em-dash: 2+ hyphens between word chars
		j := i
		for j < len(r) && r[j] == '-' {
			j++
		}

		if j-i >= 2 {
			if i > 0 && strings.ContainsRune(`!"'&.,?`, r[i-1]) || i > 0 && isWordRune(r[i-1]) {
				if j < len(r) && isWordRune(r[j]) {
					if i > start {
						out = append(out, string(r[start:i]))
					}

					out = append(out, string(r[i:j]))
					start = j
				}
			}

			i = j - 1

			continue
		}

		// hyphenated word: preceded by 2 letters (or letter-letter-) and
		// followed by letter [hyphen?] letter.
		before := i >= 2 && isLetterRune(r[i-1]) && isLetterRune(r[i-2])
		before = before || (i >= 3 && isLetterRune(r[i-1]) && r[i-2] == '-' && isLetterRune(r[i-3]))

		after := i+1 < len(r) && isLetterRune(r[i+1])
		if after {
			k := i + 2
			if k < len(r) && r[k] == '-' {
				k++
			}

			after = k < len(r) && isLetterRune(r[k])
		}

		if before && after && i+1 > start {
			out = append(out, string(r[start:i+1]))
			start = i + 1
		}
	}

	if start < len(r) {
		out = append(out, string(r[start:]))
	}

	return out
}

func runeLen(s string) int { return len([]rune(s)) }

// ShortenLabel mimics textwrap.shorten(text, width, placeholder="…"), with
// Graphify's fallback to a hard cut when only the placeholder would remain.
func ShortenLabel(text string, width int) string {
	flat := strings.Join(strings.Fields(text), " ")
	if runeLen(flat) <= width {
		return flat
	}

	const ph = "…"

	chunks := splitChunks(flat)
	cur := []string{}
	curLen := 0

	for len(chunks) > 0 {
		l := runeLen(chunks[0])
		if curLen+l > width {
			break
		}

		cur = append(cur, chunks[0])
		curLen += l
		chunks = chunks[1:]
	}

	// break_long_words: a first chunk longer than the line is split.
	if len(chunks) > 0 && runeLen(chunks[0]) > width && len(cur) == 0 {
		r := []rune(chunks[0])
		cur = append(cur, string(r[:width]))
		curLen = width
	}

	if len(cur) > 0 && strings.TrimSpace(cur[len(cur)-1]) == "" {
		curLen -= runeLen(cur[len(cur)-1])
		cur = cur[:len(cur)-1]
	}

	for len(cur) > 0 {
		last := cur[len(cur)-1]
		if strings.TrimSpace(last) != "" && curLen+runeLen(ph) <= width {
			return strings.Join(cur, "") + ph
		}

		curLen -= runeLen(last)
		cur = cur[:len(cur)-1]
	}

	r := []rune(flat)

	return string(r[:width-1]) + ph
}
