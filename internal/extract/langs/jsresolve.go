package langs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

var (
	jsResolveExts = []string{".ts", ".tsx", ".mts", ".cts", ".svelte", ".js", ".jsx", ".mjs", ".cjs"}
	jsIndexFiles  = []string{"index.ts", "index.tsx", "index.svelte", "index.js", "index.jsx", "index.mjs"}
)

// resolveJSImportPath resolves a JS/TS import candidate to a local file
// (or returns the candidate unchanged when nothing exists).
func resolveJSImportPath(cand string) string {
	cand = filepath.Clean(cand)
	if isFile(cand) {
		return cand
	}

	switch filepath.Ext(cand) {
	case ".js":
		if ts := strings.TrimSuffix(cand, ".js") + ".ts"; isFile(ts) {
			return ts
		}
	case ".jsx":
		if tsx := strings.TrimSuffix(cand, ".jsx") + ".tsx"; isFile(tsx) {
			return tsx
		}
	}

	for _, e := range jsResolveExts {
		if p := cand + e; isFile(p) {
			return p
		}
	}

	if isDir(cand) {
		for _, idx := range jsIndexFiles {
			if p := filepath.Join(cand, idx); isFile(p) {
				return p
			}
		}
	}

	return cand
}

type tsConfig struct {
	aliases map[string][]string
	order   []string
	baseURL string
}

var (
	tsCacheMu sync.Mutex
	tsCache   = map[string]*tsConfig{}
)

func findJSConfig(start string) (string, string) {
	for cur := start; ; {
		for _, name := range []string{"tsconfig.json", "jsconfig.json"} {
			p := filepath.Join(cur, name)
			if _, err := os.Stat(p); err == nil {
				return p, cur
			}
		}

		parent := filepath.Dir(cur)
		if parent == cur {
			return "", ""
		}

		cur = parent
	}
}

var (
	jsoncTokens   = regexp.MustCompile(`(?s)"(?:\\.|[^"\\])*"|/\*.*?\*/|//[^\n]*`)
	trailingComma = regexp.MustCompile(`,(\s*[}\]])`)
)

func stripJSONC(s string) string {
	s = jsoncTokens.ReplaceAllStringFunc(s, func(t string) string {
		if strings.HasPrefix(t, `"`) {
			return t
		}

		return ""
	})

	return trailingComma.ReplaceAllString(s, "$1")
}

func readJSONConfig(p string) map[string]any {
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil
	}

	var data map[string]any
	if json.Unmarshal(raw, &data) == nil {
		return data
	}

	if json.Unmarshal([]byte(stripJSONC(string(raw))), &data) == nil {
		return data
	}

	return nil
}

func readTSAliases(cfg, baseDir string, seen map[string]bool, into *tsConfig) {
	if seen[cfg] {
		return
	}

	seen[cfg] = true

	data := readJSONConfig(cfg)
	if data == nil {
		return
	}

	var extends []string

	switch e := data["extends"].(type) {
	case string:
		extends = []string{e}
	case []any:
		for _, x := range e {
			if s, ok := x.(string); ok {
				extends = append(extends, s)
			}
		}
	}

	for _, ext := range extends {
		if ext == "" || strings.HasPrefix(ext, "@") {
			continue
		}

		p := filepath.Clean(filepath.Join(baseDir, ext))
		if filepath.Ext(p) == "" {
			p += ".json"
		}

		if _, err := os.Stat(p); err == nil {
			readTSAliases(p, filepath.Dir(p), seen, into)
		}
	}

	if refs, ok := data["references"].([]any); ok {
		for _, r := range refs {
			m, ok := r.(map[string]any)
			if !ok {
				continue
			}

			rp, _ := m["path"].(string)
			if rp == "" {
				continue
			}

			p := filepath.Clean(filepath.Join(baseDir, rp))

			switch {
			case isDir(p):
				p = filepath.Join(p, "tsconfig.json")
			case filepath.Ext(p) == "":
				p += ".json"
			}

			if _, err := os.Stat(p); err == nil {
				readTSAliases(p, filepath.Dir(p), seen, into)
			}
		}
	}

	co, _ := data["compilerOptions"].(map[string]any)

	baseURL, _ := co["baseUrl"].(string)
	if baseURL == "" {
		baseURL = "."
	}

	pathsBase := filepath.Join(baseDir, baseURL)

	paths, _ := co["paths"].(map[string]any)
	for alias, t := range paths {
		arr, ok := t.([]any)
		if !ok || len(arr) == 0 {
			continue
		}

		var targets []string

		for _, x := range arr {
			if s, ok := x.(string); ok && s != "" {
				targets = append(targets, filepath.Join(pathsBase, s))
			}
		}

		if len(targets) > 0 {
			if _, exists := into.aliases[alias]; !exists {
				into.order = append(into.order, alias)
			}

			into.aliases[alias] = targets
		}
	}
}

