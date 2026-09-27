package langs

import (
	"sort"
	"strings"
	"sync"

	ts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// The tags extractor gives broad, shallow coverage for every grammar that
// ships a tree-sitter tags query (definitions + references). Dedicated
// extractors always take precedence.

var (
	taggerMu sync.Mutex
	taggers  = map[string]*ts.Tagger{}
)

func taggerFor(entry *grammars.LangEntry) *ts.Tagger {
	taggerMu.Lock()
	defer taggerMu.Unlock()

	if t, ok := taggers[entry.Name]; ok {
		return t
	}

	q := grammars.ResolveTagsQuery(*entry)
	if strings.TrimSpace(q) == "" {
		taggers[entry.Name] = nil

		return nil
	}

	lang := entry.Language()
	if lang == nil {
		taggers[entry.Name] = nil

		return nil
	}

	t, err := ts.NewTagger(lang, q)
	if err != nil {
		taggers[entry.Name] = nil

		return nil
	}

	taggers[entry.Name] = t

	return t
}

var classKinds = base.NewSet("class", "interface", "struct", "module", "trait", "enum", "type", "object",
	"namespace", "protocol", "record", "union", "impl", "implementation", "mixin", "extension", "contract", "schema")

type tagDef struct {
	id, name, kind string
	start, end     uint32
	line           int
	isClass        bool
	parent         string
}

func makeTagsExtractor(entry grammars.LangEntry) extract.Extractor {
	lang := entry.Name

	return func(path, _ string, src []byte) *model.Extraction {
		e := grammars.DetectLanguageByName(lang)
		if e == nil {
			return &model.Extraction{Error: "grammar not found"}
		}

		tg := taggerFor(e)
		if tg == nil {
			return &model.Extraction{Skipped: true}
		}

		tags := tg.Tag(src)

		b := base.NewBuilder(path)
		b.AddFileNode()

		var defs []*tagDef

		for _, t := range tags {
			if !strings.HasPrefix(t.Kind, "definition.") || strings.TrimSpace(t.Name) == "" {
				continue
			}

			kind := strings.TrimPrefix(t.Kind, "definition.")
			defs = append(defs, &tagDef{
				name: strings.TrimSpace(t.Name), kind: kind,
				start: t.Range.StartByte, end: t.Range.EndByte,
				line: int(t.Range.StartPoint.Row) + 1, isClass: classKinds.Has(kind),
			})
		}

		sort.SliceStable(defs, func(i, j int) bool {
			if defs[i].start != defs[j].start {
				return defs[i].start < defs[j].start
			}

			return defs[i].end > defs[j].end
		})

		// Innermost enclosing class for each definition.
		var stack []*tagDef

		for _, d := range defs {
			for len(stack) > 0 && stack[len(stack)-1].end <= d.start {
				stack = stack[:len(stack)-1]
			}

			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].isClass && stack[i].end >= d.end && stack[i] != d {
					d.parent = stack[i].id

					break
				}
			}

			switch {
			case d.isClass:
				d.id = ids.MakeID(b.Stem, d.name)
				b.AddNode(d.id, d.name, d.line)

				owner := b.FileID
				if d.parent != "" && d.parent != d.id {
					owner = d.parent
				}

				b.AddEdge(owner, d.id, "contains", d.line)
			case d.parent != "":
				d.id = ids.MakeID(d.parent, d.name)
				label := "." + d.name + "()"

				if !isCallableKind(d.kind) {
					label = d.name
				}

				b.AddNode(d.id, label, d.line)

				rel := "method"
				if !isCallableKind(d.kind) {
					rel = "contains"
				}

				b.AddEdge(d.parent, d.id, rel, d.line)
			default:
				d.id = ids.MakeID(b.Stem, d.name)
				label := d.name + "()"

				if !isCallableKind(d.kind) {
					label = d.name
				}

				b.AddNode(d.id, label, d.line)
				b.AddEdge(b.FileID, d.id, "contains", d.line)
			}

			if n := b.Get(d.id); n != nil {
				n.Callable = isCallableKind(d.kind) || d.isClass
				n.CallableClass = d.isClass
			}

			stack = append(stack, d)
		}

		labelToID := map[string]string{}
		for _, n := range b.Nodes {
			labelToID[strings.TrimLeft(strings.Trim(n.Label, "()"), ".")] = n.ID
		}

		enclosing := func(pos uint32) string {
			best := ""
			bestSpan := uint32(1<<32 - 1)

			for _, d := range defs {
				if !isCallableKind(d.kind) || d.start > pos || d.end < pos {
					continue
				}

				if span := d.end - d.start; span < bestSpan {
					best, bestSpan = d.id, span
				}
			}

			return best
		}

		seen := map[[2]string]bool{}

		for _, t := range tags {
			if !strings.HasPrefix(t.Kind, "reference.") {
				continue
			}

			kind := strings.TrimPrefix(t.Kind, "reference.")
			name := strings.TrimSpace(t.Name)

			if name == "" || base.BuiltinGlobals.Has(name) {
				continue
			}

			line := int(t.Range.StartPoint.Row) + 1

			if kind == "import" || kind == "module" && strings.Contains(t.Kind, "import") {
				b.AddEdgeCtx(b.FileID, ids.MakeID(lastSeg(lastSeg(name, "/"), ".")), "imports", line, "import")

				continue
			}

			caller := enclosing(t.Range.StartByte)
			if caller == "" {
				continue
			}

			if kind != "call" && kind != "send" && kind != "method" && kind != "function" {
				if kind == "class" || kind == "type" || kind == "implementation" || kind == "interface" {
					if tgt := labelToID[name]; tgt != "" && tgt != caller {
						pair := [2]string{caller, tgt}
						if !seen[pair] {
							seen[pair] = true
							b.AddEdgeCtx(caller, tgt, "references", line, "type")
						}
					}
				}

				continue
			}

			if tgt := labelToID[name]; tgt != "" && tgt != caller {
				pair := [2]string{caller, tgt}
				if !seen[pair] {
					seen[pair] = true
					b.AddEdgeCtx(caller, tgt, "calls", line, "call")
				}

				continue
			}

			b.RawCalls = append(b.RawCalls, &model.RawCall{
				CallerID: caller, Callee: name, Language: lang,
				SourceFile: path, SourceLocation: base.Loc(line),
			})
		}

		return b.Result()
	}
}

