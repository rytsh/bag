# bag

`bag` turns a codebase (plus its docs, papers and images) into a **knowledge
graph** you can query instead of grepping. It is a pure-Go reimplementation of
[Graphify](https://github.com/Graphify-Labs/graphify): same `graph.json` schema,
same node IDs, same confidence tags, so graphs are interchangeable between the
two tools.

- **Single static binary.** No Python, no CGo, no C toolchain. Tree-sitter runs
  through [gotreesitter](https://github.com/odvcencio/gotreesitter).
- **Local and deterministic for code.** Code is parsed from the AST; no LLM and
  nothing leaves your machine. The optional semantic pass for docs, PDFs,
  images and transcribed video/audio talks to any OpenAI-compatible endpoint
  (OpenAI, Ollama, vLLM, LM Studio, …).
- **Every edge is explained.** Each edge is tagged `EXTRACTED` (stated in the
  source), `INFERRED` (resolved by bag) or `AMBIGUOUS`.
- **Many languages.** Dedicated extractors (ported from Graphify, output checked
  against it) for Go, Python, JavaScript, TypeScript/TSX, Java, C, C++, C#,
  Kotlin, Scala, PHP, Ruby, Lua/Luau, Swift, Objective-C, Rust, Zig, Bash,
  Elixir, Julia and Markdown, plus package manifests (`go.mod`,
  `pyproject.toml`, `Cargo.toml`, `pom.xml`). About 100 more languages are
  covered through tree-sitter tags queries. Run `bag languages` for the full list.

## Install

```sh
go install github.com/rytsh/bag/cmd/bag@latest
# or
make build
```

## Quick start

```sh
bag extract .                  # graphify-out/graph.json, GRAPH_REPORT.md, graph.html
bag query "how does auth reach the database"
bag path AuthService Database
bag explain RateLimiter
bag update .                   # fast rebuild, reuses the per-file AST cache
```

Output (same layout as Graphify):

```
graphify-out/
├── graph.json        the full graph (Graphify node_link format)
├── GRAPH_REPORT.md   god nodes, communities, surprising connections, questions
├── graph.html        interactive vis-network view
└── cache/            content-addressed AST (and LLM) cache
```

## Commands

| Command | What it does |
| --- | --- |
| `extract [dir]` | Detect → extract → resolve → cluster → report. `--semantic` adds the LLM pass for docs/papers/images and video/audio transcripts |
| `update [dir]` | Rebuild using the AST cache (only changed files are re-parsed) |
| `watch [dir]` | Rebuild on file changes (debounced) |
| `hook install\|uninstall\|status` | Git hooks (post-commit/checkout/merge) that keep the graph fresh |
| `query "<question>"` | Scored seed search + BFS/DFS expansion, rendered within a token budget |
| `path <A> <B>` | Shortest path between two concepts |
| `explain <name>` | One node and all of its connections |
| `stats` | Node/edge/relation/confidence counts |
| `cluster-only [dir]` | Re-cluster an existing graph.json and rewrite the report/HTML |
| `export html\|graphml\|cypher\|wiki\|obsidian` | Other output formats |
| `diff <old> <new>` | Compare two graph.json files |
| `serve` | MCP server over stdio, or `--transport http` (streamable HTTP + REST API) |
| `install --platform agents\|claude\|opencode\|cursor\|all` | Tell your assistant to use the graph |
| `languages` | Supported languages and extensions |

## MCP

```sh
bag serve                                   # stdio, for local assistants
bag serve --transport http --host 0.0.0.0 --api-key "$SECRET"
```

The tools use Graphify's names (`query_graph`, `get_node`, `get_neighbors`,
`get_community`, `god_nodes`, `graph_stats`, `shortest_path`), so existing
assistant configs keep working. The HTTP server also exposes `/healthz` and
`/api/v1/{stats,query,path,explain}`. The graph file is hot-reloaded when it
changes.

## Configuration

Config is loaded with [chu](https://github.com/rakunlabs/chu) from
`bag.{yaml,toml,json}` and `BAG_*` environment variables:

| Env | Default | Purpose |
| --- | --- | --- |
| `BAG_LOG_LEVEL` | `info` | log level |
| `BAG_OUT_DIR` | `graphify-out` | output directory |
| `BAG_WORKERS` | CPU count | extraction parallelism |
| `BAG_LLM_BASE_URL` | `https://api.openai.com/v1` | OpenAI-compatible endpoint (e.g. `http://localhost:11434/v1` for Ollama) |
| `BAG_LLM_API_KEY` | | API key (never logged) |
| `BAG_LLM_MODEL` | | model name for `--semantic` |
| `BAG_LLM_TOKEN_BUDGET` | `60000` | per-chunk input budget |
| `BAG_LLM_CONCURRENCY` | `4` | parallel LLM requests |
| `BAG_TRANSCRIBE_BASE_URL` / `BAG_TRANSCRIBE_API_KEY` | LLM values | OpenAI-compatible `/audio/transcriptions` endpoint (OpenAI, Groq, LocalAI, speaches…) |
| `BAG_TRANSCRIBE_MODEL` | `whisper-1` | transcription model (`--whisper-model`) |
| `BAG_TRANSCRIBE_LANGUAGE` | | optional ISO-639-1 hint |
| `BAG_TRANSCRIBE_MAX_UPLOAD_MB` | `25` | per-request limit; larger files are split with ffmpeg |
| `BAG_TRANSCRIBE_FFMPEG` | `ffmpeg` on PATH | ffmpeg binary (`-` disables it) |
| `BAG_SERVER_HOST` / `BAG_SERVER_PORT` / `BAG_SERVER_API_KEY` | `127.0.0.1` / `8080` / | `bag serve --transport http` |

Files are filtered with `.gitignore`, `.graphifyignore` and `.bagignore`
(gitignore syntax, `!` negation supported). Secret-looking files (`.env`,
keys, credentials) are always skipped.

With `--semantic`, video and audio files (`.mp4 .mov .webm .mkv .avi .m4v
.mp3 .wav .m4a .ogg`) are transcribed to `graphify-out/transcripts/<stem>.txt`
and then read as documents, like Graphify's Whisper step. The prompt is built
from the AST god nodes (`GRAPHIFY_WHISPER_PROMPT` overrides it). Files the API
accepts are uploaded as-is; other containers and oversized files need
`ffmpeg`, which is run as an external process (bag itself stays pure Go).
Existing transcripts are reused; `--no-transcribe` skips the step.

## Graphify compatibility

`bag` keeps Graphify's contracts:

- **Node IDs.** The same `normalize_id`/`make_id` recipe (casefold + NFKC to a
  fixpoint, Unicode `\w` runs), golden-tested against `graphify.ids`.
- **graph.json.** `node_link_data` with `links`, canonical key order, stable
  sort, `community`, `norm_label`, `_origin`, `confidence_score`.
- **Resolution passes.** File-id canonicalization, collision salting,
  header/impl class merging (C/C++/ObjC), unique-stub rewiring, cross-file
  call resolution with import evidence, language family guards, Go/Java/PHP/
  Python/C# type and import resolution, JS/TS symbol-level imports through
  re-export barrels, receiver-typed member calls (TS/JS, C#, Swift, Ruby),
  Python class/module-qualified calls, submodule imports, `__init__.py`
  re-exports and the ambiguous-module guard, JS/TS and Python
  `indirect_call` (callbacks, dispatch tables), dynamic `import()`,
  receiver-typed member calls for C++, Objective-C, Java and Rust `self`,
  Ruby inherited implicit-self promotion, `unresolved_calls` parking for
  cross-repo merges, Kotlin/Elixir import targets and qualified calls, C#
  interface dispatch (`dispatches_to`), config JSON (`package.json`,
  `tsconfig.json`, ...) and MCP server configs.
- **Deduplication.** Graphify's MinHash/LSH candidate stage is reproduced
  bit-for-bit, so rationale/document/concept merges match.

`testdata/graphify_golden.json` holds Graphify's output for
`testdata/fixtures`, and `testdata/graphify_resolve_golden.json` for the
multi-file `testdata/resolve` corpus; `go test ./internal/extract` fails if bag
drifts from either. For any other directory, run `make parity DIR=path`.

Known gaps are tracked in [ISSUES.md](ISSUES.md): very large minified JS
bundles (a gotreesitter parser bug), a few Groovy constructs the reference
grammar misparses, and an empty `source_location` on stub nodes.

## Layout

```
cmd/bag/                 CLI (into lifecycle, chu config, logi logging)
internal/
  ids/                   node-ID normalization (Graphify-compatible)
  detect/                corpus walk, ignore files, classification
  extract/               pipeline + corpus-level resolution passes
    tsx/                 gotreesitter wrapper
    base/                shared builder/helpers
    generic/             config-driven class/function extractor
    golang/ langs/       language extractors, resolvers, tags fallback
  graph/                 graph model, build, dedup, graph.json I/O
  cluster/               Leiden community detection
  analyze/               god nodes, surprises, questions, cycles, diff
  report/                GRAPH_REPORT.md
  export/                html, graphml, cypher, wiki, obsidian
  query/                 search, traversal, path, explain
  semantic/              LLM pass (OpenAI-compatible, via rakunlabs/ok)
  server/                MCP (stdio + HTTP via rakunlabs/ada)
  pipeline/ store/       orchestration and caches
```

## License

Apache-2.0. `bag` contains code adapted from Graphify (Apache-2.0,
© Safi Shamsi and the Graphify contributors); see [NOTICE](NOTICE).