func loadTSConfig(start string) *tsConfig {
	cfg, dir := findJSConfig(start)
	if cfg == "" {
		return nil
	}

	tsCacheMu.Lock()
	defer tsCacheMu.Unlock()

	if c, ok := tsCache[cfg]; ok {
		return c
	}

	c := &tsConfig{aliases: map[string][]string{}}
	readTSAliases(cfg, dir, map[string]bool{}, c)

	if data := readJSONConfig(cfg); data != nil {
		if co, ok := data["compilerOptions"].(map[string]any); ok {
			if b, ok := co["baseUrl"].(string); ok && b != "" {
				c.baseURL = filepath.Clean(filepath.Join(dir, b))
			}
		}
	}

	tsCache[cfg] = c

	return c
}

type aliasMatch struct {
	kind, length int
	captured     string
	wildcard     bool
	targets      []string
}

func matchAlias(raw, pattern string) (aliasMatch, bool) {
	if strings.Contains(pattern, "*") {
		if strings.Count(pattern, "*") != 1 {
			return aliasMatch{}, false
		}

		pre, suf, _ := strings.Cut(pattern, "*")
		if !strings.HasPrefix(raw, pre) || !strings.HasSuffix(raw, suf) {
			return aliasMatch{}, false
		}

		end := len(raw) - len(suf)
		if end < len(pre) {
			return aliasMatch{}, false
		}

		return aliasMatch{kind: 1, length: -len(pre), captured: raw[len(pre):end], wildcard: true}, true
	}

	if raw == pattern {
		return aliasMatch{kind: 0, length: -len(pattern)}, true
	}

	pre := strings.TrimRight(pattern, "/")
	if pre != "" && strings.HasPrefix(raw, pre+"/") {
		return aliasMatch{kind: 2, length: -len(pre), captured: strings.TrimLeft(raw[len(pre):], "/")}, true
	}

	return aliasMatch{}, false
}

func resolveTSAlias(raw string, c *tsConfig) string {
	var (
		best  aliasMatch
		found bool
	)

	for _, pattern := range c.order {
		m, ok := matchAlias(raw, pattern)
		if !ok {
			continue
		}

		if !found || m.kind < best.kind || (m.kind == best.kind && m.length < best.length) {
			m.targets = c.aliases[pattern]
			best, found = m, true
		}
	}

	if !found {
		if c.baseURL != "" {
			if r := resolveJSImportPath(filepath.Join(c.baseURL, raw)); isFile(r) {
				return r
			}
		}

		return ""
	}

	first := ""

	for _, t := range best.targets {
		var cand string

		if best.wildcard {
			cand = t
			if best.captured != "" {
				cand = strings.Replace(t, "*", best.captured, 1)
			}

			cand = filepath.Clean(cand)
		} else {
			cand = t
			if best.captured != "" {
				cand = filepath.Clean(filepath.Join(cand, best.captured))
			}
		}

		if r := resolveJSImportPath(cand); isFile(r) {
			return r
		}

		if first == "" {
			first = cand
		}
	}

	return first
}

// resolveJSModule resolves a specifier from startDir to a local path (or ""
// when it is external). Relative specifiers always yield a path, even when
// it does not exist on disk.
func resolveJSModule(raw, startDir string) string {
	if strings.HasPrefix(raw, ".") {
		return resolveJSImportPath(filepath.Join(startDir, raw))
	}

	if c := loadTSConfig(startDir); c != nil {
		if hit := resolveTSAlias(raw, c); hit != "" {
			return resolveJSImportPath(hit)
		}
	}

	if hit := resolveWorkspaceImport(raw, startDir); hit != "" {
		return hit
	}

	if strings.HasPrefix(raw, "@/") {
		if cfg, _ := findJSConfig(startDir); cfg == "" {
			sub := raw[2:]
			if sub != "" {
				anchor := jsProjectAnchor(startDir)
				if isDir(filepath.Join(anchor, "src")) {
					if c := resolveJSImportPath(filepath.Join(anchor, "src", sub)); isFile(c) {
						return c
					}
				}

				if c := resolveJSImportPath(filepath.Join(anchor, sub)); isFile(c) {
					return c
				}
			}
		}
	}

	return ""
}

