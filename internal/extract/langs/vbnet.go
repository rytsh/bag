package langs

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/extract/base"
	"github.com/rytsh/bag/internal/extract/tsx"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
)

var vbTypes = map[string]string{"class_block": "class", "module_block": "module", "interface_block": "interface", "structure_block": "structure", "enum_block": "enum"}

type vbCallKey struct {
	owner, name string
	arity       int
}

// ExtractVBNet is adapted from Graphify's extract_vbnet (Apache-2.0).
// The dedicated walker retains namespace owners, optional arities and Handles.
func ExtractVBNet(path, _ string, src []byte) *model.Extraction {
	tree, err := tsx.Parse("vbnet", src)
	if err != nil {
		return &model.Extraction{Error: err.Error()}
	}
	defer tree.Release()
	b := newStructuralBuilder(path, "vbnet")
	b.node(b.FileID, filepath.Base(path), "file", tree.Root, true, false)
	types, owners, events := map[string]string{}, map[string]string{}, map[[2]string]string{}
	methods := map[vbCallKey][]string{}
	type body struct {
		node                    *tsx.Node
		caller, owner, typeName string
	}
	type handle struct {
		caller, owner, written string
		node                   *tsx.Node
	}
	var bodies []body
	var handles []handle
	for _, stmt := range namedOfType(tree.Root, "imports_statement") {
		if ns := stmt.ChildOfType("namespace_name"); ns != nil {
			id := b.node(ids.MakeID("vbnet", "namespace", strings.ToLower(ns.Text())), ns.Text(), "external_namespace", ns, false, false)
			b.edge(b.FileID, id, "imports", stmt)
		}
	}
	typeRef := func(name string, n *tsx.Node) string {
		short := lastSeg(name, ".")
		if id := types[strings.ToLower(short)]; id != "" {
			return id
		}
		return b.node(ids.MakeID("vbnet", "type", strings.ToLower(name)), short, "external_type", n, false, false)
	}
	dataMember := func(owner string, n *tsx.Node, name, kind string) string {
		id := b.node(ids.MakeID(owner, kind, strings.ToLower(name), strconv.Itoa(n.Line()-1)), name, kind, n, true, false)
		b.edge(owner, id, "contains", n)
		return id
	}
	processType := func(block *tsx.Node, parent, namespace string) {
		kind := vbTypes[block.Type()]
		nn := block.Field("name")
		if nn == nil {
			return
		}
		name, full := nn.Text(), nn.Text()
		if namespace != "" {
			full = namespace + "." + name
		}
		owner := strings.ToLower(full)
		id := b.node(ids.MakeID(b.Stem, "type", owner), name, kind, block, true, kind == "class" || kind == "structure")
		n := b.Get(id)
		n.Metadata["name"], n.Metadata["full_name"] = name, full
		n.MetaOrder = append(n.MetaOrder, "name", "full_name")
		b.edge(parent, id, "contains", block)
		types[strings.ToLower(name)] = id
		if owners[strings.ToLower(name)] == "" {
			owners[strings.ToLower(name)] = owner
		}
		for _, clause := range namedOfType(block, "inherits_clause", "implements_clause") {
			relation := "inherits"
			if clause.Type() == "implements_clause" {
				relation = "implements"
			}
			for _, typ := range namedOfType(clause, "type") {
				b.edge(id, typeRef(typ.Text(), typ), relation, clause)
			}
		}
		for _, m := range block.NamedChildren() {
			switch m.Type() {
			case "enum_member":
				if name := m.Field("name"); name != nil {
					dataMember(id, m, name.Text(), "enum_member")
				}
			case "field_declaration":
				for _, d := range namedOfType(m, "variable_declarator") {
					if name := d.Field("name"); name != nil {
						dataMember(id, d, name.Text(), "field")
					}
				}
			case "property_declaration", "event_declaration":
				if name := m.Field("name"); name != nil {
					kind := "property"
					if m.Type() == "event_declaration" {
						kind = "event"
					}
					mid := dataMember(id, m, name.Text(), kind)
					if kind == "event" {
						events[[2]string{owner, strings.ToLower(name.Text())}] = mid
					}
				}
			case "method_declaration", "constructor_declaration":
				name, kind := "New", "constructor"
				if m.Type() == "method_declaration" {
					nn := m.Field("name")
					if nn == nil {
						continue
					}
					name, kind = nn.Text(), "method"
				}
				params := namedOfType(m.Field("parameters"), "parameter")
				arity, required := len(params), 0
				for _, p := range params {
					if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(p.Text())), "optional ") {
						required++
					}
				}
				var accepted []int
				for a := required; a <= arity; a++ {
					accepted = append(accepted, a)
				}
				mid := b.node(ids.MakeID(id, kind, strings.ToLower(name), strconv.Itoa(arity), strconv.Itoa(m.Line()-1)), name+"()", kind, m, true, true)
				n := b.Get(mid)
				n.Metadata["owner"], n.Metadata["name"], n.Metadata["arity"], n.Metadata["accepted_arities"] = owner, name, arity, accepted
				n.MetaOrder = append(n.MetaOrder, "owner", "name", "arity", "accepted_arities")
				b.edge(id, mid, "method", m)
				for _, a := range accepted {
					key := vbCallKey{owner, strings.ToLower(name), a}
					methods[key] = append(methods[key], mid)
				}
				for _, clause := range namedOfType(m, "handles_clause") {
					for _, h := range namedOfType(clause, "namespace_name") {
						handles = append(handles, handle{mid, owner, h.Text(), h})
					}
				}
				bodies = append(bodies, body{m, mid, owner, nn.Text()})
			}
		}
	}
	var scan func(*tsx.Node, string, string)
	scan = func(n *tsx.Node, parent, namespace string) {
		if n.Type() == "imports_statement" {
			return
		}
		if n.Type() == "namespace_block" {
			nn := n.Field("name")
			if nn == nil {
				return
			}
			full := nn.Text()
			if namespace != "" {
				full = namespace + "." + full
			}
			id := b.node(ids.MakeID(b.Stem, "namespace", strings.ToLower(full)), nn.Text(), "namespace", n, true, false)
			b.Get(id).Metadata["full_name"] = full
			b.Get(id).MetaOrder = append(b.Get(id).MetaOrder, "full_name")
			b.edge(parent, id, "contains", n)
			for _, c := range n.NamedChildren() {
				if c.StartByte() != nn.StartByte() || c.Type() != nn.Type() {
					scan(c, id, full)
				}
			}
			return
		}
		if vbTypes[n.Type()] != "" {
			processType(n, parent, namespace)
			for _, c := range namedOfType(n, "type_declaration") {
				scan(c, parent, namespace)
			}
			return
		}
		for _, c := range n.NamedChildren() {
			scan(c, parent, namespace)
		}
	}
	scan(tree.Root, b.FileID, "")
	for _, h := range handles {
		id := events[[2]string{h.owner, strings.ToLower(lastSeg(h.written, "."))}]
		if id == "" {
			id = b.node(ids.MakeID("vbnet", "event", strings.ToLower(h.written)), h.written, "external_event", h.node, false, false)
		}
		b.edge(h.caller, id, "handles", h.node)
	}
	for _, body := range bodies {
		walkStructural(body.node, func(n *tsx.Node) bool {
			if n.StartByte() != body.node.StartByte() && (n.Type() == "method_declaration" || n.Type() == "constructor_declaration" || n.Type() == "property_declaration") {
				return false
			}
			if n.Type() != "invocation" {
				return true
			}
			target := n.Field("target")
			if target == nil {
				return true
			}
			parts := strings.Split(target.Text(), ".")
			recv, callee := strings.ToLower(strings.Join(parts[:len(parts)-1], ".")), parts[len(parts)-1]
			owner := ""
			if recv == "" || recv == "me" || recv == "myclass" || recv == strings.ToLower(body.typeName) {
				owner = body.owner
			} else {
				owner = owners[lastSeg(recv, ".")]
			}
			if owner == "" {
				return true
			}
			arity := len(n.Field("arguments").NamedChildren())
			candidates := methods[vbCallKey{owner, strings.ToLower(callee), arity}]
			if len(candidates) == 1 {
				b.edge(body.caller, candidates[0], "calls", n)
			} else {
				b.RawCalls = append(b.RawCalls, &model.RawCall{CallerID: body.caller, Callee: callee, Owner: owner, Arity: arity, IsMemberCall: true, Language: "vbnet", SourceFile: path, SourceLocation: base.Loc(n.Line())})
			}
			return true
		})
	}
	res := b.Result()
	res.ParseErrors = tree.Root.HasError()
	return res
}

