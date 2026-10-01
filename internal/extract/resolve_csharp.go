package extract

import "github.com/rytsh/bag/internal/model"

// canonicalizeCSharpNamespaces collapses duplicate namespace nodes (one per
// file) onto a single canonical node per label.
func (c *corpus) canonicalizeCSharpNamespaces() {
	byLabel := map[string][]*model.Node{}

	var order []string

	for _, n := range c.nodes {
		if n.Type != "namespace" {
			continue
		}

		if _, ok := byLabel[n.Label]; !ok {
			order = append(order, n.Label)
		}

		byLabel[n.Label] = append(byLabel[n.Label], n)
	}

	remap := map[string]string{}
	drop := map[*model.Node]bool{}

	for _, l := range order {
		g := byLabel[l]
		if len(g) < 2 {
			continue
		}

		canon := g[0]
		for _, n := range g[1:] {
			if n.SourceFile < canon.SourceFile ||
				(n.SourceFile == canon.SourceFile && n.SourceLocation < canon.SourceLocation) ||
				(n.SourceFile == canon.SourceFile && n.SourceLocation == canon.SourceLocation && n.ID < canon.ID) {
				canon = n
			}
		}

		for _, n := range g {
			if n != canon {
				drop[n] = true
				remap[n.ID] = canon.ID
			}
		}
	}

	if len(drop) == 0 {
		return
	}

	for _, e := range c.edges {
		if v, ok := remap[e.Source]; ok {
			e.Source = v
		}

		if v, ok := remap[e.Target]; ok {
			e.Target = v
		}
	}

	kept := c.nodes[:0]

	for _, n := range c.nodes {
		if !drop[n] {
			kept = append(kept, n)
		}
	}

	c.nodes = kept
}
