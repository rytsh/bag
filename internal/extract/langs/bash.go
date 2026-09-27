package langs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// Adapted from Graphify's graphify/extractors/bash.py (Apache-2.0).

var (
	bashSourceCommands = base.NewSet("source", ".")
	bashScriptRunners  = base.NewSet("bash", "sh", "zsh", "ksh", "dash")
	bashLeadingExp     = regexp.MustCompile(`^(?:(?:\$\{[^}]*\}|\$[A-Za-z_][A-Za-z0-9_]*)/?)+`)
	bashLeadingVar     = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)[^}]*\}|^\$([A-Za-z_][A-Za-z0-9_]*)`)
	bashDirnameIdiom   = regexp.MustCompile(`dirname[^)]*\)((?:/\.\.)*)`)
	bashSourceDirname  = regexp.MustCompile(`^\$\(dirname\s+"\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?"\)`)
)

func bashSourceSuffix(raw string, allowDotDot bool) string {
	s := strings.TrimLeft(bashLeadingExp.ReplaceAllString(raw, ""), "/")
	if s == "" || strings.Contains(s, "$") {
		return ""
	}

	if !allowDotDot {
		for _, seg := range strings.Split(s, "/") {
			if seg == ".." {
				return ""
			}
		}
	}

	return s
}

func withinTree(ceiling, target string) bool {
	c, t := filepath.Clean(ceiling), filepath.Clean(target)

	return t == c || strings.HasPrefix(t, c+string(os.PathSeparator))
}

func bashAssignmentBase(value, scriptDir string) string {
	v := strings.Trim(strings.TrimSpace(value), `'"`)
	if v == "" {
		return ""
	}

	if strings.Contains(v, "dirname") && (strings.Contains(v, "BASH_SOURCE") || strings.Contains(v, "$0")) {
		b := scriptDir

		if m := bashDirnameIdiom.FindStringSubmatch(v); m != nil {
			for range strings.Count(m[1], "..") {
				b = filepath.Dir(b)
			}
		}

		return b
	}

	if strings.ContainsAny(v, "$`") {
		return ""
	}

	if filepath.IsAbs(v) {
		return v
	}

	return filepath.Join(scriptDir, v)
}

func absClean(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}

	if r, err := filepath.EvalSymlinks(a); err == nil {
		return r
	}

	return a
}

