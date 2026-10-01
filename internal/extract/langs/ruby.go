package langs

import (
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/model"
)

func rubySanitize(name string) string {
	switch {
	case name == "":
		return name
	case strings.HasSuffix(name, "!"):
		return name[:len(name)-1] + "_bang"
	case strings.HasSuffix(name, "?"):
		return name[:len(name)-1] + "_pred"
	case strings.HasSuffix(name, "="):
		return name[:len(name)-1] + "_eq"
	}

	return name
}

func rubyConstFullName(n *tsx.Node) string {
	if n == nil {
		return ""
	}

	return strings.TrimPrefix(strings.TrimSpace(n.Text()), "::")
}

func rubyClassHook(x *generic.Ctx, n *tsx.Node, classID string, line int) {
	if sup := n.Field("superclass"); sup != nil {
		for _, s := range sup.Children() {
			base := ""

			switch s.Type() {
			case "constant":
				base = s.Text()
			case "scope_resolution":
				var consts []*tsx.Node

				for _, c := range s.Children() {
					if c.Type() == "constant" {
						consts = append(consts, c)
					}
				}

				if len(consts) > 0 {
					base = consts[len(consts)-1].Text()
				}
			default:
				continue
			}

			if base != "" {
				e := x.B.AddEdge(classID, x.EnsureNamed(base), "inherits", line)
				rubyInheritsMeta(x, e, strings.TrimSpace(s.Text()), x.LexicalScopes)
			}

			break
		}
	}

	bd := x.Cfg.FindBody(n)
	if bd == nil {
		return
	}

	for _, st := range bd.Children() {
		if st.Type() != "call" || st.Field("receiver") != nil {
			continue
		}

		m := st.Field("method")
		if m == nil || (m.Text() != "include" && m.Text() != "extend" && m.Text() != "prepend") {
			continue
		}

		args := st.Field("arguments")
		if args == nil {
			continue
		}

		for _, a := range args.Children() {
			if a.Type() != "constant" && a.Type() != "scope_resolution" {
				continue
			}

			if mod := rubyConstFullName(a); mod != "" {
				x.B.RawCalls = append(x.B.RawCalls, &model.RawCall{
					CallerID: classID, Callee: mod, Language: "mixin",
					SourceFile: x.Path, SourceLocation: base.Loc(st.Line()),
				})
			}
		}
	}
}

func rubyCallName(_ *generic.Ctx, n *tsx.Node) (string, bool, string) {
	callee := ""
	if m := n.Field("method"); m != nil {
		callee = m.Text()
	}

	recv := n.Field("receiver")
	if recv == nil {
		return callee, false, ""
	}

	switch recv.Type() {
	case "identifier", "constant":
		return callee, true, recv.Text()
	case "scope_resolution":
		return callee, true, rubyConstFullName(recv)
	}

	return callee, true, ""
}

var rubyConfig = &generic.Config{
	Lang:             "ruby",
	Grammar:          "ruby",
	ClassTypes:       base.NewSet("class", "module"),
	FunctionTypes:    base.NewSet("method", "singleton_method"),
	CallTypes:        base.NewSet("call"),
	NameFallback:     []string{"constant", "scope_resolution", "identifier"},
	BodyFallback:     []string{"body_statement"},
	FunctionBoundary: base.NewSet("method", "singleton_method"),
	SanitizeName:     rubySanitize,
	ClassHook:        rubyClassHook,
	CallName:         rubyCallName,
	DecorateRawCall:  rubyDecorateRawCall,
	PreScan:          rubyPreScan,
	ClassMeta:        rubyClassMeta,
	ExtraWalk:        rubyExtraWalk,
	FunctionHook:     func(x *generic.Ctx, n *tsx.Node, funcID string, _ int) { rubyMethodKind(x, n, funcID) },
	QualifyClassName: func(x *generic.Ctx, name string) (string, []string) {
		segs := strings.Split(name, "::")

		return strings.Join(append(append([]string{}, x.ClassScope...), segs...), "::"), segs
	},
}

// ExtractRuby extracts a Ruby file.
func ExtractRuby(path, root string, src []byte) *model.Extraction {
	return generic.Extract(rubyConfig, path, root, src)
}
