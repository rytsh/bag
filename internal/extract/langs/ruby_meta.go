package langs

import (
	"sort"
	"strings"

	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

// Ruby resolution metadata: method dispatch kind, lookup barriers, reopened
// classes, external method owners and superclass references, which the
// corpus resolver needs to prove an inherited implicit-self call.
//
// Adapted from Graphify's extractors/engine.py Ruby handling (Apache-2.0).

var (
	rubyLookupMutators = map[string]bool{
		"alias_method": true, "attr": true, "attr_accessor": true, "attr_reader": true,
		"attr_writer": true, "define_method": true, "define_singleton_method": true,
		"include": true, "extend": true, "module_function": true, "prepend": true,
		"refine": true, "remove_method": true, "undef_method": true, "using": true,
	}
	rubyExternalOwnerMutators = func() map[string]bool {
		m := map[string]bool{"class_eval": true, "class_exec": true, "module_eval": true, "module_exec": true}
		for k := range rubyLookupMutators {
			m[k] = true
		}

		return m
	}()
	rubyClassFactories = map[[2]string]bool{{"Struct", "new"}: true, {"Class", "new"}: true, {"Data", "define"}: true}
)

func rubyConstRaw(n *tsx.Node) string {
	if n == nil || (n.Type() != "constant" && n.Type() != "scope_resolution") {
		return ""
	}

	return strings.TrimSpace(n.Text())
}

// rubyBodyHasLookupBarrier reports whether a lexical body can alter method
// lookup dynamically (mutators, alias/undef, nested/conditional defs).
func rubyBodyHasLookupBarrier(body *tsx.Node) bool {
	var visit func(n *tsx.Node, direct bool) bool
	visit = func(n *tsx.Node, direct bool) bool {
		for _, c := range n.Children() {
			switch c.Type() {
			case "class", "module":
				continue
			case "singleton_class", "method", "singleton_method":
				if !direct {
					return true
				}

				continue
			case "alias", "undef":
				return true
			case "call":
				recv, m := c.Field("receiver"), c.Field("method")
				if (recv == nil || recv.Text() == "self") && m != nil && rubyLookupMutators[m.Text()] {
					return true
				}
			}

			if visit(c, false) {
				return true
			}
		}

		return false
	}

	return visit(body, true)
}

func rubyFileHasRefinementBarrier(root *tsx.Node) bool {
	var visit func(n *tsx.Node) bool
	visit = func(n *tsx.Node) bool {
		if n.Type() == "call" {
			if m := n.Field("method"); m != nil && (m.Text() == "refine" || m.Text() == "using") {
				return true
			}
		}

		for _, c := range n.Children() {
			if visit(c) {
				return true
			}
		}

		return false
	}

	return visit(root)
}

// rubyExternalMethodOwners returns constant owners modified outside their
// class body.
func rubyExternalMethodOwners(root *tsx.Node) []string {
	owners := map[string]bool{}

	var constRefs func(n *tsx.Node) []string
	constRefs = func(n *tsx.Node) []string {
		switch n.Type() {
		case "constant", "scope_resolution":
			if r := rubyConstRaw(n); r != "" {
				return []string{r}
			}

			return nil
		case "left_assignment_list", "parenthesized_statements", "splat_argument":
			var out []string
			for _, c := range n.NamedChildren() {
				out = append(out, constRefs(c)...)
			}

			return out
		}

		return nil
	}

	var aliasRefs func(n *tsx.Node, allowArray bool) []string
	aliasRefs = func(n *tsx.Node, allowArray bool) []string {
		switch {
		case n.Type() == "constant" || n.Type() == "scope_resolution":
			if r := rubyConstRaw(n); r != "" {
				return []string{r}
			}

			return nil
		case n.Type() == "parenthesized_statements" || n.Type() == "right_assignment_list" ||
			(allowArray && n.Type() == "array"):
			var out []string
			for _, c := range n.NamedChildren() {
				out = append(out, aliasRefs(c, allowArray)...)
			}

			return out
		}

		return nil
	}

	var visit func(n *tsx.Node)
	visit = func(n *tsx.Node) {
		switch n.Type() {
		case "assignment", "operator_assignment":
			left := n.Field("left")

			var lhs []string
			if left != nil {
				lhs = constRefs(left)
			}

			for _, r := range lhs {
				owners[r+"::*"] = true
			}

			if right := n.Field("right"); n.Type() == "assignment" && len(lhs) > 0 && right != nil {
				for _, r := range aliasRefs(right, left != nil && left.Type() == "left_assignment_list") {
					owners[r+"::*"] = true
				}
			}
		}

		switch n.Type() {
		case "class", "module":
			if nn := n.Field("name"); nn != nil && nn.Type() == "scope_resolution" {
				if r := rubyConstRaw(nn); r != "" {
					owners[r] = true
				}
			}
		case "singleton_method":
			if o := n.Field("object"); o != nil && o.Text() != "self" {
				if r := rubyConstRaw(o); r != "" {
					owners[r] = true
				}
			}

			return
		case "singleton_class":
			if v := n.Field("value"); v != nil && v.Text() != "self" {
				if r := rubyConstRaw(v); r != "" {
					owners[r] = true
				}
			}
		case "call":
			recv, m := n.Field("receiver"), n.Field("method")
			if recv != nil && (recv.Type() == "constant" || recv.Type() == "scope_resolution") &&
				m != nil && rubyExternalOwnerMutators[m.Text()] {
				if r := rubyConstRaw(recv); r != "" {
					owners[r] = true
				}
			}
		case "method":
			return
		}

		for _, c := range n.Children() {
			visit(c)
		}
	}

	visit(root)

	out := make([]string, 0, len(owners))
	for k := range owners {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}

func rubyPreScan(x *generic.Ctx) {
	fn := x.B.AddFileNode()
	fn.SetMeta("ruby_resolution_schema", 1)

	if rubyBodyHasLookupBarrier(x.Tree.Root) || rubyFileHasRefinementBarrier(x.Tree.Root) {
		fn.SetMeta("ruby_lookup_unsafe", true)
	}

	if owners := rubyExternalMethodOwners(x.Tree.Root); len(owners) > 0 {
		v := make([]any, len(owners))
		for i, o := range owners {
			v[i] = o
		}

		fn.SetMeta("ruby_external_method_owners", v)
	}
}

func rubyClassMeta(x *generic.Ctx, n *tsx.Node, classID string, reopened bool) {
	node := x.B.Get(classID)
	if node == nil {
		return
	}

	absolute := false
	if nn := x.Cfg.FindName(n); nn != nil {
		absolute = strings.HasPrefix(nn.Text(), "::")
	}

	if absolute {
		node.SetMeta("ruby_lookup_unsafe", true)
	}

	if reopened {
		node.SetMeta("ruby_reopened", true)
	}

	if bd := x.Cfg.FindBody(n); bd != nil && rubyBodyHasLookupBarrier(bd) {
		node.SetMeta("ruby_lookup_unsafe", true)
	}
}

// rubyInheritsMeta stamps a Ruby inherits edge with the raw superclass
// reference and the enclosing lexical scopes (innermost first).
func rubyInheritsMeta(x *generic.Ctx, e *model.Edge, raw string, scopes []string) {
	rev := make([]any, len(scopes))
	for i := range scopes {
		rev[i] = scopes[len(scopes)-1-i]
	}

	e.SetMeta("ruby_superclass_ref", raw)
	e.SetMeta("ruby_lexical_scopes", rev)
}

// rubyMethodKind tags a method node with its dispatch kind.
func rubyMethodKind(x *generic.Ctx, n *tsx.Node, funcID string) {
	if x.ParentOf(funcID) == "" {
		return
	}

	kind := "ambiguous"

	switch {
	case n.Type() == "singleton_method":
		if o := n.Field("object"); x.SingletonDepth == 0 && o != nil && o.Text() == "self" {
			kind = "singleton"
		}
	case x.SingletonDepth == 1:
		kind = "singleton"
	case x.SingletonDepth == 0:
		kind = "instance"
	}

	counts, _ := x.Data["rb_method_counts"].(map[string]int)
	kinds, _ := x.Data["rb_method_kinds"].(map[string]map[string]bool)

	if counts == nil {
		counts, kinds = map[string]int{}, map[string]map[string]bool{}
		x.Data["rb_method_counts"], x.Data["rb_method_kinds"] = counts, kinds
	}

	if kinds[funcID] == nil {
		kinds[funcID] = map[string]bool{}
	}

	kinds[funcID][kind] = true
	counts[funcID]++

	stored := "ambiguous"
	if len(kinds[funcID]) == 1 && counts[funcID] == 1 && (kind == "instance" || kind == "singleton") {
		stored = kind
	}

	if node := x.B.Get(funcID); node != nil {
		node.SetMeta("ruby_method_kind", stored)
	}
}

// rubyExtraWalk handles `class << self` and constant class factories
// (`Foo = Struct.new(...) do ... end`, `Err = Class.new(Base)`).
func rubyExtraWalk(x *generic.Ctx, n *tsx.Node, parentClass string) bool {
	switch n.Type() {
	case "singleton_class":
		if parentClass == "" {
			return false
		}

		v, bd := n.Field("value"), n.Field("body")
		if v == nil || v.Text() != "self" || bd == nil {
			return false
		}

		if rubyBodyHasLookupBarrier(bd) {
			if node := x.B.Get(parentClass); node != nil {
				node.SetMeta("ruby_lookup_unsafe", true)
			}
		}

		x.SingletonDepth++
		for _, c := range bd.Children() {
			x.Walk(c, parentClass)
		}
		x.SingletonDepth--

		return true
	case "assignment":
		return rubyClassFactory(x, n)
	}

	return false
}

func rubyClassFactory(x *generic.Ctx, n *tsx.Node) bool {
	left, right := n.Field("left"), n.Field("right")
	if left == nil || right == nil || left.Type() != "constant" || right.Type() != "call" {
		return false
	}

	recv, m := right.Field("receiver"), right.Field("method")
	if recv == nil || m == nil || recv.Type() != "constant" || !rubyClassFactories[[2]string{recv.Text(), m.Text()}] {
		return false
	}

	constName := left.Text()
	if constName == "" {
		return false
	}

	segs := strings.Split(constName, "::")
	qualified := strings.Join(append(append([]string{}, x.ClassScope...), segs...), "::")
	line := n.Line()
	b := x.B

	classID := ids.MakeID(b.Stem, qualified)
	node := b.AddNode(classID, qualified, line)
	node.SetMeta("ruby_lookup_unsafe", true)
	x.MarkCallable(classID, true)
	b.AddEdge(b.FileID, classID, "contains", line)

	if recv.Text() == "Class" {
		if args := right.ChildOfType("argument_list"); args != nil {
			for _, a := range args.Children() {
				if a.Type() != "constant" && a.Type() != "scope_resolution" {
					continue
				}

				last := a.Text()
				if a.Type() == "scope_resolution" {
					last = ""

					for _, c := range a.Children() {
						if c.Type() == "constant" {
							last = c.Text()
						}
					}
				}

				if last != "" {
					baseID := ids.MakeID(b.Stem, last)
					if !b.Has(baseID) {
						baseID = ids.MakeID(last)
						if !b.Has(baseID) {
							b.AddStub(baseID, last)
						}
					}

					e := b.AddEdge(classID, baseID, "inherits", line)
					rubyInheritsMeta(x, e, rubyConstRaw(a), x.LexicalScopes)
				}

				break
			}
		}
	}

	block := right.ChildOfType("do_block", "block")
	if block != nil {
		body := block.ChildOfType("body_statement")
		if body == nil {
			body = block
		}

		x.ClassScope = append(x.ClassScope, segs...)
		x.LexicalScopes = append(x.LexicalScopes, qualified)

		for _, c := range body.Children() {
			x.Walk(c, classID)
		}

		x.LexicalScopes = x.LexicalScopes[:len(x.LexicalScopes)-1]
		x.ClassScope = x.ClassScope[:len(x.ClassScope)-len(segs)]
	}

	return true
}
