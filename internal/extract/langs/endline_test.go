package langs

import (
	"strings"
	"testing"

	"github.com/rytsh/bag/internal/extract"
	"github.com/rytsh/bag/internal/model"
)

// span is the location a test expects for one node, by label.
type span struct{ start, end int }

func extractSpans(t *testing.T, path, src string) map[string]span {
	t.Helper()

	fn := extract.Lookup(path)
	if fn == nil {
		t.Fatalf("no extractor for %s", path)
	}

	ex := fn(path, "", []byte(src))
	if ex == nil || ex.Error != "" {
		t.Fatalf("extract %s: %+v", path, ex)
	}

	out := map[string]span{}
	for _, n := range ex.Nodes {
		if n.SourceFile == "" {
			continue
		}
		start := 0
		if strings.HasPrefix(n.SourceLocation, "L") {
			for _, c := range n.SourceLocation[1:] {
				start = start*10 + int(c-'0')
			}
		}
		out[n.Label] = span{start, n.EndLine}
	}

	return out
}

func checkSpans(t *testing.T, got map[string]span, want map[string]span) {
	t.Helper()

	for label, w := range want {
		g, ok := got[label]
		if !ok {
			t.Errorf("no node labelled %q (have %v)", label, got)

			continue
		}
		if g != w {
			t.Errorf("%s spans L%d-%d, want L%d-%d", label, g.start, g.end, w.start, w.end)
		}
	}
}

func TestEndLineGo(t *testing.T) {
	got := extractSpans(t, "svc/svc.go", `package svc

type Server struct {
	name string
}

type Store interface {
	Get(id string) string
}

func New() *Server {
	return &Server{}
}

func (s *Server) Start(
	port int,
) error {
	return nil
}

func Short() {}
`)

	checkSpans(t, got, map[string]span{
		"Server":   {3, 5},
		"Store":    {7, 9},
		"New()":    {11, 13},
		".Start()": {15, 19},
		// A one-line declaration has no range to report.
		"Short()": {21, 0},
		// Neither does an interface method signature.
		".Get()": {8, 0},
	})
}

func TestEndLinePython(t *testing.T) {
	got := extractSpans(t, "app/models.py", `class Invoice:
    def total(self):
        return sum(
            line.amount for line in self.lines
        )

    def paid(self):
        return True


def load(path):
    with open(path) as f:
        return f.read()
`)

	checkSpans(t, got, map[string]span{
		"Invoice":  {1, 8},
		".total()": {2, 5},
		".paid()":  {7, 8},
		"load()":   {11, 13},
	})
}

func TestEndLineTypeScript(t *testing.T) {
	got := extractSpans(t, "web/api.ts", `export class Client {
  fetch(id: string): Promise<string> {
    return get(id)
  }
}

export function get(id: string) {
  return Promise.resolve(id)
}

export const handler = (req: Request) => {
  return new Response("ok")
}
`)

	checkSpans(t, got, map[string]span{
		"Client":    {1, 5},
		".fetch()":  {2, 4},
		"get()":     {7, 9},
		"handler()": {11, 13},
	})
}

func TestEndLineJava(t *testing.T) {
	got := extractSpans(t, "src/Billing.java", `package billing;

public class Billing {
    public int total() {
        return 1;
    }
}
`)

	checkSpans(t, got, map[string]span{
		"Billing":  {3, 7},
		".total()": {4, 6},
	})
}

func TestSetEndLine(t *testing.T) {
	for name, tt := range map[string]struct {
		loc       string
		preset    int
		end, want int
	}{
		"records a range":                     {"L10", 0, 14, 14},
		"a one-line declaration has none":     {"L10", 0, 10, 0},
		"an end before the start is wrong":    {"L10", 0, 9, 0},
		"unknown end":                         {"L10", 0, 0, 0},
		"no start, no range":                  {"", 0, 14, 0},
		"not a line location":                 {"42:5", 0, 50, 0},
		"the first declaration keeps its end": {"L10", 20, 14, 20},
	} {
		n := &model.Node{SourceLocation: tt.loc, EndLine: tt.preset}
		if got := n.SetEndLine(tt.end).EndLine; got != tt.want {
			t.Errorf("%s: EndLine = %d, want %d", name, got, tt.want)
		}
	}

	var nilNode *model.Node
	if nilNode.SetEndLine(5) != nil {
		t.Error("SetEndLine on a nil node returned a node")
	}
}

func TestSpan(t *testing.T) {
	for want, n := range map[string]*model.Node{
		"L10-L24": {SourceLocation: "L10", EndLine: 24},
		"L10":     {SourceLocation: "L10"},
		"":        {},
		"42:5":    {SourceLocation: "42:5", EndLine: 50},
	} {
		if got := n.Span(); got != want {
			t.Errorf("Span(%+v) = %q, want %q", *n, got, want)
		}
	}
}