// ExtractBash extracts a shell script.
func ExtractBash(path, _ string, src []byte) *model.Extraction {
	tree, err := tsx.Parse("bash", src)
	if err != nil {
		return &model.Extraction{Error: err.Error()}
	}
	defer tree.Release()

	b := base.NewBuilder(path)
	name := filepath.Base(path)
	dir := filepath.Dir(path)

	meta := func(kind string) map[string]any { return map[string]any{"language": "bash", "kind": kind} }

	addNode := func(id, label string, line int, kind string) {
		if id != "" && !b.Has(id) {
			b.AddNode(id, label, line).Metadata = meta(kind)
		}
	}

	addEdge := func(src, tgt, rel string, line int, conf, ctx string) *model.Edge {
		if src == "" || tgt == "" || src == tgt {
			return nil
		}

		e := b.AddEdgeCtx(src, tgt, rel, line, ctx)
		if conf != "" {
			e.Confidence = conf
		}

		return e
	}

	fileID := b.FileID
	entryID := fileID + "__entry"

	addNode(fileID, name, 1, "file")
	addNode(entryID, name+" script", 1, "bash_entrypoint")
	addEdge(fileID, entryID, "contains", 1, "", "")

	literal := func(n *tsx.Node) string {
		raw := strings.TrimSpace(n.Text())
		if raw == "" {
			return ""
		}

		if (raw[0] == '\'' || raw[0] == '"') && len(raw) >= 2 && raw[len(raw)-1] == raw[0] {
			raw = raw[1 : len(raw)-1]
		}

		for _, tok := range []string{"$", "`", "$(", "<(", ">", "|", ";", "&"} {
			if strings.Contains(raw, tok) {
				return ""
			}
		}

		return raw
	}

	funcName := func(n *tsx.Node) string {
		if w := n.ChildOfType("word"); w != nil {
			return literal(w)
		}

		return ""
	}

	defined := map[string]bool{}

	tree.Root.Walk(func(n *tsx.Node) bool {
		if n.Type() == "function_definition" {
			if nm := funcName(n); nm != "" {
				defined[nm] = true
			}
		}

		return true
	})

	varBases := map[string]string{}

	for _, a := range tree.Root.Children() {
		if a.Type() != "variable_assignment" {
			continue
		}

		nn, vn := a.Field("name"), a.Field("value")
		if nn == nil || vn == nil {
			continue
		}

		if bb := bashAssignmentBase(vn.Text(), dir); bb != "" {
			varBases[strings.TrimSpace(nn.Text())] = bb
		}
	}

	insideExpansion := func(n *tsx.Node) bool {
		for p := n.Parent(); p != nil; p = p.Parent() {
			if p.Type() == "command_substitution" || p.Type() == "process_substitution" {
				return true
			}
		}

		return false
	}

	callAllowed := func(n *tsx.Node) bool {
		saw := false

		for p := n.Parent(); p != nil; p = p.Parent() {
			switch p.Type() {
			case "process_substitution":
				return false
			case "command_substitution":
				saw = true
			case "variable_assignment":
				if saw {
					return true
				}
			}
		}

		return !saw
	}

	cmdName := func(n *tsx.Node) *tsx.Node {
		if c := n.Field("name"); c != nil {
			return c
		}

		if ch := n.Children(); len(ch) > 0 {
			return ch[0]
		}

		return nil
	}

	importFrom := func(target string, line int, conf string) {
		addEdge(fileID, ids.MakeID(target), "imports_from", line, conf, "import")
	}

	var funcBodies []rustBody

	var walk func(n *tsx.Node, parent string)
	walk = func(n *tsx.Node, parent string) {
		switch n.Type() {
		case "function_definition":
			nm := funcName(n)
			if nm == "" {
				return
			}

			fid := ids.MakeID(b.Stem, nm)
			addNode(fid, nm+"()", n.Line(), "bash_function")
			addEdge(parent, fid, "defines", n.Line(), "", "")

			bd := n.ChildOfType("compound_statement")
			funcBodies = append(funcBodies, rustBody{fid, bd})

			if bd != nil {
				walk(bd, fid)
			}

			return
		case "command":
			if insideExpansion(n) {
				return
			}

			cn := cmdName(n)
			if cn == nil {
				return
			}

			cmd := literal(cn)

			var args []*tsx.Node

			for _, c := range n.Children() {
				if (c.Type() == "word" || c.Type() == "string" || c.Type() == "concatenation") && !c.Same(cn) {
					args = append(args, c)
				}
			}

			line := n.Line()

			if bashSourceCommands.Has(cmd) && !defined[cmd] {
				if len(args) == 0 {
					return
				}

				raw := strings.Trim(strings.TrimSpace(args[0].Text()), `'"`)

				switch {
				case strings.HasPrefix(raw, ".") || strings.HasPrefix(raw, "/"):
					res := absClean(filepath.Join(dir, raw))
					if _, err := os.Stat(res); err == nil {
						importFrom(res, line, "")
					}
				case strings.Contains(raw, "$"):
					if m := bashSourceDirname.FindStringSubmatchIndex(raw); m != nil {
						vn := raw[m[2]:m[3]]
						bb := filepath.Dir(dir)

						if v, ok := varBases[vn]; ok {
							bb = filepath.Dir(v)
						}

						suf := strings.TrimLeft(raw[m[1]:], "/")
						if suf != "" && !strings.Contains(suf, "$") && !strings.Contains("/"+suf+"/", "/../") {
							if res := absClean(filepath.Join(bb, suf)); isFile(res) {
								importFrom(res, line, model.Inferred)
							}
						}

						return
					}

					vm := bashLeadingVar.FindStringSubmatch(raw)
					vn := ""

					if vm != nil {
						vn = vm[1]
						if vn == "" {
							vn = vm[2]
						}
					}

					_, tracked := varBases[vn]

					suf := bashSourceSuffix(raw, tracked)
					if suf == "" {
						return
					}

					var res, ceiling string

					if tracked {
						res = filepath.Clean(filepath.Join(varBases[vn], suf))
						ceiling = filepath.Dir(varBases[vn])
					} else {
						res = absClean(filepath.Join(dir, suf))
						ceiling = dir
					}

					if withinTree(ceiling, res) && isFile(res) {
						importFrom(res, line, model.Inferred)
					}
				default:
					if cand := filepath.Join(dir, raw); raw != "" && isFile(cand) {
						importFrom(absClean(cand), line, model.Inferred)
					} else if id := ids.MakeID(raw); id != "" {
						addEdge(fileID, id, "imports", line, "", "import")
					}
				}

				return
			}

			if defined[cmd] {
				return
			}

			raw := ""
			if strings.HasSuffix(cmd, ".sh") {
				raw = cmd
			}

			if bashScriptRunners.Has(cmd) && len(args) > 0 {
				raw = literal(args[0])
			}

			if raw == "" {
				cand := strings.Trim(strings.TrimSpace(cn.Text()), `'"`)
				if strings.HasSuffix(cand, ".sh") && strings.Contains(cand, "$") {
					raw = bashSourceSuffix(cand, false)
				}
			}

			if strings.HasSuffix(raw, ".sh") {
				res := absClean(filepath.Join(dir, raw))
				if isFile(res) {
					caller := parent
					if parent == fileID {
						caller = entryID
					}

					addEdge(caller, ids.MakeID(res)+"__entry", "calls", line, "", "script_invocation")
				}
			}

			return
		case "declaration_command":
			if p := n.Parent(); p != nil && p.Type() == "program" {
				for _, c := range n.Children() {
					if c.Type() != "variable_assignment" {
						continue
					}

					if vn := c.Field("name"); vn != nil {
						if v := strings.TrimSpace(vn.Text()); v != "" {
							vid := ids.MakeID(b.Stem, v)
							if !b.Has(vid) {
								b.AddNode(vid, v, c.Line())
							}

							addEdge(fileID, vid, "defines", c.Line(), "", "")
						}
					}
				}
			}

			return
		}

		for _, c := range n.Children() {
			walk(c, parent)
		}
	}

	walk(tree.Root, fileID)

	rawSeen := map[[2]string]bool{}

	var wc func(n *tsx.Node, fid string, seen map[[2]string]bool)
	wc = func(n *tsx.Node, fid string, seen map[[2]string]bool) {
		if n == nil {
			return
		}

		for _, c := range n.Children() {
			if c.Type() == "function_definition" {
				continue
			}

			if c.Type() == "command" && callAllowed(c) {
				if cn := cmdName(c); cn != nil {
					nm := literal(cn)

					switch {
					case nm != "" && defined[nm]:
						tgt := ids.MakeID(b.Stem, nm)
						k := [2]string{fid, tgt}

						if !seen[k] {
							seen[k] = true
							addEdge(fid, tgt, "calls", c.Line(), "", "call")
						}
					case nm != "" && !bashSourceCommands.Has(nm) && !bashScriptRunners.Has(nm):
						k := [2]string{fid, nm}
						if !rawSeen[k] {
							rawSeen[k] = true
							b.RawCalls = append(b.RawCalls, &model.RawCall{
								Language: "bash", Callee: nm, CallerID: fid,
								SourceFile: path, SourceLocation: base.Loc(c.Line()),
							})
						}
					}
				}
			}

			wc(c, fid, seen)
		}
	}

	wc(tree.Root, entryID, map[[2]string]bool{})

	for _, fb := range funcBodies {
		wc(fb.node, fb.id, map[[2]string]bool{})
	}

	return b.ResultUnfiltered()
}
