package export

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/rytsh/bag/internal/graph"
	"github.com/rytsh/bag/internal/model"
)

var obsidianUnsafe = regexp.MustCompile(`[\\/*?:"<>|#^\[\]]`)

func obsidianName(label string) string {
	s := strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(label)
	s = strings.TrimSpace(obsidianUnsafe.ReplaceAllString(s, ""))

	low := strings.ToLower(s)
	for _, ext := range []string{".md", ".mdx", ".markdown"} {
		if strings.HasSuffix(low, ext) {
			s = s[:len(s)-len(ext)]

			break
		}
	}

	if s == "" {
		s = "unnamed"
	}

	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}

	return s
}

func obsidianTag(s string) string {
	var b strings.Builder

	for _, r := range s {
		switch {
		case r == ' ' || r == '-' || r == '.':
			b.WriteByte('_')
		case r == '_' || r == '/' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 127:
			b.WriteRune(r)
		}
	}

	return b.String()
}

func yamlStr(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

// ToObsidian writes an Obsidian vault: one note per node with wikilinks,
// plus one hub note per community.
func ToObsidian(g *graph.Graph, c graph.Communities, labels map[int]string, outDir string) (int, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return 0, err
	}

	nc := c.NodeCommunity()
	label := func(cid int) string {
		if l, ok := labels[cid]; ok {
			return l
		}

		return fmt.Sprintf("Community %d", cid)
	}

	names := map[string]string{}
	used := map[string]int{}

	ids := g.NodeIDs()
	sort.Strings(ids)

	for _, id := range ids {
		base := obsidianName(g.Node(id).Label)
		key := strings.ToLower(base)
		used[key]++

		if used[key] > 1 {
			base = fmt.Sprintf("%s (%d)", base, used[key])
		}

		names[id] = base
	}

	count := 0

	for _, id := range ids {
		n := g.Node(id)

		var L []string

		L = append(L, "---",
			"id: "+yamlStr(n.ID),
			"label: "+yamlStr(n.Label),
			"source_file: "+yamlStr(n.SourceFile),
			"source_location: "+yamlStr(n.SourceLocation),
			"file_type: "+yamlStr(n.FileType))

		if cid, ok := nc[id]; ok {
			L = append(L, fmt.Sprintf("community: %d", cid), "community_name: "+yamlStr(label(cid)),
				"tags:", "  - community/"+obsidianTag(label(cid)))
		}

		L = append(L, "---", "", "# "+n.Label, "")

		if n.SourceFile != "" {
			L = append(L, fmt.Sprintf("Source: `%s` %s", n.SourceFile, n.SourceLocation), "")
		}

		if cid, ok := nc[id]; ok {
			L = append(L, fmt.Sprintf("Community: [[_COMMUNITY_%s|%s]]", obsidianName(label(cid)), label(cid)), "")
		}

		byRel := map[string][]string{}

		for _, nb := range g.Neighbors(id) {
			e := g.Edge(id, nb)

			conf := e.Confidence
			if conf == "" {
				conf = model.Extracted
			}

			dir := "→"
			if e.Target == id && e.Source != id {
				dir = "←"
			}

			byRel[e.Relation] = append(byRel[e.Relation], fmt.Sprintf("- %s [[%s]] `%s`", dir, names[nb], conf))
		}

		if len(byRel) > 0 {
			L = append(L, "## Connections", "")

			rels := make([]string, 0, len(byRel))
			for r := range byRel {
				rels = append(rels, r)
			}

			sort.Strings(rels)

			for _, r := range rels {
				L = append(L, "### "+r)
				L = append(L, byRel[r]...)
				L = append(L, "")
			}
		}

		if err := graph.WriteFileAtomic(filepath.Join(outDir, names[id]+".md"), []byte(strings.Join(L, "\n"))); err != nil {
			return count, err
		}

		count++
	}

	for cid, ms := range c {
		var L []string

		L = append(L, "# "+label(cid), "", fmt.Sprintf("%d members", len(ms)), "")

		sorted := append([]string(nil), ms...)
		sort.SliceStable(sorted, func(i, j int) bool { return g.Degree(sorted[i]) > g.Degree(sorted[j]) })

		for _, m := range sorted {
			if nm, ok := names[m]; ok {
				L = append(L, "- [["+nm+"]]")
			}
		}

		f := filepath.Join(outDir, "_COMMUNITY_"+obsidianName(label(cid))+".md")
		if err := graph.WriteFileAtomic(f, []byte(strings.Join(L, "\n"))); err != nil {
			return count, err
		}
	}

	return count, nil
}
