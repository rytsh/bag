// Package detect walks a corpus directory and classifies files.
//
// Adapted from Graphify's graphify/detect.py (Apache-2.0).
package detect

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// FileType is a corpus category.
type FileType string

const (
	Code     FileType = "code"
	Document FileType = "document"
	Paper    FileType = "paper"
	Image    FileType = "image"
	Video    FileType = "video"
)

// DefaultOutDir is the output directory that is never scanned as input.
const DefaultOutDir = "graphify-out"

var (
	DocExtensions   = set(".md", ".mdx", ".qmd", ".skill", ".txt", ".rst", ".html", ".yaml", ".yml")
	PaperExtensions = set(".pdf")
	ImageExtensions = set(".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg")
	VideoExtensions = set(".mp4", ".mov", ".webm", ".mkv", ".avi", ".m4v", ".mp3", ".wav", ".m4a", ".ogg")
)

var skipDirs = set(
	"venv", ".venv", "node_modules", "__pycache__", ".git",
	"dist", "build", "target", "site-packages", "lib64",
	".pytest_cache", ".mypy_cache", ".ruff_cache",
	".tox", ".nox", ".eggs",
	"graphify-out", "bag-out",
	"lcov-report", "visual-tests", "visual-test", "__snapshots__",
	"storybook-static", "dist-protected",
	".next", ".nuxt", ".turbo", ".angular",
	".idea", ".cache", ".parcel-cache", ".svelte-kit", ".terraform", ".serverless",
	".graphify", ".obsidian", ".smart-env", ".worktrees",
)

var skipFiles = set(
	"package-lock.json", "yarn.lock", "pnpm-lock.yaml",
	"Cargo.lock", "poetry.lock", "Gemfile.lock",
	"composer.lock", "go.sum", "go.work.sum",
)

var credentialStoreDirs = set(".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker")

var sensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(^|[\\/])\.(env|envrc)(\.|$)`),
	regexp.MustCompile(`(?i)\.(pem|key|p12|pfx|cert|crt|der|p8)$`),
	regexp.MustCompile(`(?i)(^|[^A-Za-z0-9])(id_rsa|id_dsa|id_ecdsa|id_ed25519)(\.pub)?$`),
	regexp.MustCompile(`(?i)^secring(\.(gpg|pgp))?$`),
	regexp.MustCompile(`(?i)(\.netrc|\.pgpass|\.htpasswd|\.npmrc|\.pypirc|\.git-credentials|\.boto)$`),
}

var envTemplate = regexp.MustCompile(`(?i)\.(example|sample|template|dist|defaults?)$`)

// Options configure a scan.
type Options struct {
	// Gitignore enables .gitignore handling (.graphifyignore / .bagignore are
	// always honored).
	Gitignore bool
	// FollowSymlinks follows symlinked directories.
	FollowSymlinks bool
	// Excludes are extra gitignore-style patterns anchored at the root.
	Excludes []string
	// IsCode reports whether a path is a code file bag can extract. When nil
	// only the built-in extension list is used.
	IsCode func(path string) bool
	// OutDir is skipped (relative or absolute).
	OutDir string
}

// Result is the scan summary.
type Result struct {
	Root         string                `json:"scan_root"`
	Files        map[FileType][]string `json:"files"`
	TotalFiles   int                   `json:"total_files"`
	Skipped      []string              `json:"skipped_sensitive,omitempty"`
	Unclassified []string              `json:"unclassified,omitempty"`
}

// Detect walks root and classifies every file.
func Detect(root string, opt Options) (*Result, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}

	res := &Result{
		Root: root,
		Files: map[FileType][]string{
			Code: {}, Document: {}, Paper: {}, Image: {}, Video: {},
		},
	}

	base := &ignoreSet{}
	if opt.Gitignore {
		if gitDir := findGitDir(root); gitDir != "" {
			base.loadFile("", filepath.Join(gitDir, "info", "exclude"))
		}
	}

	for _, ex := range opt.Excludes {
		base.add("", ex)
	}

	outAbs := ""
	if opt.OutDir != "" {
		outAbs, _ = filepath.Abs(opt.OutDir)
	}

	var walk func(dir string, ign *ignoreSet) error
	walk = func(dir string, ign *ignoreSet) error {
		ign = ign.clone()
		relDir := relSlash(root, dir)
		if opt.Gitignore {
			ign.loadFile(relDir, filepath.Join(dir, ".gitignore"))
		}

		ign.loadFile(relDir, filepath.Join(dir, ".graphifyignore"))
		ign.loadFile(relDir, filepath.Join(dir, ".bagignore"))

		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil //nolint:nilerr // unreadable directories are skipped like Graphify
		}

		for _, e := range entries {
			name := e.Name()
			full := filepath.Join(dir, name)
			rel := relSlash(root, full)

			isDir := e.IsDir()
			if e.Type()&fs.ModeSymlink != 0 {
				st, err := os.Stat(full)
				if err != nil {
					continue
				}

				isDir = st.IsDir()
				if isDir && !opt.FollowSymlinks {
					continue
				}
			}

			if isDir {
				if skipDirs[name] || strings.HasSuffix(name, ".egg-info") {
					continue
				}

				if outAbs != "" && full == outAbs {
					continue
				}

				if ign.match(rel, true) {
					continue
				}

				if err := walk(full, ign); err != nil {
					return err
				}

				continue
			}

			if !e.Type().IsRegular() && e.Type()&fs.ModeSymlink == 0 {
				continue
			}

			if skipFiles[name] || ign.match(rel, false) {
				continue
			}

			if isSensitive(rel) {
				res.Skipped = append(res.Skipped, full)

				continue
			}

			ft, ok := Classify(full, opt.IsCode)
			if !ok {
				res.Unclassified = append(res.Unclassified, full)

				continue
			}

			res.Files[ft] = append(res.Files[ft], full)
			res.TotalFiles++
		}

		return nil
	}

	if err := walk(root, base); err != nil {
		return nil, err
	}

	for k := range res.Files {
		sort.Strings(res.Files[k])
	}

	return res, nil
}

// Classify returns the file type for path.
func Classify(path string, isCode func(string) bool) (FileType, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	name := strings.ToLower(filepath.Base(path))

	switch {
	case isCode != nil && isCode(path):
		return Code, true
	case strings.HasSuffix(name, ".mcp.json") || name == "mcp.json":
		return Code, true
	case PaperExtensions[ext]:
		return Paper, true
	case ImageExtensions[ext]:
		return Image, true
	case VideoExtensions[ext]:
		return Video, true
	case DocExtensions[ext]:
		return Document, true
	}

	if ext == "" && hasShebang(path) && isCode != nil && isCode(path) {
		return Code, true
	}

	return "", false
}

func isSensitive(rel string) bool {
	parts := strings.Split(rel, "/")
	for _, p := range parts[:len(parts)-1] {
		if credentialStoreDirs[strings.ToLower(p)] {
			return true
		}
	}

	name := parts[len(parts)-1]
	for _, re := range sensitivePatterns {
		if re.MatchString(name) && !envTemplate.MatchString(name) {
			return true
		}
	}

	return false
}

func hasShebang(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	line, _ := bufio.NewReader(f).ReadString('\n')

	return strings.HasPrefix(line, "#!")
}

// FirstLine returns the first line of a file (used for shebang detection).
func FirstLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	line, _ := bufio.NewReader(f).ReadString('\n')

	return strings.TrimRight(line, "\r\n")
}

func findGitDir(start string) string {
	cur := start
	for {
		g := filepath.Join(cur, ".git")
		if st, err := os.Stat(g); err == nil && st.IsDir() {
			return g
		}

		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}

		cur = parent
	}
}

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[i] = true
	}

	return m
}
