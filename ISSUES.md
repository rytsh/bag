# Known issues

Open parity gaps against Graphify that are not fixed in bag itself. Each entry
has a reproducer so it can be re-checked after a dependency bump.

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
