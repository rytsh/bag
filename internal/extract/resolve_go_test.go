package extract_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rytsh/bag/internal/detect"
	"github.com/rytsh/bag/internal/extract"
	_ "github.com/rytsh/bag/internal/extract/langs"
	"github.com/rytsh/bag/internal/graph"
	"github.com/rytsh/bag/internal/model"
)

// goPackage is one Go package spread over files the way real code is: the
// type declared in one file, methods on it in two others (one generic
// receiver, one file that declares a method above an unrelated type), and a
// test file that only references it. A second package declares a type of the
// same name so label-only resolution cannot pick one.
var goPackage = map[string]string{
	"go.mod": "module example.com/shop\n",
	"cart/cart.go": `package cart

type Cart struct {
	Items []int
}

func New() *Cart { return &Cart{} }
`,
	"cart/total.go": `package cart

func (c *Cart) Total() int {
	return len(c.Items)
}
`,
	"cart/set.go": `package cart

func (s Set[T]) Len() int { return len(s) }

type Set[T comparable] map[T]struct{}

func (c *Cart) Empty() bool { return c.Total() == 0 }
`,
	"cart/cart_test.go": `package cart

func fixture() *Cart { return New() }
`,
	"order/cart.go": `package order

type Cart struct{}

func (c Cart) Total() int { return 0 }
`,
}

