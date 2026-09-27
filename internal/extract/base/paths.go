package base

import (
	"path"
	"regexp"
	"strings"
)

var testDirSegments = NewSet("tests", "test", "spec", "specs", "__tests__")

var testFilePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^test_.*`),
	regexp.MustCompile(`(?i)^.*_test\..+$`),
	regexp.MustCompile(`(?i)^.*\.test\..+$`),
	regexp.MustCompile(`(?i)^.*\.spec\..+$`),
	regexp.MustCompile(`(?i)^.*_spec\..+$`),
	regexp.MustCompile(`(?i)^.*\.tests\.ps1$`),
	regexp.MustCompile(`^.*Test\.java$`),
	regexp.MustCompile(`^.*Tests\.java$`),
	regexp.MustCompile(`^.*Tests\.cs$`),
}

// IsTestPath classifies a source path as test code (segment-aware).
func IsTestPath(p string) bool {
	if p == "" {
		return false
	}

	norm := strings.ReplaceAll(p, `\`, "/")
	for _, seg := range strings.Split(norm, "/") {
		if testDirSegments.Has(strings.ToLower(seg)) {
			return true
		}
	}

	name := path.Base(norm)
	for _, re := range testFilePatterns {
		if re.MatchString(name) {
			return true
		}
	}

	return false
}

// DisambiguateCandidates resolves an ambiguous bare-name call to a single
// candidate using the non-test preference and path proximity tie-breakers.
func DisambiguateCandidates(candidates []string, files map[string]string, callSite string) string {
	if len(candidates) == 0 {
		return ""
	}

	if len(candidates) == 1 {
		return candidates[0]
	}

	callIsTest := IsTestPath(callSite)

	var testC, nonTest []string

	for _, c := range candidates {
		if IsTestPath(files[c]) {
			testC = append(testC, c)
		} else {
			nonTest = append(nonTest, c)
		}
	}

	var survivors []string

	if callIsTest {
		callNorm := strings.ReplaceAll(callSite, `\`, "/")

		var same []string

		for _, c := range testC {
			if strings.ReplaceAll(files[c], `\`, "/") == callNorm {
				same = append(same, c)
			}
		}

		if len(same) == 1 {
			return same[0]
		}

		switch {
		case len(testC) > 0:
			survivors = testC
		case len(nonTest) > 0:
			survivors = nonTest
		default:
			survivors = candidates
		}
	} else {
		survivors = nonTest
	}

	if len(survivors) == 1 {
		return survivors[0]
	}

	if len(survivors) == 0 {
		return ""
	}

	sub := make(map[string]string, len(survivors))
	for _, c := range survivors {
		sub[c] = files[c]
	}

	return proximityWinner(callSite, survivors, sub)
}

func proximityWinner(callSite string, order []string, files map[string]string) string {
	if callSite == "" {
		return ""
	}

	callNorm := strings.ReplaceAll(callSite, `\`, "/")
	callDir := path.Dir(callNorm)

	var sameFile, sameDir []string

	for _, c := range order {
		f := strings.ReplaceAll(files[c], `\`, "/")
		if f == callNorm {
			sameFile = append(sameFile, c)
		}
	}

	if len(sameFile) == 1 {
		return sameFile[0]
	}

	if len(sameFile) > 1 {
		return ""
	}

	for _, c := range order {
		if path.Dir(strings.ReplaceAll(files[c], `\`, "/")) == callDir {
			sameDir = append(sameDir, c)
		}
	}

	if len(sameDir) == 1 {
		return sameDir[0]
	}

	if len(sameDir) > 1 {
		return ""
	}

	callParts := splitDir(callDir)
	best := -1

	var winners []string

	for _, c := range order {
		parts := splitDir(path.Dir(strings.ReplaceAll(files[c], `\`, "/")))
		n := 0

		for i := 0; i < len(callParts) && i < len(parts); i++ {
			if callParts[i] != parts[i] {
				break
			}

			n++
		}

		switch {
		case n > best:
			best = n
			winners = []string{c}
		case n == best:
			winners = append(winners, c)
		}
	}

	if len(winners) == 1 && best > 0 {
		return winners[0]
	}

	return ""
}

func splitDir(d string) []string {
	if d == "." || d == "" {
		return nil
	}

	return strings.Split(strings.Trim(d, "/"), "/")
}

// LangFamilyByExt maps file extensions to their interop language family.
var LangFamilyByExt = map[string]string{
	".js": "jsts", ".jsx": "jsts", ".mjs": "jsts", ".cjs": "jsts",
	".ts": "jsts", ".tsx": "jsts", ".mts": "jsts", ".cts": "jsts",
	".vue": "jsts", ".svelte": "jsts", ".astro": "jsts",
	".java": "jvm", ".kt": "jvm", ".kts": "jvm",
	".scala": "jvm", ".groovy": "jvm", ".gradle": "jvm",
	".c": "native", ".h": "native", ".cpp": "native", ".cc": "native",
	".cxx": "native", ".hpp": "native", ".cu": "native", ".cuh": "native",
	".metal": "native", ".m": "native", ".mm": "native", ".swift": "native",
	".py":  "python",
	".go":  "go",
	".rs":  "rust",
	".cbl": "cobol", ".cob": "cobol", ".cobol": "cobol", ".cpy": "cobol",
	".r":   "r",
	".sol": "solidity",
	".erl": "erlang", ".hrl": "erlang", ".escript": "erlang",
	".rb": "ruby", ".rake": "ruby",
	".php": "php", ".phtml": "php", ".php3": "php", ".php4": "php",
	".php5": "php", ".php7": "php", ".phps": "php",
	".cs": "dotnet", ".vb": "dotnet", ".razor": "dotnet", ".cshtml": "dotnet", ".xaml": "dotnet",
	".lua": "lua", ".luau": "lua",
	".zig": "zig",
	".ex":  "elixir", ".exs": "elixir",
	".jl":   "julia",
	".dart": "dart",
	".sh":   "shell", ".bash": "shell",
	".ps1": "powershell", ".psm1": "powershell", ".psd1": "powershell",
}

// LangFamily returns the interop family of a source file, or "".
func LangFamily(sourceFile string) string {
	if sourceFile == "" {
		return ""
	}

	return LangFamilyByExt[strings.ToLower(Suffix(sourceFile))]
}

var caseInsensitiveExts = NewSet(
	".php", ".phtml", ".php3", ".php4", ".php5", ".php7", ".phps",
	".sql",
	".nim", ".nims", ".nimble",
	".cbl", ".cob", ".cobol", ".cpy",
	".vb",
)

// LangIsCaseInsensitive reports whether identifiers in the file's language
// resolve case-insensitively.
func LangIsCaseInsensitive(sourceFile string) bool {
	if sourceFile == "" {
		return false
	}

	return caseInsensitiveExts.Has(strings.ToLower(Suffix(sourceFile)))
}
