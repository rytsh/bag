# AGENTS.md

Guidance for AI agents working on this repository.

## What this is

`bag` is a pure-Go, Graphify-compatible code knowledge-graph builder. The
contract is **byte-level compatibility with Graphify's graph.json and node
IDs**. Many functions are adapted from Graphify's Python sources; keep the
behaviour identical unless there is a deliberate, documented reason.

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
- When porting Graphify logic, note the source function in the doc comment
  ("Adapted from Graphify's X (Apache-2.0)").
- Wrap errors with `fmt.Errorf("...; %w", err)`.

## Checking parity

```sh
go test ./...                       # includes the Graphify golden parity test
make parity DIR=/path/to/project    # needs `graphify` installed
```

`scripts/compare.py` prints node/edge set differences per file extension.
