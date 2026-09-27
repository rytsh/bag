package langs

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// Adapted from Graphify's graphify/extractors/markdown.py (Apache-2.0).

var (
	mdInlineLink  = regexp.MustCompile(`(?:^|[^!])\[[^\]]*\]\(\s*<?([^)\s>]+)>?(?:\s+[^)]*)?\)`)
	mdRefDef      = regexp.MustCompile(`^\s{0,3}\[[^\]]+\]:\s*<?([^\s>]+)>?`)
	mdWikilink    = regexp.MustCompile(`(?:^|[^!])\[\[([^\]|#]+)(?:[#|][^\]]*)?\]\]`)
	mdHeading     = regexp.MustCompile(`^(#{1,6})\s+(.+)`)
	mdLinkable    = base.NewSet(".md", ".mdx", ".qmd", ".markdown", ".rst", ".txt")
	mdFrontScalar = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9_\-. ]*):\s*(.*)$`)
)

func mdResolveLink(raw, dir string) string {
	t := strings.TrimSpace(raw)
	t = strings.SplitN(t, "#", 2)[0]
	t = strings.TrimSpace(strings.SplitN(t, "?", 2)[0])

	if t == "" {
		return ""
	}

	low := strings.ToLower(t)
	if strings.Contains(t, "://") || strings.HasPrefix(low, "mailto:") || strings.HasPrefix(low, "tel:") ||
		strings.HasPrefix(low, "//") || strings.HasPrefix(low, "data:") {
		return ""
	}

	ext := strings.ToLower(filepath.Ext(t))
	if ext == "" {
		t += ".md"
		ext = ".md"
	}

	if !mdLinkable.Has(ext) {
		return ""
	}

	if !filepath.IsAbs(t) {
		t = filepath.Join(dir, t)
	}

	return filepath.Clean(t)
}

func mdFrontmatter(lines []string) (map[string]any, int) {
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, 0
	}

	limit := len(lines)
	if limit > 201 {
		limit = 201
	}

	for i := 1; i < limit; i++ {
		if s := strings.TrimSpace(lines[i]); s == "---" || s == "..." {
			fm := map[string]any{}

			for _, l := range lines[1:i] {
				if l == "" || l[0] == ' ' || l[0] == '\t' {
					continue
				}

				m := mdFrontScalar.FindStringSubmatch(strings.TrimSpace(l))
				if m == nil || strings.TrimSpace(m[2]) == "" {
					continue
				}

				fm[strings.TrimSpace(m[1])] = strings.Trim(strings.TrimSpace(m[2]), `"'`)
			}

			return fm, i + 1
		}
	}

	return nil, 0
}

// ExtractMarkdown extracts headings and document links from markdown.
func ExtractMarkdown(path, _ string, src []byte) *model.Extraction {
	b := base.NewBuilder(path)
	lines := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")

	fm, bodyStart := mdFrontmatter(lines)

	fileNode := b.AddFileNode()
	fileNode.FileType = model.FileTypeDocument
	fileNode.Extra = map[string]any{"node_kind": "page"}

	if len(fm) > 0 {
		fileNode.Extra["frontmatter"] = fm
	}

	dir := filepath.Dir(path)
	linked := map[string]bool{}

	addLink := func(raw string, line int) {
		res := mdResolveLink(raw, dir)
		if res == "" {
			return
		}

		id := ids.MakeID(res)
		if id == b.FileID || linked[id] {
			return
		}

		linked[id] = true
		b.AddEdge(b.FileID, id, "references", line)
	}

	type level struct {
		n  int
		id string
	}

	var stack []level

	inCode := false

	for i, l := range lines {
		ln := i + 1

		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			inCode = !inCode

			continue
		}

		if inCode {
			continue
		}

		for _, m := range mdInlineLink.FindAllStringSubmatch(l, -1) {
			addLink(m[1], ln)
		}

		for _, m := range mdWikilink.FindAllStringSubmatch(l, -1) {
			addLink(m[1], ln)
		}

		if m := mdRefDef.FindStringSubmatch(l); m != nil {
			addLink(m[1], ln)
		}

		if i < bodyStart {
			continue
		}

		m := mdHeading.FindStringSubmatch(l)
		if m == nil {
			continue
		}

		lvl, title := len(m[1]), strings.TrimSpace(m[2])

		hid := ids.MakeID(b.Stem, title)
		if b.Has(hid) {
			hid = ids.MakeID(b.Stem, title, itoa(ln))
		}

		hn := b.AddNode(hid, title, ln)
		hn.FileType = model.FileTypeDocument
		hn.Extra = map[string]any{"node_kind": "heading"}

		for len(stack) > 0 && stack[len(stack)-1].n >= lvl {
			stack = stack[:len(stack)-1]
		}

		parent := b.FileID
		if len(stack) > 0 {
			parent = stack[len(stack)-1].id
		}

		b.AddEdge(parent, hid, "contains", ln)
		stack = append(stack, level{lvl, hid})
	}

	return b.ResultUnfiltered()
}