func jsProjectAnchor(start string) string {
	for cur := start; ; {
		if isFile(filepath.Join(cur, "package.json")) {
			return cur
		}

		parent := filepath.Dir(cur)
		if parent == cur {
			return start
		}

		cur = parent
	}
}

// resolveJSTarget returns (target id source path, resolved path). When the
// import is external the id is ref_<package root> and resolved is "".
func resolveJSTarget(raw, fromPath string) (string, string, bool) {
	if raw == "" {
		return "", "", false
	}

	if rp := resolveJSModule(raw, filepath.Dir(fromPath)); rp != "" {
		return rp, rp, true
	}

	if strings.HasSuffix(raw, "/") || lastSeg(raw, "/") == "" {
		return "", "", false
	}

	pkg := strings.SplitN(raw, "/", 2)[0]
	if strings.HasPrefix(raw, "@") {
		parts := strings.SplitN(raw, "/", 3)
		if len(parts) >= 2 {
			pkg = parts[0] + "/" + parts[1]
		}
	}

	return "ref\x00" + pkg, "", true
}

// ---- workspaces ----

var (
	wsMu    sync.Mutex
	wsCache = map[string]map[string]string{}
)

func findWorkspaceRoot(start string) string {
	for cur := start; ; {
		if _, err := os.Stat(filepath.Join(cur, "pnpm-workspace.yaml")); err == nil {
			return cur
		}

		if data := readJSONConfig(filepath.Join(cur, "package.json")); data != nil {
			if _, ok := data["workspaces"]; ok {
				return cur
			}
		}

		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}

		cur = parent
	}
}

func workspaceGlobs(root string) []string {
	if raw, err := os.ReadFile(filepath.Join(root, "pnpm-workspace.yaml")); err == nil {
		var out []string

		in := false

		for _, line := range strings.Split(string(raw), "\n") {
			l := strings.TrimSpace(line)
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}

			if strings.HasPrefix(l, "packages:") {
				in = true

				continue
			}

			if in && strings.HasPrefix(l, "-") {
				v := strings.Trim(strings.TrimSpace(l[1:]), `'"`)
				if v != "" && !strings.HasPrefix(v, "!") {
					out = append(out, v)
				}

				continue
			}

			if in && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				break
			}
		}

		return out
	}

	data := readJSONConfig(filepath.Join(root, "package.json"))

	var list []any

	switch w := data["workspaces"].(type) {
	case []any:
		list = w
	case map[string]any:
		list, _ = w["packages"].([]any)
	}

	var out []string

	for _, x := range list {
		if s, ok := x.(string); ok && !strings.HasPrefix(s, "!") {
			out = append(out, s)
		}
	}

	return out
}

func loadWorkspacePackages(start string) map[string]string {
	root := findWorkspaceRoot(start)
	if root == "" {
		return nil
	}

	wsMu.Lock()
	defer wsMu.Unlock()

	if m, ok := wsCache[root]; ok {
		return m
	}

	m := map[string]string{}

	for _, g := range workspaceGlobs(root) {
		matches, _ := filepath.Glob(filepath.Join(root, strings.TrimSuffix(g, "/**")))
		for _, dir := range matches {
			data := readJSONConfig(filepath.Join(dir, "package.json"))
			if name, ok := data["name"].(string); ok && name != "" {
				m[name] = dir
			}
		}
	}

	wsCache[root] = m

	return m
}

var (
	jsExportConditionPriority  = []string{"source", "import", "module", "svelte", "require", "default", "types"}
	jsPlatformExportConditions = map[string][]string{"native": {"react-native"}}
	jsPlatformSuffixes         = []string{"web", "native", "ios", "android"}
	jsPlatformPathHints        = []struct {
		platform string
		hints    []string
	}{
		{"native", []string{"native", "mobile", "ios", "android", "react-native"}},
		{"web", []string{"web", "browser", "desktop", "electron"}},
	}
)

func jsImporterPlatform(startDir string) string {
	parts := strings.Split(filepath.ToSlash(startDir), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		seg := strings.ToLower(parts[i])
		for _, h := range jsPlatformPathHints {
			for _, x := range h.hints {
				if seg == x {
					return h.platform
				}
			}
		}
	}

	return ""
}