func isCallableKind(kind string) bool {
	switch kind {
	case "function", "method", "macro", "constructor", "subroutine", "procedure", "test", "rule", "task", "target", "command":
		return true
	}

	return false
}

// genericGrammarBlocklist excludes grammars that are data/markup/config, not
// program structure, or that Graphify maps to documents.
var genericGrammarBlocklist = base.NewSet(
	"markdown", "markdown_inline", "json", "json5", "yaml", "toml", "xml", "html", "css", "scss", "less",
	"csv", "diff", "comment", "jsdoc", "doxygen", "regex", "gitcommit", "git_rebase", "gitattributes",
	"gitignore", "git_config", "ini", "properties", "requirements", "todotxt", "ssh_config", "rst",
	"org", "norg", "djot", "bibtex", "pem", "dtd", "editorconfig", "vimdoc", "textproto", "ron",
	"corn", "cpon", "kdl", "tmux", "hyprlang", "desktop", "eds", "http", "hurl", "mermaid", "dot",
	"csv", "tsv", "typst", "ledger", "beancount", "cooklang", "chatito", "disassembly", "llvm", "asm",
	"embedded_template", "jinja2", "liquid", "twig", "pug", "html_tags",
)

// registerTagsLanguages registers the tags extractor for every grammar with
// a tags query whose extensions are not yet claimed.
func registerTagsLanguages() {
	for _, e := range grammars.AllLanguages() {
		if genericGrammarBlocklist.Has(e.Name) || len(e.Extensions) == 0 {
			continue
		}

		var free []string

		for _, ext := range e.Extensions {
			if extract.LookupLanguage("x"+strings.ToLower(ext)) == nil {
				free = append(free, ext)
			}
		}

		if len(free) == 0 {
			continue
		}

		if strings.TrimSpace(e.TagsQuery) == "" && !hasInferredTags(e) {
			continue
		}

		extract.Register(&extract.Language{Name: e.Name, Extensions: free, Extract: makeTagsExtractor(e)})
	}
}

// hasInferredTags reports whether gotreesitter can infer a tags query
// without loading the grammar eagerly for every language (only grammars in
// the known-good set are allowed).
func hasInferredTags(e grammars.LangEntry) bool {
	return inferredTagLanguages.Has(e.Name)
}

var inferredTagLanguages = base.NewSet(
	"angular", "bicep", "bitbake", "cairo", "capnp", "circom", "clojure", "cmake", "commonlisp", "crystal",
	"cue", "d", "dart", "devicetree", "erlang", "faust", "fish", "fortran", "fsharp", "gdscript",
	"gleam", "glsl", "gn", "graphql", "groovy", "hack", "hare", "haxe", "hcl", "hlsl", "jq", "julia",
	"luau", "matlab", "mojo", "move", "nim", "ocaml", "odin", "powershell", "prisma", "proto", "puppet",
	"r", "rescript", "scheme", "solidity", "sql", "squirrel", "starlark", "teal", "templ", "thrift",
	"tlaplus", "v", "vhdl", "wgsl", "apex", "authzed", "arduino", "haskell", "elm", "purescript",
	"racket", "fennel", "janet", "nix", "tcl", "perl", "ada", "agda", "cobol", "pascal", "verilog",
	"smithy", "pkl", "nickel", "dhall", "rego", "promql", "sparql", "ql", "wat", "tablegen", "firrtl",
	"elsa", "enforce", "forth", "uxntal", "yuck", "linkerscript", "meson", "just", "make", "ninja",
	"kconfig", "nushell", "awk", "brightscript", "elisp", "facility", "fidl", "bass", "wolfram",
)
