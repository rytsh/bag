# Known issues

Open parity gaps against Graphify that are not fixed in bag itself. Each entry
has a reproducer so it can be re-checked after a dependency bump.

## gotreesitter: JavaScript block with `X={k:v}` parses to ERROR

**Status:** open upstream (not reported yet). Reproduces on
`github.com/odvcencio/gotreesitter` v0.55.1 and on `main`
(`v0.55.2-0.20260927095639-272b3020e1e1`).

**Impact on bag:** very large minified bundles (for example a vendored
`.yarn/releases/yarn-4.7.0.cjs`, 2.7 MB) stop parsing at the first
occurrence (`ParseStopReason() == no_stacks_alive` at byte 63986). Only the
nodes/edges from the first ~64 KB are extracted. In sourcebot this is the
entire remaining `cjs` diff and the 11 `yarn_releases_*` `indirect_call` edges
in `ts`/`tsx`. Hand-written code is not affected.

### Reproducer

```go
package main

import (
	"fmt"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

func main() {
	lang := grammars.JavascriptLanguage()
	for _, s := range []string{
		`{T={r:x}}`,      // ERROR  (C tree-sitter: clean)
		`if(a){T={r:x}}`, // ERROR  (C tree-sitter: clean)
		`{T=[x]}`,        // ERROR  (C tree-sitter: clean)
		`{T={r:x};}`,     // clean
		`{T={r:1}}`,      // clean
		`{T={r}}`,        // clean
		`{T={r:x,s:1}}`,  // clean
		`T={r:x}`,        // clean
		`function f(){T={r:x}}`, // clean
	} {
		tree, _ := gts.NewParser(lang).Parse([]byte(s))
		fmt.Printf("%-24s hasError=%v\n", s, tree.RootNode().HasError())
		tree.Release()
	}
}
```

gotreesitter's tree for `{T={r:x}}`:

```
(ERROR (ERROR) (object_assignment_pattern (shorthand_property_identifier_pattern)
  (object (pair (property_identifier) (identifier)))))
```

C tree-sitter (py-tree-sitter 0.25.2, tree-sitter-javascript 0.25.0):

```
(program (statement_block (expression_statement (assignment_expression
  left: (identifier) right: (object (pair key: (property_identifier) value: (identifier)))))))
```

### Trigger

A standalone (or `if`) block whose last statement is a simple `=` assignment
without a trailing `;`, where the right-hand side is an object/array literal
whose last value is an identifier. `{T={r:x}}` is ambiguous up to the closing
`}`: `{T={r:x}}` could start an object pattern (`{T={r:x}} = ...`) or be a
block with an assignment. The C parser keeps both GLR branches and the block
reading wins at `}`/EOF; gotreesitter drops the block branch early.

Related but different: upstream issue #111 (`{a}b=c`, fixed in v0.20.6 via
the external scanner's ASI handling).

### Findings

- The external scanner is not at fault. Wrapping `Language.ExternalScanner`
  and logging every `Scan` call shows the same ASI decisions as the C scanner
  (`}` with `_automatic_semicolon` valid → true). The divergence is in the
  GLR core.
- `GOT_GLR_FOREST=0` (production parser) behaves the same.
- `GOT_GLR_FOREST_RECOVER=1` / `SetGLRForestRecover(true)` fixes the minimal
  cases, but all 14 failing top-level statements of `yarn-4.7.0.cjs` still
  fail, e.g. the delta-minimised
  `function (){("",()=>{e}),T={r:e}}`. It is also a process-global switch
  (race-prone with parallel workers), so it is not an option for bag.
- Rewriting the source (inserting `;`) would shift byte offsets and break
  node locations, so a bag-side preprocessing workaround is not acceptable.

### Next steps

Report upstream with the reproducer above; bump gotreesitter once fixed and
re-run the sourcebot comparison (`cjs` row).

## Groovy: reference-grammar parse quirks

**Status:** mostly done. `internal/extract/langs/groovy.go` extracts Groovy
and Gradle against gotreesitter's Groovy grammar (which has a different tree
shape from Graphify's `amaanq/tree-sitter-groovy` 0.1.2) and matches Graphify
on classes, interfaces, methods, constructors, heritage (including
`extends a.b.Base<T> implements X, Y`), class annotations, imports, calls and
the Spock line-based fallback.

Still different: places where the *reference* grammar misparses and
Graphify faithfully reports the misparse. bag parses these correctly, so it
emits the real method where Graphify emits a wrong one:

| Source | Graphify | bag |
|---|---|---|
| `String name` then `A(String n) {...}` (short field name vs class name) | method `.name()` | constructor `.A()` |
| `static String e` then `@Override String toString() {}` | method `.e()` | method `.toString()` |
| `String a = "z"` then `A() { go() }` | constructor dropped | constructor `.A()` |

Whether the field swallows the next declaration depends on token lengths
in tree-sitter-groovy's error recovery (`String nm` + `A(...)` is swallowed,
`String x` + `int y` + `A()` is not), so reproducing it would mean emulating
that parser's recovery. Not worth it unless a real project shows the gap.