func jsPlatformVariants(cand, platform string) []string {
	var order []string

	for _, p := range jsPlatformSuffixes {
		if p == platform {
			order = append(order, p)
		}
	}

	for _, p := range jsPlatformSuffixes {
		if p != platform {
			order = append(order, p)
		}
	}

	ext := filepath.Ext(cand)
	stem := strings.TrimSuffix(filepath.Base(cand), ext)
	out := make([]string, 0, len(order))

	for _, p := range order {
		out = append(out, filepath.Join(filepath.Dir(cand), stem+"."+p+ext))
	}

	return out
}

func jsExportTargets(v any, platform string) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case map[string]any:
		var out []string

		conds := append(append([]string{}, jsPlatformExportConditions[platform]...), jsExportConditionPriority...)
		for _, c := range conds {
			switch sub := t[c].(type) {
			case string, map[string]any:
				out = append(out, jsExportTargets(sub, platform)...)
			}
		}

		return out
	}

	return nil
}

func jsContainedIn(p, dir string) bool {
	rp, rd := jsResolvedPath(p), jsResolvedPath(dir)
	rel, err := filepath.Rel(rd, rp)

	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

func jsExportsCandidates(dir string, targets []string, platform string) []string {
	var out []string

	for _, t := range targets {
		c := filepath.Join(dir, t)
		if !jsContainedIn(c, dir) {
			continue
		}

		out = append(out, c)
		out = append(out, jsPlatformVariants(c, platform)...)
	}

	return out
}

// jsPackageEntryCandidates mirrors Graphify's _package_entry_candidates.
func jsPackageEntryCandidates(dir, sub, platform string) []string {
	data := readJSONConfig(filepath.Join(dir, "package.json"))

	if sub != "" {
		if exports, ok := data["exports"].(map[string]any); ok {
			key := "./" + sub
			if targets := jsExportTargets(exports[key], platform); len(targets) > 0 {
				if c := jsExportsCandidates(dir, targets, platform); len(c) > 0 {
					return c
				}
			} else {
				keys := make([]string, 0, len(exports))
				for k := range exports {
					keys = append(keys, k)
				}

				sort.Strings(keys)

				for _, pattern := range keys {
					if strings.Count(pattern, "*") != 1 {
						continue
					}

					prefix, suffix, _ := strings.Cut(pattern, "*")
					if !strings.HasPrefix(key, prefix) || (suffix != "" && !strings.HasSuffix(key, suffix)) {
						continue
					}

					matched := key[len(prefix) : len(key)-len(suffix)]

					var wild []string

					for _, r := range jsExportTargets(exports[pattern], platform) {
						if strings.Contains(r, "*") {
							wild = append(wild, strings.ReplaceAll(r, "*", matched))
						}
					}

					if len(wild) > 0 {
						if c := jsExportsCandidates(dir, wild, platform); len(c) > 0 {
							return c
						}
					}
				}
			}
		}

		return []string{filepath.Join(dir, sub)}
	}

	switch exports := data["exports"].(type) {
	case string:
		return []string{filepath.Join(dir, exports)}
	case map[string]any:
		if targets := jsExportTargets(exports["."], platform); len(targets) > 0 {
			return jsExportsCandidates(dir, targets, platform)
		}
	}

	var out []string

	for _, k := range []string{"svelte", "module", "main", "types"} {
		if s, ok := data[k].(string); ok {
			out = append(out, filepath.Join(dir, s))
		}
	}

	return append(out, filepath.Join(dir, "src", "index"), filepath.Join(dir, "index"))
}

// resolveWorkspaceImport mirrors Graphify's _resolve_workspace_import.
func resolveWorkspaceImport(raw, start string) string {
	pkgs := loadWorkspacePackages(start)
	if len(pkgs) == 0 {
		return ""
	}

	platform := jsImporterPlatform(start)

	names := make([]string, 0, len(pkgs))
	for n := range pkgs {
		names = append(names, n)
	}

	sort.Strings(names)

	for _, name := range names {
		dir := pkgs[name]

		sub := ""

		switch {
		case raw == name:
		case strings.HasPrefix(raw, name+"/"):
			sub = raw[len(name)+1:]
		default:
			continue
		}

		for _, c := range jsPackageEntryCandidates(dir, sub, platform) {
			if r := resolveJSImportPath(c); isFile(r) {
				return r
			}
		}
	}

	return ""
}
