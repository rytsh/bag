package tsx

import (
	"strings"
	"testing"
	"time"
)

func TestParseBudgetGrowsWithSize(t *testing.T) {
	if ParseBudget(0) != parseBaseBudget || ParseBudget(1<<30) != parseMaxBudget || ParseBudget(100_000) <= ParseBudget(1000) {
		t.Errorf("budget: 0=%s 1k=%s 100k=%s 1G=%s", ParseBudget(0), ParseBudget(1000), ParseBudget(100_000), ParseBudget(1<<30))
	}
}

// JSX in a .ts file made the typescript grammar backtrack for minutes and
// lose almost every declaration; such a file is parsed as tsx.
func TestTypeScriptWithJSXIsParsedAsTSX(t *testing.T) {
	const comp = `
export function Card%d(props: Props) {
  const [s, setS] = useState(0);
  return <div className="card" onClick={() => setS(s + 1)}><span>{props.title}</span></div>;
}
`
	var b strings.Builder
	for range 300 {
		b.WriteString(comp)
	}
	src := []byte(b.String())

	if g := TypeScriptGrammar(src); g != "tsx" {
		t.Fatalf("grammar = %s, want tsx", g)
	}

	start := time.Now()
	tree, err := Parse(TypeScriptGrammar(src), src)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Release()

	if took := time.Since(start); took > 5*time.Second || tree.StoppedEarly() != "" {
		t.Errorf("tsx parse took %s, stop %q", took, tree.StoppedEarly())
	}
}

// Plain TypeScript keeps its grammar: generics and comparisons are not JSX,
// and a <T>x type assertion only parses as typescript.
func TestPlainTypeScriptKeepsItsGrammar(t *testing.T) {
	for _, src := range []string{
		"const xs: Array<string> = [];\nfunction f<T>(x: T): Map<string, T> { return new Map<string, T>(); }\n",
		"if (a < b && c > d) { x = <number>y; }\n",
		"// see </docs>\nlet a = 1;\n",
	} {
		if g := TypeScriptGrammar([]byte(src)); g != "typescript" {
			t.Errorf("TypeScriptGrammar(%q) = %s", src, g)
		}
	}

	for _, src := range []string{
		"const el = <div />;\n",
		"function A() { return (\n  <Button onClick={go}>Go</Button>\n); }\n",
		"const frag = <>\n<b>x</b>\n</>;\n",
	} {
		if g := TypeScriptGrammar([]byte(src)); g != "tsx" {
			t.Errorf("TypeScriptGrammar(%q) = %s, want tsx", src, g)
		}
	}
}

// A file known to stall gets a short budget when a later pass parses it,
// with or without a final newline; one that then parses whole is not
// reported as stopped.
func TestRepeatParseOfAStalledSourceIsShort(t *testing.T) {
	src := []byte("x = 1\n")
	slowMu.Lock()
	slowSources[slowKey("python", src)] = true
	slowMu.Unlock()

	if slowKey("python", []byte("x = 1")) != slowKey("python", src) {
		t.Fatal("the final newline changes the key")
	}

	tree, err := Parse("python", []byte("x = 1"))
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Release()

	if tree.StoppedEarly() != "" {
		t.Errorf("a source that parsed whole was reported as stopped: %q", tree.StoppedEarly())
	}
}
