package langs

import (
	"strings"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// Adapted from Graphify's graphify/extractors/elixir.py (Apache-2.0).

var (
	exImportKeywords = base.NewSet("alias", "import", "require", "use")
	exSkipKeywords   = base.NewSet("def", "defp", "defmodule", "defmacro", "defmacrop",
		"defstruct", "defprotocol", "defimpl", "defguard",
		"alias", "import", "require", "use",
		"if", "unless", "case", "cond", "with", "for")
)

func exAliasText(n *tsx.Node) string {
	if n == nil {
		return ""
	}

	if c := n.ChildOfType("alias"); c != nil {
		return c.Text()
	}

	return ""
}

func exAliasModules(n *tsx.Node) []string {
	for _, c := range n.Children() {
		switch c.Type() {
		case "alias":
			return []string{c.Text()}
		case "dot":
			base := ""

			var tup *tsx.Node

			for _, s := range c.Children() {
				if s.Type() == "alias" && base == "" {
					base = s.Text()
				} else if s.Type() == "tuple" {
					tup = s
				}
			}

			if base != "" && tup != nil {
				var out []string

				for _, m := range tup.Children() {
					if m.Type() == "alias" {
						out = append(out, base+"."+m.Text())
					}
				}

				if len(out) > 0 {
					return out
				}
			}

			return []string{c.Text()}
		}
	}

	return nil
}

func exDefimplTarget(n *tsx.Node) string {
	for _, c := range n.Children() {
		if c.Type() != "keywords" {
			continue
		}

		for _, p := range c.Children() {
			if p.Type() != "pair" {
				continue
			}

			kw, val := "", ""

			for _, s := range p.Children() {
				switch s.Type() {
				case "keyword":
					kw = s.Text()
				case "alias":
					val = s.Text()
				}
			}

			if strings.TrimSpace(strings.TrimRight(kw, ": ")) == "for" && val != "" {
				return val
			}
		}
	}

	return ""
}

// ExtractElixir extracts an Elixir file.
func ExtractElixir(path, _ string, src []byte) *model.Extraction {
	tree, err := tsx.Parse("elixir", src)
	if err != nil {
		return &model.Extraction{Error: err.Error()}
	}
	defer tree.Release()

	b := base.NewBuilder(path)
	b.AddFileNode()

	var bodies []rustBody

	var walk func(n *tsx.Node, parent string)
	walk = func(n *tsx.Node, parent string) {
		if n.Type() != "call" {
			for _, c := range n.Children() {
				walk(c, parent)
			}

			return
		}

		ident, args, do := n.ChildOfType("identifier"), n.ChildOfType("arguments"), n.ChildOfType("do_block")
		if ident == nil {
			for _, c := range n.Children() {
				walk(c, parent)
			}

			return
		}

		kw, line := ident.Text(), n.Line()

		walkDo := func(owner string) {
			if do != nil {
				for _, c := range do.Children() {
					walk(c, owner)
				}
			}
		}

		switch kw {
		case "defmodule", "defprotocol":
			name := exAliasText(args)
			if name == "" {
				return
			}

			id := ids.MakeID(b.Stem, name)
			if mn := b.AddNode(id, name, line); parent == "" {
				mn.TopModule = true
			}

			if kw == "defmodule" {
				b.AddEdge(b.FileID, id, "contains", line)
			} else {
				owner := parent
				if owner == "" {
					owner = b.FileID
				}

				b.AddEdge(owner, id, "contains", line)
			}

			walkDo(id)

			return
		case "defimpl":
			proto := exAliasText(args)
			if proto == "" {
				return
			}

			target := exDefimplTarget(args)
			id := ids.MakeID(b.Stem, "defimpl", proto, target)

			label := proto
			if target != "" {
				label = proto + " (for " + target + ")"
			}

			b.AddNode(id, label, line)

			owner := parent
			if owner == "" {
				owner = b.FileID
			}

			b.AddEdge(owner, id, "contains", line)
			b.AddEdge(id, ids.MakeID(b.Stem, proto), "implements", line)
			walkDo(id)

			return
		case "def", "defp":
			fn := ""

			if args != nil {
				for _, c := range args.Children() {
					for c.Type() == "binary_operator" {
						head := c.ChildOfType("call", "identifier", "binary_operator")
						if head == nil {
							break
						}

						c = head
					}

					if c.Type() == "call" {
						if id := c.ChildOfType("identifier"); id != nil {
							fn = id.Text()
						}
					} else if c.Type() == "identifier" {
						fn = c.Text()

						break
					}
				}
			}

			if fn == "" {
				return
			}

			container := parent
			if container == "" {
				container = b.FileID
			}

			fid := ids.MakeID(container, fn)
			b.AddNode(fid, fn+"()", line)

			if parent != "" {
				b.AddEdge(parent, fid, "method", line)
			} else {
				b.AddEdge(b.FileID, fid, "contains", line)
			}

			if do != nil {
				bodies = append(bodies, rustBody{id: fid, node: do})
			}

			return
		}

		if exImportKeywords.Has(kw) && args != nil {
			for _, m := range exAliasModules(args) {
				b.AddEdgeCtx(b.FileID, ids.MakeID(m), "imports", line, "import")
			}

			return
		}

		for _, c := range n.Children() {
			walk(c, parent)
		}
	}

	walk(tree.Root, "")

	labelToID := map[string]string{}
	for _, n := range b.Nodes {
		labelToID[strings.TrimLeft(strings.Trim(n.Label, "()"), ".")] = n.ID
	}

	seen := map[[2]string]bool{}

	var wc func(n *tsx.Node, caller string)
	wc = func(n *tsx.Node, caller string) {
		if n.Type() != "call" {
			for _, c := range n.Children() {
				wc(c, caller)
			}

			return
		}

		if id := n.ChildOfType("identifier"); id != nil && exSkipKeywords.Has(id.Text()) {
			for _, c := range n.Children() {
				wc(c, caller)
			}

			return
		}

		callee, member := "", false

		for _, c := range n.Children() {
			if c.Type() == "dot" {
				member = true
				parts := strings.Split(strings.TrimRight(c.Text(), "."), ".")
				callee = parts[len(parts)-1]

				break
			}

			if c.Type() == "identifier" {
				callee = c.Text()

				break
			}
		}

		if callee != "" && !base.BuiltinGlobals.Has(callee) {
			if tgt := labelToID[callee]; tgt != "" && tgt != caller {
				pair := [2]string{caller, tgt}
				if !seen[pair] {
					seen[pair] = true
					b.AddEdgeCtx(caller, tgt, "calls", n.Line(), "call")
				}
			} else {
				b.RawCalls = append(b.RawCalls, &model.RawCall{
					CallerID: caller, Callee: callee, IsMemberCall: member, Language: "elixir",
					SourceFile: path, SourceLocation: base.Loc(n.Line()),
				})
			}
		}

		for _, c := range n.Children() {
			wc(c, caller)
		}
	}

	for _, bd := range bodies {
		wc(bd.node, bd.id)
	}

	res := b.Result()

	// Graphify drops non-import edges to unknown targets but keeps imports.
	return res
}

// resolveElixirImportTargets rewrites alias/import/require/use edges whose
// bare module id matches exactly one top-level module in another file.
//
// Adapted from Graphify's _resolve_elixir_import_targets (Apache-2.0).
func resolveElixirImportTargets(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, _ []extract.FileResult) {
	byID := map[string]*model.Node{}
	mods := map[string][]string{}

	for _, n := range *nodesP {
		byID[n.ID] = n
		if n.TopModule && n.Label != "" {
			k := ids.MakeID(n.Label)
			mods[k] = append(mods[k], n.ID)
		}
	}

	if len(mods) == 0 {
		return
	}

	for _, e := range *edgesP {
		isElixir := strings.HasSuffix(e.SourceFile, ".ex") || strings.HasSuffix(e.SourceFile, ".exs")
		if e.Relation != "imports" || e.Context != "import" || !isElixir {
			continue
		}

		if _, ok := byID[e.Target]; ok {
			continue
		}

		c := mods[e.Target]
		if len(c) != 1 {
			continue
		}

		if t := byID[c[0]]; t != nil && t.SourceFile == e.SourceFile {
			continue
		}

		e.Target = c[0]
	}
}