func buildGo(t *testing.T, files map[string]string) *graph.Graph {
	t.Helper()

	root := t.TempDir()
	for rel, src := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	det, err := detect.Detect(root, detect.Options{IsCode: extract.IsCode})
	if err != nil {
		t.Fatal(err)
	}

	res, err := extract.Run(context.Background(), det.Files[detect.Code], extract.Options{Root: root, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}

	return graph.Build(res.Nodes, res.Edges, nil, root)
}

func nodesLabelled(g *graph.Graph, label, file string) []*model.Node {
	var out []*model.Node
	for _, n := range g.Nodes() {
		if n.Label == label && n.SourceFile == file {
			out = append(out, n)
		}
	}

	return out
}

// A Go type is one node however many files declare methods on it: the
// declaration, carrying every method, with its own location and end line.
func TestGoReceiversFoldIntoTheTypeDeclaration(t *testing.T) {
	g := buildGo(t, goPackage)

	for _, f := range []string{"cart/total.go", "cart/set.go"} {
		for _, n := range g.Nodes() {
			if n.SourceFile == f && n.Label == "Cart" {
				t.Errorf("%s still has its own Cart node %s", f, n.ID)
			}
		}
	}

	decl := nodesLabelled(g, "Cart", "cart/cart.go")
	if len(decl) != 1 {
		t.Fatalf("Cart declarations in cart/cart.go = %d, want 1", len(decl))
	}
	cart := decl[0]
	if cart.SourceLocation != "L3" || cart.EndLine != 5 {
		t.Errorf("Cart at %s-%d, want L3-5", cart.SourceLocation, cart.EndLine)
	}

	methods := map[string]string{}
	for _, e := range g.Edges() {
		if e.Source == cart.ID && e.Relation == "method" {
			methods[g.Node(e.Target).Label] = g.Node(e.Target).SourceFile
		}
	}
	if methods[".Total()"] != "cart/total.go" || methods[".Empty()"] != "cart/set.go" || len(methods) != 2 {
		t.Errorf("Cart methods = %v, want Total from total.go and Empty from set.go", methods)
	}

	// The other package's Cart is untouched and keeps its own method.
	other := nodesLabelled(g, "Cart", "order/cart.go")
	if len(other) != 1 || other[0].ID == cart.ID {
		t.Fatalf("order Cart = %+v", other)
	}
}

// A method declared above its type in the same file must not leave the type
// at the method's line, and a generic receiver names the type, not "Set[T]".
func TestGoReceiverAboveItsDeclaration(t *testing.T) {
	g := buildGo(t, goPackage)

	set := nodesLabelled(g, "Set", "cart/set.go")
	if len(set) != 1 {
		t.Fatalf("Set nodes = %d, want 1", len(set))
	}
	if set[0].SourceLocation != "L5" {
		t.Errorf("Set at %s, want its declaration at L5", set[0].SourceLocation)
	}

	n := 0
	for _, e := range g.Edges() {
		if e.Source == set[0].ID && e.Relation == "method" && g.Node(e.Target).Label == ".Len()" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("Set has %d .Len() method edges, want 1", n)
	}
}

// An unqualified type reference in another file of the package reaches the
// package's declaration even when other packages declare the same name.
func TestGoUnqualifiedTypeReferenceResolvesInThePackage(t *testing.T) {
	g := buildGo(t, goPackage)

	cart := nodesLabelled(g, "Cart", "cart/cart.go")[0]

	found := false
	for _, e := range g.Edges() {
		src := g.Node(e.Source)
		if e.Relation == "references" && src != nil && src.Label == "fixture()" {
			if e.Target != cart.ID {
				t.Errorf("fixture() references %s, want the package's Cart %s", e.Target, cart.ID)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("fixture() has no type reference")
	}

	for _, n := range g.Nodes() {
		if n.Label == "Cart" && n.SourceFile == "" {
			t.Errorf("a sourceless Cart stub survived: %s", n.ID)
		}
	}
}

func methodOwner(t *testing.T, g *graph.Graph, method string) *model.Node {
	t.Helper()

	for _, e := range g.Edges() {
		if e.Relation == "method" && g.Node(e.Target) != nil && g.Node(e.Target).Label == method {
			return g.Node(e.Source)
		}
	}
	t.Fatalf("no method edge into %s", method)

	return nil
}

// A qualified reference from another package (manager.Manager) must not share
// an id with the type it names: that collision salted the type's id.
func TestGoQualifiedReferenceKeepsTheTypeIDStable(t *testing.T) {
	g := buildGo(t, map[string]string{
		"go.mod":             "module example.com/app\n",
		"manager/manager.go": "package manager\n\ntype Manager struct{}\n",
		"server/server.go":   "package server\n\nimport \"example.com/app/manager\"\n\nfunc Start(m *manager.Manager) {}\n",
	})

	mgr := nodesLabelled(g, "Manager", "manager/manager.go")
	if len(mgr) != 1 || mgr[0].ID != "manager_manager" {
		t.Fatalf("Manager = %+v, want one node with id manager_manager", mgr)
	}

	found := false
	for _, e := range g.Edges() {
		if src := g.Node(e.Source); src != nil && src.Label == "Start()" && e.Relation == "references" {
			if e.Target != mgr[0].ID {
				t.Errorf("Start references %s, want %s", e.Target, mgr[0].ID)
			}
			found = true
		}
	}
	if !found {
		t.Error("Start() has no reference to manager.Manager")
	}
}

// A file named like a type of its package shares that type's id once
// canonicalized. The file keeps its extension wherever in the package the
// type is declared, and a receiver is only ever folded into a type.
func TestGoFileNamedLikeAPackageType(t *testing.T) {
	g := buildGo(t, map[string]string{
		"go.mod":          "module example.com/shop\n",
		"cart/types.go":   "package cart\n\ntype Total struct{}\n",
		"cart/total.go":   "package cart\n\nfunc helper() {}\n",
		"cart/x.go":       "package cart\n\nfunc (t Total) M() {}\n",
		"cart/orphan.go":  "package cart\n\nfunc (o Orphan) N() {}\n",
		"cart/orphan2.go": "package cart\n\nfunc helper2() {}\n",
	})

	total := nodesLabelled(g, "Total", "cart/types.go")
	if len(total) != 1 || total[0].ID != "cart_total" {
		t.Fatalf("Total = %+v, want one node with id cart_total", total)
	}
	if file := nodesLabelled(g, "total.go", "cart/total.go"); len(file) != 1 || file[0].ID != "cart_total_go" {
		t.Errorf("total.go file node = %+v, want id cart_total_go", file)
	}
	if owner := methodOwner(t, g, ".M()"); owner.ID != "cart_total" {
		t.Errorf("M belongs to %s (%s), want the Total type", owner.ID, owner.Label)
	}

	// Orphan is declared nowhere: its receiver stays, and never becomes a file.
	if owner := methodOwner(t, g, ".N()"); owner.Label != "Orphan" {
		t.Errorf("N belongs to %s (%s), want the Orphan receiver", owner.ID, owner.Label)
	}
}

// An external test package in the same directory is a package of its own:
// its types neither fold into nor block the package's.
func TestGoExternalTestPackageIsSeparate(t *testing.T) {
	g := buildGo(t, map[string]string{
		"go.mod":           "module example.com/shop\n",
		"cart/cart.go":     "package cart\n\ntype Cart struct{}\n",
		"cart/total.go":    "package cart\n\nfunc (c Cart) Total() int { return 0 }\n",
		"cart/ext_test.go": "package cart_test\n\ntype Cart struct{}\n\nfunc (c Cart) Fake() int { return 0 }\n",
	})

	cart := nodesLabelled(g, "Cart", "cart/cart.go")
	if len(cart) != 1 {
		t.Fatalf("Cart declarations in cart.go = %d", len(cart))
	}
	if owner := methodOwner(t, g, ".Total()"); owner.ID != cart[0].ID {
		t.Errorf("Total belongs to %s, want %s", owner.ID, cart[0].ID)
	}
	if owner := methodOwner(t, g, ".Fake()"); owner.SourceFile != "cart/ext_test.go" {
		t.Errorf("Fake belongs to %s in %s, want the test package's Cart", owner.ID, owner.SourceFile)
	}
}

// A type parameter shadows a package type of the same name.
func TestGoTypeParameterIsNotThePackageType(t *testing.T) {
	g := buildGo(t, map[string]string{
		"go.mod":       "module example.com/shop\n",
		"tree/node.go": "package tree\n\ntype Node struct{}\n",
		"tree/map.go":  "package tree\n\nfunc Map[Node any](n Node) Node { return n }\n",
	})

	node := nodesLabelled(g, "Node", "tree/node.go")[0]
	for _, e := range g.Edges() {
		if src := g.Node(e.Source); src != nil && src.Label == "Map()" && e.Target == node.ID {
			t.Errorf("Map's type parameter Node was resolved to the package type: %+v", e)
		}
	}
}
