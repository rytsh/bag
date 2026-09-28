package langs

import (
	"crypto/sha1" //nolint:gosec // id digest, mirrors Graphify
	"encoding/hex"
	"strings"

	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/generic"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

type csRef struct {
	name, role, qualifier string
	qualified             bool
}

var csTypeParamScopes = base.NewSet("class_declaration", "interface_declaration", "record_declaration",
	"struct_declaration", "method_declaration")

func csTypeParams(n *tsx.Node) base.Set {
	out := base.Set{}

	for s := n; s != nil; s = s.Parent() {
		if !csTypeParamScopes.Has(s.Type()) {
			continue
		}

		for _, c := range s.Children() {
			if c.Type() != "type_parameter_list" {
				continue
			}

			for _, p := range c.Children() {
				switch p.Type() {
				case "type_parameter":
					if id := p.ChildOfType("identifier"); id != nil && id.Text() != "" {
						out[id.Text()] = struct{}{}
					}
				case "identifier":
					if p.Text() != "" {
						out[p.Text()] = struct{}{}
					}
				}
			}
		}
	}

	return out
}

func rpartition(s, sep string) (string, string) {
	if i := strings.LastIndex(s, sep); i >= 0 {
		return s[:i], s[i+len(sep):]
	}

	return "", s
}

func csTypeRefs(n *tsx.Node, generic bool, out *[]csRef, skip base.Set) {
	if n == nil {
		return
	}

	if skip == nil {
		skip = csTypeParams(n)
	}

	role := roleOf(generic)

	switch n.Type() {
	case "predefined_type":
		return
	case "identifier":
		if s := n.Text(); s != "" && !skip.Has(s) {
			*out = append(*out, csRef{name: s, role: role})
		}

		return
	case "qualified_name":
		prefix, text := rpartition(n.Text(), ".")
		text = strings.SplitN(text, "<", 2)[0]

		if text != "" && !skip.Has(text) {
			*out = append(*out, csRef{name: text, role: role, qualified: true, qualifier: prefix})
		}

		return
	case "generic_name":
		nc := n.Field("name")
		if nc == nil {
			nc = n.ChildOfType("identifier")
		}

		if nc != nil {
			q := nc.Type() == "qualified_name"
			prefix, name := rpartition(nc.Text(), ".")

			if name != "" && !skip.Has(name) {
				r := csRef{name: name, role: role, qualified: q}
				if q {
					r.qualifier = prefix
				}

				*out = append(*out, r)
			}
		}

		for _, s := range n.Children() {
			if s.Type() == "type_argument_list" {
				for _, a := range s.NamedChildren() {
					csTypeRefs(a, true, out, skip)
				}
			}
		}

		return
	case "nullable_type", "array_type", "pointer_type", "ref_type":
		for _, c := range n.NamedChildren() {
			csTypeRefs(c, generic, out, skip)
		}

		return
	case "tuple_type":
		for _, el := range n.Children() {
			if el.Type() == "tuple_element" {
				csTypeRefs(el.Field("type"), generic, out, skip)
			}
		}

		return
	}

	if n.IsNamed() {
		for _, c := range n.NamedChildren() {
			csTypeRefs(c, generic, out, skip)
		}
	}
}

func csRefMeta(r csRef) map[string]any {
	m := map[string]any{"ref_token": r.name}
	if r.qualified {
		m["qualified"] = true
	}

	if r.qualifier != "" {
		m["ref_qualifier"] = r.qualifier
	}

	return m
}

func csEmit(x *generic.Ctx, from string, line int, refs []csRef, def string) {
	for _, r := range refs {
		ctx := def
		if r.role == "generic_arg" {
			ctx = "generic_arg"
		}

		tgt := x.EnsureNamed(r.name)
		if tgt != from {
			e := x.B.AddEdgeCtx(from, tgt, "references", line, ctx)
			e.Metadata = csRefMeta(r)
		}
	}
}

func csReadTypeName(n *tsx.Node) (string, bool, string, bool) {
	if n == nil {
		return "", false, "", false
	}

	switch n.Type() {
	case "identifier", "predefined_type":
		return n.Text(), false, "", true
	case "qualified_name":
		prefix, tail := rpartition(n.Text(), ".")

		return strings.SplitN(tail, "<", 2)[0], true, prefix, true
	case "generic_name":
		if nn := n.Field("name"); nn != nil {
			q := nn.Type() == "qualified_name"
			prefix, tail := rpartition(nn.Text(), ".")

			if !q {
				prefix = ""
			}

			return tail, q, prefix, true
		}
	}

	for _, c := range n.NamedChildren() {
		if a, b, c2, ok := csReadTypeName(c); ok {
			return a, b, c2, true
		}
	}

	return "", false, "", false
}

func csAttributes(n *tsx.Node) []csRef {
	skip := csTypeParams(n)

	var out []csRef

	for _, c := range n.Children() {
		if c.Type() != "attribute_list" {
			continue
		}

		for _, a := range c.Children() {
			if a.Type() != "attribute" {
				continue
			}

			nn := a.Field("name")
			if nn == nil {
				nn = a.ChildOfType("identifier", "qualified_name")
			}

			if nn == nil {
				continue
			}

			q := nn.Type() == "qualified_name"
			prefix, text := rpartition(nn.Text(), ".")

			if text != "" && !skip.Has(text) {
				r := csRef{name: text, qualified: q}
				if q {
					r.qualifier = prefix
				}

				out = append(out, r)
			}
		}
	}

	return out
}

func csImport(x *generic.Ctx, n *tsx.Node) [][2]string {
	text := strings.TrimRight(strings.TrimSpace(n.Text()), ";")
	text = strings.TrimSpace(strings.TrimPrefix(text, "global "))

	if !strings.HasPrefix(text, "using") {
		return nil
	}

	body := strings.TrimSpace(text[len("using"):])
	kind, alias, target := "namespace", "", body

	switch {
	case strings.HasPrefix(body, "static "):
		kind, target = "static", strings.TrimSpace(body[len("static "):])
	case strings.Contains(body, "="):
		lhs, rhs, _ := strings.Cut(body, "=")
		kind, alias, target = "alias", strings.TrimSpace(lhs), strings.TrimSpace(rhs)
	}

	if target == "" {
		return nil
	}

	md := map[string]any{"using_kind": kind, "target_fqn": target}
	if alias != "" {
		md["alias"] = alias
	}

	if len(x.ScopeStack) > 0 {
		md["scope_kind"] = "namespace"
		md["scope_id"] = x.ScopeStack[len(x.ScopeStack)-1]
	} else {
		md["scope_kind"] = "file"
	}

	e := x.B.AddEdgeCtx(x.B.FileID, ids.MakeID(target), "imports", n.Line(), "import")
	e.Metadata = md

	return nil
}

func csInterfaceNames(root *tsx.Node) base.Set {
	out := base.Set{}

	root.Walk(func(n *tsx.Node) bool {
		if n.Type() == "interface_declaration" {
			if nn := n.Field("name"); nn != nil && nn.Text() != "" {
				out[nn.Text()] = struct{}{}
			}
		}

		return true
	})

	return out
}

func csClassifyBase(name string, ifaces base.Set) string {
	if ifaces.Has(name) {
		return "implements"
	}

	if len(name) >= 2 && name[0] == 'I' && name[1] >= 'A' && name[1] <= 'Z' {
		return "implements"
	}

	return "inherits"
}

func csClassHook(x *generic.Ctx, n *tsx.Node, classID string, line int) {
	b := x.B
	t := n.Type()
	tps := csTypeParams(n)
	ifaces, _ := x.Data["cs_ifaces"].(base.Set)

	if nn := b.Get(classID); nn != nil {
		if x.ParentClass != "" {
			if nn.Metadata == nil {
				nn.Metadata = map[string]any{}
			}

			nn.Metadata["is_nested_type"] = true
		}

		if t == "class_declaration" || t == "struct_declaration" || t == "interface_declaration" || t == "record_declaration" {
			for _, c := range n.Children() {
				if c.Type() == "modifier" && c.Text() == "partial" {
					if nn.Metadata == nil {
						nn.Metadata = map[string]any{}
					}

					nn.Metadata["is_partial"] = true
				}
			}
		}
	}

	for _, c := range n.Children() {
		if c.Type() != "base_list" {
			continue
		}

		for _, s := range c.Children() {
			if s.Type() != "identifier" && s.Type() != "generic_name" && s.Type() != "qualified_name" {
				continue
			}

			name, q, qual, ok := csReadTypeName(s)
			if !ok || name == "" || tps.Has(name) {
				continue
			}

			baseID := ids.MakeID(b.Stem, strings.Join(x.NamespaceStack, "."), name)
			if !b.Has(baseID) {
				baseID = ids.MakeID(name)
				if !b.Has(baseID) {
					b.AddStub(baseID, name)
				}
			}

			rel := "inherits"
			if t != "interface_declaration" {
				rel = csClassifyBase(name, ifaces)
			}

			e := b.AddEdge(classID, baseID, rel, line)
			e.Metadata = csRefMeta(csRef{name: name, qualified: q, qualifier: qual})

			if s.Type() == "generic_name" {
				for _, tal := range s.Children() {
					if tal.Type() != "type_argument_list" {
						continue
					}

					for _, a := range tal.NamedChildren() {
						var refs []csRef
						csTypeRefs(a, true, &refs, tps)

						for _, r := range refs {
							e := b.AddEdgeCtx(classID, x.EnsureNamed(r.name), "references", line, "generic_arg")
							e.Metadata = csRefMeta(r)
						}
					}
				}
			}
		}
	}

	if t == "class_declaration" || t == "record_declaration" || t == "struct_declaration" {
		for _, c := range n.Children() {
			if c.Type() != "parameter_list" {
				continue
			}

			for _, p := range c.Children() {
				if p.Type() != "parameter" {
					continue
				}

				if pn, recv := p.Field("name"), csReceiverTypeName(p.Field("type")); pn != nil && recv != "" && !tps.Has(recv) {
					csRecordField(x, classID, pn.Text(), recv)
				}

				var refs []csRef
				csTypeRefs(p.Field("type"), false, &refs, tps)
				csEmit(x, classID, p.Line(), refs, "field")
			}
		}
	}
}

func csFunctionHook(x *generic.Ctx, n *tsx.Node, funcID string, line int) {
	tps := csTypeParams(n)

	csRecordMethodScope(x, n, funcID)

	if params := n.Field("parameters"); params != nil {
		for _, p := range params.Children() {
			if p.Type() != "parameter" {
				continue
			}

			var refs []csRef
			csTypeRefs(p.Field("type"), false, &refs, tps)
			csEmit(x, funcID, line, refs, "parameter_type")
		}
	}

	var refs []csRef
	csTypeRefs(n.Field("returns"), false, &refs, tps)
	csEmit(x, funcID, line, refs, "return_type")

	for _, a := range csAttributes(n) {
		tgt := x.EnsureNamed(a.name)
		if tgt != funcID {
			e := x.B.AddEdgeCtx(funcID, tgt, "references", line, "attribute")
			e.Metadata = csRefMeta(a)
		}
	}
}

func csNamespaceName(n *tsx.Node) string {
	if nn := n.Field("name"); nn != nil {
		return strings.TrimSpace(nn.Text())
	}

	if c := n.ChildOfType("identifier", "qualified_name"); c != nil {
		return strings.TrimSpace(c.Text())
	}

	return ""
}

func csNamespaceID(dotted string) string {
	sum := sha1.Sum([]byte(dotted)) //nolint:gosec // id digest

	return "csharp_namespace:" + hex.EncodeToString(sum[:])[:16]
}

func csExtraWalk(x *generic.Ctx, n *tsx.Node, parentClass string) bool {
	b := x.B

	switch n.Type() {
	case "enum_member_declaration":
		if parentClass == "" {
			return false
		}

		nn := n.Field("name")
		if nn == nil || nn.Text() == "" {
			return true
		}

		id := ids.MakeID(parentClass, nn.Text())
		if !b.Has(id) {
			b.AddNode(id, nn.Text(), n.Line())
			b.AddEdge(parentClass, id, "case_of", n.Line())
		}

		return true
	case "namespace_declaration", "file_scoped_namespace_declaration":
		name := csNamespaceName(n)
		pushed := false

		if name != "" {
			x.NamespaceStack = append(x.NamespaceStack, name)
			x.ScopeStack = append(x.ScopeStack, "s"+itoa(int(n.StartByte())))
			pushed = true
			label := strings.Join(x.NamespaceStack, ".")
			id := csNamespaceID(label)

			if !b.Has(id) {
				nn := b.AddNode(id, label, n.Line())
				nn.Type = "namespace"
				nn.Metadata = map[string]any{"kind": "csharp_namespace", "namespace": label}
			}

			b.AddEdge(b.FileID, id, "contains", n.Line())
		}

		if n.Type() == "file_scoped_namespace_declaration" {
			return true
		}

		if bd := n.Field("body"); bd != nil {
			for _, c := range bd.Children() {
				x.Walk(c, parentClass)
			}
		}

		if pushed {
			x.NamespaceStack = x.NamespaceStack[:len(x.NamespaceStack)-1]
			x.ScopeStack = x.ScopeStack[:len(x.ScopeStack)-1]
		}

		return true
	case "field_declaration":
		if parentClass == "" {
			return false
		}

		tn := n.Field("type")
		if tn == nil {
			if vd := n.ChildOfType("variable_declaration"); vd != nil {
				tn = vd.Field("type")
			}
		}

		name, _, _, ok := csReadTypeName(tn)
		if !ok {
			return true
		}

		ref := tn
		if ref == nil {
			ref = n
		}

		tps := csTypeParams(ref)
		if name == "" || tps.Has(name) {
			return true
		}

		if upperStart(name) {
			if vd := n.ChildOfType("variable_declaration"); vd != nil {
				for _, d := range vd.Children() {
					if d.Type() != "variable_declarator" {
						continue
					}

					nn := d.Field("name")
					if nn == nil {
						nn = d.ChildOfType("identifier")
					}

					if nn != nil {
						csRecordField(x, parentClass, nn.Text(), name)
					}
				}
			}
		}

		var refs []csRef
		csTypeRefs(tn, false, &refs, tps)
		csEmit(x, parentClass, n.Line(), refs, "field")

		return true
	case "property_declaration":
		if parentClass == "" {
			return false
		}

		if nn := n.Field("name"); nn != nil && nn.Text() != "" {
			pid := ids.MakeID(parentClass, nn.Text())
			if !b.Has(pid) {
				b.AddNode(pid, nn.Text(), n.Line())
				b.AddEdgeCtx(parentClass, pid, "defines", n.Line(), "field")
			}
		}

		if tn := n.Field("type"); tn != nil {
			if pn := n.Field("name"); pn != nil {
				csRecordField(x, parentClass, pn.Text(), csReceiverTypeName(tn))
			}

			var refs []csRef
			csTypeRefs(tn, false, &refs, nil)
			csEmit(x, parentClass, n.Line(), refs, "field")
		}

		return true
	}

	if parentClass != "" && strings.HasPrefix(n.Type(), "preproc_") {
		for _, c := range n.Children() {
			x.Walk(c, parentClass)
		}

		return true
	}

	return false
}

func csBareCallName(n *tsx.Node) string {
	if n.Type() == "generic_name" {
		if c := n.ChildOfType("identifier"); c != nil {
			return c.Text()
		}
	}

	return n.Text()
}

func csCallName(x *generic.Ctx, n *tsx.Node) (string, bool, string) {
	if n.Type() == "object_creation_expression" {
		if name, _, _, ok := csReadTypeName(n.Field("type")); ok && name != "" {
			return name, false, ""
		}

		return "", false, ""
	}

	fn := n.Field("function")
	if fn == nil {
		return "", false, ""
	}

	switch fn.Type() {
	case "member_access_expression":
		mn, recv := fn.Field("name"), fn.Field("expression")
		if mn == nil {
			return "", false, ""
		}

		r := ""

		if recv != nil {
			switch recv.Type() {
			case "identifier":
				r = recv.Text()
			case "this", "this_expression":
				r = "this"
			case "base", "base_expression":
				r = "base"
			case "member_access_expression":
				in, f := recv.Field("expression"), recv.Field("name")
				if in != nil && (in.Type() == "this" || in.Type() == "this_expression") && f != nil && f.Type() == "identifier" {
					r = f.Text()
				}
			}
		}

		csGenericCallRefs(x, fn.Field("name"), n)

		return csBareCallName(mn), true, r
	case "identifier":
		return fn.Text(), false, ""
	case "generic_name":
		csGenericCallRefs(x, fn, n)

		return csBareCallName(fn), false, ""
	}

	return "", false, ""
}

func csGenericCallRefs(x *generic.Ctx, name, call *tsx.Node) {
	if name == nil || name.Type() != "generic_name" {
		return
	}

	tal := name.ChildOfType("type_argument_list")
	if tal == nil {
		return
	}

	tps := csTypeParams(call)
	caller := x.CurrentCaller()

	for _, a := range tal.NamedChildren() {
		var refs []csRef
		csTypeRefs(a, true, &refs, tps)

		for _, r := range refs {
			tgt := x.EnsureNamed(r.name)
			if tgt == caller {
				continue
			}

			e := x.B.AddEdgeCtx(caller, tgt, "references", call.Line(), "generic_arg")
			e.Metadata = csRefMeta(r)
		}
	}
}

var csharpConfig = &generic.Config{
	Lang:    "csharp",
	Grammar: "c_sharp",
	ClassTypes: base.NewSet("class_declaration", "interface_declaration", "enum_declaration",
		"struct_declaration", "record_declaration"),
	FunctionTypes:     base.NewSet("method_declaration"),
	ImportTypes:       base.NewSet("using_directive"),
	CallTypes:         base.NewSet("invocation_expression", "object_creation_expression"),
	CallFunctionField: "function",
	CallAccessorTypes: base.NewSet("member_access_expression"),
	CallAccessorField: "name",
	BodyFallback:      []string{"declaration_list"},
	FunctionBoundary:  base.NewSet("method_declaration"),
	ImportHandler:     csImport,
	ClassHook:         csClassHook,
	FunctionHook:      csFunctionHook,
	ExtraWalk:         csExtraWalk,
	CallName:          csCallName,
	DecorateRawCall:   csDecorateRawCall,
	DeferStubTarget: func(n *tsx.Node) bool {
		if n.Type() != "object_creation_expression" {
			return false
		}

		_, q, qual, ok := csReadTypeName(n.Field("type"))

		return ok && q && qual != ""
	},
	DeferMember: func(member bool, recv string) bool { return member && recv != "" },
	PreScan: func(x *generic.Ctx) {
		x.Data["cs_ifaces"] = csInterfaceNames(x.Tree.Root)
	},
}

// ExtractCSharp extracts a C# file.
func ExtractCSharp(path, root string, src []byte) *model.Extraction {
	return generic.Extract(csharpConfig, path, root, src)
}
