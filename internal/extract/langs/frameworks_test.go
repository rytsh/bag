package langs

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/ids"
	"github.com/rytsh/bag/internal/model"
	"github.com/rytsh/bag/internal/store"
)

func TestAstroMask(t *testing.T) {
	src := []byte("---\r\ninterface Props { title: string }\r\n---\r\n<p>şablon 🪢</p>\r\n<script type='application/ld+json'>{\"@type\":\"Thing\"}</script>\r\n<script data-value='a>b'>const first = () => 1</script><script type=module>const second = () => first()</script>\r\n")
	masked := maskAstro(src)
	if len(masked) != len(src) {
		t.Fatal("mask changed source byte length")
	}
	for i, c := range src {
		if (c == '\n' || c == '\r') && masked[i] != c {
			t.Fatalf("newline at %d moved", i)
		}
	}
	for _, absent := range []string{"şablon", "@type", "<script", "<p>"} {
		if strings.Contains(string(masked), absent) {
			t.Fatalf("unmasked template/non-JS text %q", absent)
		}
	}
	ex := ExtractAstro("page.astro", "", src)
	if ex.Error != "" || ex.ParseErrors {
		t.Fatalf("valid Astro produced parse errors: %+v", ex)
	}
	for _, name := range []string{"Props", "first", "second"} {
		want := ids.MakeID("page", name)
		found := false
		for _, n := range ex.Nodes {
			if n.ID == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing Astro symbol %s", name)
		}
	}
	for _, src := range []string{"<div>Only markup</div>", "---\nfunction broken( {\n---\n<div/>"} {
		ex := ExtractAstro("page.astro", "", []byte(src))
		if ex.ParseErrors != strings.Contains(src, "broken") {
			t.Fatalf("incorrect parse-error status for %q: %+v", src, ex)
		}
	}
}

func TestSolidityFreeCallsFailClosed(t *testing.T) {
	ex := ExtractSolidity("free.sol", "", []byte(`
function same(uint x) pure returns (uint) { return x; }
function same(address x) pure returns (address) { return x; }
function local() pure {}
function run(uint x) pure { same(x); local(); Lib.method(); hidden(); }
contract C { function hidden() public {} }
`))
	if ex.Error != "" || ex.ParseErrors {
		t.Fatalf("parse failed: %+v", ex)
	}
	caller, target := ids.MakeID("free", "function", "run", "1:uint"), ids.MakeID("free", "function", "local", "0:")
	var calls []*model.Edge
	for _, e := range ex.Edges {
		if e.Source == caller && e.Relation == "calls" {
			calls = append(calls, e)
		}
	}
	if len(calls) != 1 || calls[0].Target != target {
		t.Fatalf("free function guessed ambiguous/contract calls: %+v", calls)
	}
	if len(ex.RawCalls) != 1 || ex.RawCalls[0].Callee != "method" || !ex.RawCalls[0].IsMemberCall {
		t.Fatalf("qualified call not preserved: %+v", ex.RawCalls)
	}
}

func TestVBNetQualifiedCallsFailClosed(t *testing.T) {
	ex := ExtractVBNet("calls.vb", "", []byte(`Module Helpers
    Sub Log(Optional text As String = "ok")
    End Sub
    Sub Ambiguous(x As Integer)
    End Sub
    Sub Ambiguous(x As String)
    End Sub
End Module
Class Worker
    Sub Run()
        helpers.LOG()
        Helpers.Log("ok")
        Helpers.Ambiguous(1)
        Dim value As Object
        value.Log()
        Unknown.Log()
    End Sub
End Class
`))
	if ex.Error != "" || ex.ParseErrors {
		t.Fatalf("parse failed: %+v", ex)
	}
	var calls []*model.Edge
	for _, e := range ex.Edges {
		if e.Relation == "calls" {
			calls = append(calls, e)
		}
	}
	if len(calls) != 1 || calls[0].Target != ids.MakeID("calls", "type", "helpers", "method", "log", "1", "1") {
		t.Fatalf("qualified calls incorrect: %+v", calls)
	}
	if len(ex.RawCalls) != 1 || ex.RawCalls[0].Callee != "Ambiguous" {
		t.Fatalf("ambiguous call not deferred: %+v", ex.RawCalls)
	}
	nodes, edges := ex.Nodes, ex.Edges
	resolveVBNetPartialCalls("", &nodes, &edges, []extract.FileResult{{Path: "calls.vb", Ex: ex}})
	if len(edges) != len(ex.Edges) {
		t.Fatal("ambiguous overload was guessed by partial resolver")
	}
}

func TestFrameworkExtractionCache(t *testing.T) {
	cache, err := store.OpenCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var per []extract.FileResult
	for _, file := range []string{"Main.vb", "Partial.vb"} {
		path := filepath.Join("..", "..", "..", "testdata", "frameworks", "vbnet", file)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		ex := ExtractVBNet(file, "", src)
		cache.Put(file, src, ex)
		got, ok := cache.Get(file, src)
		if !ok || !reflect.DeepEqual(got, ex) {
			t.Fatalf("VB.NET metadata/raw calls did not round-trip cache: %s", file)
		}
		per = append(per, extract.FileResult{Path: file, Ex: got})
	}
	var nodes []*model.Node
	var edges []*model.Edge
	for _, fr := range per {
		nodes = append(nodes, fr.Ex.Nodes...)
		edges = append(edges, fr.Ex.Edges...)
	}
	before := len(edges)
	resolveVBNetPartialCalls("", &nodes, &edges, per)
	if len(edges) != before+2 {
		t.Fatalf("cached partial call missing: edges %d -> %d", before, len(edges))
	}
	resolveVBNetPartialCalls("", &nodes, &edges, per)
	if len(edges) != before+2 {
		t.Fatal("partial resolver duplicated an existing edge")
	}
}
