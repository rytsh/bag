# AGENTS.md

Guidance for AI agents working on this repository.

## What this is

`bag` is a pure-Go code knowledge-graph builder. It started as a port of
Graphify and much of its code is adapted from Graphify's Python sources, but
it is its own tool now: Graphify compatibility is **not** a requirement. Change
the graph whenever that makes it more useful to the people and agents reading
it, and say so in the change.

What stays stable is bag's own contract with its consumers: node IDs, the
`graph.json` keys they already read, and edge relations. Adding a key is
fine; renaming or removing one, or changing how IDs are made, is a breaking
change and needs a major version.

## Rules

- Pure Go only: `CGO_ENABLED=0` must keep working. Tree-sitter goes through
  `github.com/odvcencio/gotreesitter` via `internal/extract/tsx`.
- Follow the rakunlabs layout: `cmd/bag` is thin; logic lives in `internal/`,
  and dependencies point downward (pipeline → extract/graph/cluster/...).
- Node IDs only come from `internal/ids` (`MakeID`/`NormalizeID`). Never build
  IDs by hand.
- A new language needs one extractor (`internal/extract/langs/<lang>.go`),
  registration in `langs.go` and fixtures. Prefer the generic engine
  (`internal/extract/generic`) with hooks over a bespoke walker.
- Code adapted from Graphify keeps its attribution in the doc comment
  ("Adapted from Graphify's X (Apache-2.0)"); that is a licence obligation,
  not a promise to behave the same.
- Bump `store.ASTVersion` whenever extractor output changes, so cached
  extractions and consumers that record the engine version rebuild.
- Wrap errors with `fmt.Errorf("...; %w", err)`.

## Regression baselines

```sh
go test ./...                       # includes the golden extraction tests
```

`testdata/*_golden.json` were captured from Graphify and now serve as bag's
regression baselines: they catch an unintended change to node IDs, labels,
locations or edges. An intended change updates the golden file in the same
commit, with the reason in the message.

`make parity DIR=path` (needs `graphify` installed) is still there for
comparing against Graphify out of interest; a difference it reports is not
a bug by itself.
