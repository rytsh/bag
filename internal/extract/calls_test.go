package extract_test

import (
	"testing"

	"github.com/rytsh/bag/internal/graph"
)

// callTargets returns the labels and files a function calls, keyed by its
// owner-qualified label ("A.a" for a method, "f" for a function).
func callsFrom(g *graph.Graph, file, caller string) map[string]bool {
	out := map[string]bool{}
	for _, e := range g.Edges() {
		src, dst := g.Node(e.Source), g.Node(e.Target)
		if e.Relation != "calls" || src == nil || dst == nil || src.SourceFile != file || qualifiedLabel(g, src.ID) != caller {
			continue
		}
		out[qualifiedLabel(g, dst.ID)] = true
	}

	return out
}

// qualifiedLabel renders a node as Owner.name for a method, name otherwise.
func qualifiedLabel(g *graph.Graph, id string) string {
	n := g.Node(id)
	label := n.Label
	for len(label) > 0 && label[0] == '.' {
		label = label[1:]
	}
	if len(label) > 2 && label[len(label)-2:] == "()" {
		label = label[:len(label)-2]
	}
	for _, e := range g.Edges() {
		if e.Target == id && e.Relation == "method" {
			return g.Node(e.Source).Label + "." + label
		}
	}

	return label
}

func wantCalls(t *testing.T, g *graph.Graph, file, caller string, want ...string) {
	t.Helper()

	got := callsFrom(g, file, caller)
	if len(got) != len(want) {
		t.Errorf("%s calls %v, want %v", caller, keys(got), want)

		return
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("%s calls %v, want %v", caller, keys(got), want)

			return
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}

// self/this names the caller's own class; a same-named method of another
// class or a top-level function of the same name is not it.
func TestCallsThroughSelfBindToTheOwnClass(t *testing.T) {
	g := buildGo(t, map[string]string{
		"m.py": `class A:
    def a(self):
        self.b()

    def b(self):
        pass


class Z:
    def b(self):
        pass


class B:
    def run(self):
        self.other()

    def other(self):
        pass


def other():
    pass
`,
		"m.cs": `class B {
    void Run() { this.Other(); Other(); }
    void Other() {}
}
class D {
    void Other() {}
}
`,
	})

	wantCalls(t, g, "m.py", "A.a", "A.b")
	wantCalls(t, g, "m.py", "B.run", "B.other")
	wantCalls(t, g, "m.cs", "B.Run", "B.Other")
}

// C# members without a method_declaration node are declarations too, and
// their calls are theirs.
func TestCSharpConstructorsAccessorsAndLocalFunctions(t *testing.T) {
	g := buildGo(t, map[string]string{
		"P.cs": `class P {
    public P() { Calc(); }
    ~P() { Calc(); }
    public int X { get { return Calc(); } }
    public int Y => Calc();
    void Local() { int Inner() { return Calc(); } Inner(); }
    int Calc() { return 1; }
}
`,
	})

	wantCalls(t, g, "P.cs", "P.P", "P.Calc")
	wantCalls(t, g, "P.cs", "P.~P", "P.Calc")
	wantCalls(t, g, "P.cs", "X", "P.Calc")
	wantCalls(t, g, "P.cs", "Y", "P.Calc")
	wantCalls(t, g, "P.cs", "P.Local", "Inner")
	wantCalls(t, g, "P.cs", "Inner", "P.Calc")
}

// A member call on some other object never binds to a top-level function of
// the same name, and an untyped receiver is not guessed.
func TestMemberCallsAreNotGuessed(t *testing.T) {
	g := buildGo(t, map[string]string{
		"m.ts": `class A { run() {} }
class B { run() {} }
function go(a: A) { a.run() }
`,
		"n.js": `function forEach() {}
function push() {}
function log() {}
function main(arr, items) {
  arr.forEach(x => x);
  items.push(1);
  console.log("x");
}
`,
	})

	// The parameter's declared type picks A.run (receiver-typed resolution),
	// not whichever run() the file declared last.
	wantCalls(t, g, "m.ts", "go", "A.run")
	wantCalls(t, g, "n.js", "main")
}

// Members declared under a statement of the class body still belong to it.
func TestPythonMembersUnderClassBodyStatements(t *testing.T) {
	g := buildGo(t, map[string]string{
		"m.py": `import typing


class C:
    if typing.TYPE_CHECKING:
        def hidden(self):
            pass
    try:
        def tried(self):
            pass
    except ImportError:
        pass


def top():
    if True:
        def inner():
            pass
`,
	})

	for _, want := range []string{"C.hidden", "C.tried"} {
		found := false
		for _, n := range g.Nodes() {
			if n.SourceFile == "m.py" && qualifiedLabel(g, n.ID) == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is not a method of C", want)
		}
	}
}

// Same-named nested classes of different outer classes stay two classes,
// each with its own methods; unrelated classes keep their ids.
func TestNestedClassesOfTheSameNameStayApart(t *testing.T) {
	g := buildGo(t, map[string]string{
		"models.py": `class User:
    class Meta:
        def describe(self):
            return "user"


class Order:
    class Meta:
        def describe(self):
            return "order"
`,
		"Shop.java": `package com.x;
class Shop { static class Builder { void build() {} } }
class Other { static class Builder { void make() {} } }
`,
	})

	metas := map[string]string{}
	for _, n := range g.Nodes() {
		if n.Label == "Meta" && n.SourceFile == "models.py" {
			metas[n.ID] = n.SourceLocation
		}
	}
	if len(metas) != 2 {
		t.Fatalf("Meta classes = %v, want two", metas)
	}
	if user := g.Node("models_user"); user == nil {
		t.Error("User lost its id")
	}
	if _, ok := metas["models_meta"]; !ok {
		t.Errorf("the first Meta lost its plain id: %v", metas)
	}

	describes := 0
	for _, n := range g.Nodes() {
		if n.Label == ".describe()" && n.SourceFile == "models.py" {
			describes++
		}
	}
	if describes != 2 {
		t.Errorf("describe methods = %d, want one per Meta", describes)
	}

	builders := 0
	for _, n := range g.Nodes() {
		if n.Label == "Builder" && n.SourceFile == "Shop.java" {
			builders++
		}
	}
	if builders != 2 {
		t.Errorf("Builder classes = %d, want two", builders)
	}
}

// A nested class that extends its enclosing class keeps the inheritance,
// which a containment edge on the same pair must not replace.
func TestNestedSubclassKeepsItsInheritance(t *testing.T) {
	g := buildGo(t, map[string]string{
		"Shape.java": `abstract class Shape {
    static final class Circle extends Shape {}
}
`,
		"res.kt": `sealed class Res {
    class In : Res()
    object E : Res()
}
`,
	})

	inherits := map[string]bool{}
	for _, e := range g.Edges() {
		if e.Relation == "inherits" || e.Relation == "extends" {
			inherits[g.Node(e.Source).Label+"->"+g.Node(e.Target).Label] = true
		}
	}
	for _, want := range []string{"Circle->Shape", "In->Res", "E->Res"} {
		if !inherits[want] {
			t.Errorf("missing %s; inheritance edges: %v", want, keys(inherits))
		}
	}
}

// Rust: an impl block adds to its type whatever its generics, and a call
// binds by the path or receiver it names, not by the last same-named
// function in the file.
func TestRustImplsAndCalls(t *testing.T) {
	g := buildGo(t, map[string]string{
		"src/lib.rs": `pub struct Foo;
pub struct Bar;
pub struct Point<T> { x: T }

impl Foo { pub fn new() -> Foo { Foo } }
impl Bar { pub fn new() -> Bar { Bar } }

impl<T> Point<T> {
    pub fn fetch(&self) -> &T { self.other(); &self.x }
    fn other(&self) {}
}

impl std::fmt::Display for Point<i32> {
    fn fmt(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result { Ok(()) }
}

fn other() {}

fn main() {
    let f = Foo::new();
    let s = String::from("x");
}
`,
	})

	points := 0
	for _, n := range g.Nodes() {
		if n.SourceFile == "src/lib.rs" && (n.Label == "Point" || n.Label == "Point<T>" || n.Label == "Point<i32>") {
			points++
		}
	}
	if points != 1 {
		t.Errorf("Point nodes = %d, want the struct only", points)
	}

	wantCalls(t, g, "src/lib.rs", "main", "Foo.new")
	wantCalls(t, g, "src/lib.rs", "Point.fetch", "Point.other")

	methods := map[string]bool{}
	for _, e := range g.Edges() {
		if e.Relation == "method" && g.Node(e.Source).Label == "Point" {
			methods[g.Node(e.Target).Label] = true
		}
	}
	for _, m := range []string{".fetch()", ".other()", ".fmt()"} {
		if !methods[m] {
			t.Errorf("Point lacks %s: %v", m, keys(methods))
		}
	}
}

// TypeScript abstract methods are declarations of their class.
func TestTypeScriptAbstractMethods(t *testing.T) {
	g := buildGo(t, map[string]string{
		"shape.ts": `export abstract class Shape {
  abstract area(): number;
  describe(): string { return "area " + this.area(); }
}
`,
	})

	methods := map[string]bool{}
	for _, e := range g.Edges() {
		if e.Relation == "method" && g.Node(e.Source).Label == "Shape" {
			methods[g.Node(e.Target).Label] = true
		}
	}
	if !methods[".area()"] || !methods[".describe()"] {
		t.Errorf("Shape methods = %v, want area and describe", keys(methods))
	}
	wantCalls(t, g, "shape.ts", "Shape.describe", "Shape.area")
}
