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

## Extractor gaps found in the 2026-11 audit, not fixed yet

Each was reproduced on a small fixture; none changes an existing id.

- **TypeScript:** `export default () => {}`, `export const Foo = class { m() {} }`
  (the class's methods) and object-literal methods (`const o = { m() {} }`)
  produce no node; `export default class { hi() {} }` files `hi` under the
  file instead of the class; functions in a `namespace` are contained by the
  file, not the namespace.
- **Kotlin:** calls inside `init {}` and secondary `constructor(...)` are
  dropped (no node for the constructor); `typealias` and lambdas assigned to
  a top-level `val` are missing; an extension function records no reference
  to its receiver type.
- **Java:** calls in `static {}` initializers and record compact
  constructors are dropped.
- **Python:** calls in class-level assignments (`registry = build()`) have no
  edge; `name = lambda ...` produces no node; a file nested deeper than about
  256 levels parses as one ERROR node and loses its outer declarations
  without a warning.
- **Rust:** inline `mod inner { ... }` blocks are not scoped, so a type there
  merges with a same-named type outside.
- **C++:** an out-of-line definition (`void Foo::bar() {}`) takes the
  declaration's start line and the definition's end line, and is labelled
  `bar` rather than `.bar()`.
- **Swift:** the file's `contains` edge for a type points at its extension's
  line instead of the declaration.
- Python/Kotlin cross-file `uses` edges are appended while ranging over a
  map; when two share source, target and relation, which location survives
  is not pinned (not observed to vary in practice).