// resolveVBNetPartialCalls is adapted from Graphify's
// resolve_vbnet_partial_calls (Apache-2.0).
func resolveVBNetPartialCalls(_ string, nodesP *[]*model.Node, edgesP *[]*model.Edge, per []extract.FileResult) {
	methods := map[vbCallKey][]string{}
	for _, n := range *nodesP {
		m := n.Metadata
		if m["language"] != "vbnet" || (m["kind"] != "method" && m["kind"] != "constructor") {
			continue
		}
		owner, _ := m["owner"].(string)
		name, _ := m["name"].(string)
		var arities []int
		switch values := m["accepted_arities"].(type) {
		case []int:
			arities = values
		case []any:
			for _, v := range values {
				if a, ok := v.(float64); ok {
					arities = append(arities, int(a))
				}
			}
		}
		for _, a := range arities {
			key := vbCallKey{strings.ToLower(owner), strings.ToLower(name), a}
			methods[key] = append(methods[key], n.ID)
		}
	}
	existing := map[[2]string]bool{}
	for _, e := range *edgesP {
		if e.Relation == "calls" {
			existing[[2]string{e.Source, e.Target}] = true
		}
	}
	for _, fr := range per {
		for _, rc := range fr.Ex.RawCalls {
			if rc.Language != "vbnet" || rc.Owner == "" {
				continue
			}
			hits := methods[vbCallKey{strings.ToLower(rc.Owner), strings.ToLower(rc.Callee), rc.Arity}]
			if len(hits) != 1 || hits[0] == rc.CallerID {
				continue
			}
			pair := [2]string{rc.CallerID, hits[0]}
			if existing[pair] {
				continue
			}
			existing[pair] = true
			score := 1.0
			*edgesP = append(*edgesP, &model.Edge{Source: rc.CallerID, Target: hits[0], Relation: "calls", Context: "partial_type_call", Confidence: model.Extracted, ConfidenceScore: &score, Weight: 1, SourceFile: rc.SourceFile, SourceLocation: rc.SourceLocation})
		}
	}
}
