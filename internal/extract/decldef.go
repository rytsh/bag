package extract

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/rytsh/bag/internal/model"
)

var (
	declDefHeaderSuffixes = map[string]bool{".h": true, ".hpp": true, ".hh": true, ".hxx": true}
	declDefImplSuffixes   = map[string]bool{".m": true, ".mm": true, ".cpp": true, ".cc": true, ".cxx": true, ".c": true}
)

// declDefStem returns the (dir, base stem) sibling key of a header/impl
// file; ObjC categories (Foo+Cat.m) pair with Foo.
func declDefStem(sf string) (string, bool) {
	if sf == "" {
		return "", false
	}

	ext := strings.ToLower(filepath.Ext(sf))
	if !declDefHeaderSuffixes[ext] && !declDefImplSuffixes[ext] {
		return "", false
	}

	stem, _, _ := strings.Cut(strings.TrimSuffix(filepath.Base(sf), filepath.Ext(sf)), "+")
	if stem == "" {
		return "", false
	}

	return filepath.Dir(sf) + "\x00" + stem, true
}

func sourceStem(n *model.Node) string {
	b := filepath.Base(n.SourceFile)

	return strings.TrimSuffix(b, filepath.Ext(b))
}

// mergeDeclDefClasses collapses a class (and its members) declared in a
// header and defined in a sibling impl file into the header node, keeping
// the impl location as definition_file/definition_location.
//
// Adapted from Graphify's _merge_decl_def_classes (Apache-2.0).
func (c *corpus) mergeDeclDefClasses() {
	byID := map[string][]*model.Node{}

	for _, n := range c.nodes {
		if n.FileType != model.FileTypeCode || n.ID == "" || n.SourceFile == "" {
			continue
		}

		byID[n.ID] = append(byID[n.ID], n)
	}

	drop := map[*model.Node]bool{}

	for _, group := range byID {
		if len(group) < 2 {
			continue
		}

		keys := map[string]bool{}
		var headers []*model.Node
		ok := true

		for _, n := range group {
			k, good := declDefStem(n.SourceFile)
			if !good {
				ok = false

				break
			}

			keys[k] = true
			if declDefHeaderSuffixes[strings.ToLower(filepath.Ext(n.SourceFile))] {
				headers = append(headers, n)
			}
		}

		if !ok || len(keys) != 1 || len(headers) == 0 {
			continue
		}

		var keeper *model.Node

		if len(headers) == 1 {
			keeper = headers[0]
		} else {
			var baseHeaders []*model.Node
			for _, h := range headers {
				if !strings.Contains(sourceStem(h), "+") {
					baseHeaders = append(baseHeaders, h)
				}
			}

			switch {
			case len(baseHeaders) > 1:
				continue
			case len(baseHeaders) == 1:
				keeper = baseHeaders[0]
			default:
				keeper = headers[0]
				for _, h := range headers[1:] {
					if sourceStem(h) < sourceStem(keeper) {
						keeper = h
					}
				}
			}
		}

		var impls []*model.Node
		for _, n := range group {
			if n != keeper && declDefImplSuffixes[strings.ToLower(filepath.Ext(n.SourceFile))] {
				impls = append(impls, n)
			}
		}

		sort.SliceStable(impls, func(i, j int) bool {
			if impls[i].SourceFile != impls[j].SourceFile {
				return impls[i].SourceFile < impls[j].SourceFile
			}

			return impls[i].SourceLocation < impls[j].SourceLocation
		})

		if len(impls) > 0 {
			if keeper.Extra == nil {
				keeper.Extra = map[string]any{}
			}

			keeper.Extra["definition_file"] = impls[0].SourceFile
			if impls[0].SourceLocation != "" {
				keeper.Extra["definition_location"] = impls[0].SourceLocation
			}
		}

		for _, n := range group {
			if n != keeper {
				drop[n] = true
			}
		}
	}

	if len(drop) == 0 {
		return
	}

	nodes := c.nodes[:0]
	for _, n := range c.nodes {
		if !drop[n] {
			nodes = append(nodes, n)
		}
	}

	c.nodes = nodes

	type ekey struct{ src, tgt, rel, ctx string }

	seen := map[ekey]bool{}
	edges := c.edges[:0]

	for _, e := range c.edges {
		if e.Source == e.Target {
			continue
		}

		k := ekey{e.Source, e.Target, e.Relation, e.Context}
		if seen[k] {
			continue
		}

		seen[k] = true
		edges = append(edges, e)
	}

	c.edges = edges
}
