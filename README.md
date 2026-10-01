# bağ  🧶

[![License](https://img.shields.io/github/license/rytsh/bag?color=blue&style=flat-square)](https://raw.githubusercontent.com/rytsh/bag/main/LICENSE)
[![Coverage](https://img.shields.io/sonar/coverage/rytsh_bag?logo=sonarcloud&server=https%3A%2F%2Fsonarcloud.io&style=flat-square)](https://sonarcloud.io/summary/overall?id=rytsh_bag)

`bag` turns a codebase (plus its docs, papers and images) into a **knowledge
graph** you can explore locally or give to your AI coding assistant. It is a
pure-Go reimplementation of
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
   Elixir, Julia, Astro, Solidity, VB.NET and Markdown, plus package manifests (`go.mod`,
  `pyproject.toml`, `Cargo.toml`, `pom.xml`). About 100 more languages are
  covered through tree-sitter tags queries. Run `bag languages` for the full list.

## Install

### Prebuilt binaries (no Go required)

Download an archive from [the latest GitHub release](https://github.com/rytsh/bag/releases/latest):

| Platform | Archive |
| --- | --- |
| macOS (Apple Silicon / Intel) | `bag_<version>_darwin_arm64.tar.gz` / `bag_<version>_darwin_amd64.tar.gz` |
| Linux (ARM64 / x86-64) | `bag_<version>_linux_arm64.tar.gz` / `bag_<version>_linux_amd64.tar.gz` |
| Windows (ARM64 / x86-64) | `bag_<version>_windows_arm64.zip` / `bag_<version>_windows_amd64.zip` |

Verify the archive's SHA-256 against `checksums.txt` from the same release
(`shasum -a 256 <archive>` on macOS, `sha256sum <archive>` on Linux, or
`Get-FileHash <archive> -Algorithm SHA256` in PowerShell). Extract it and put
`bag` (Windows: `bag.exe`) in a directory on your `PATH`, then run `bag version`.
On macOS/Linux, `~/.local/bin` is a user-local option; add it to your `PATH`
if needed. No Python, Go installation or API key is needed for code extraction.

### From source

```sh
go install github.com/rytsh/bag/cmd/bag@latest
# or, from a clone of this repository:
make build
```

### Let your AI agent set it up

Paste this into your coding assistant, opened in the repository you want to explore:

```text
Set up bag (https://github.com/rytsh/bag) for this repository.
Read its README for the current commands. Detect my OS and CPU architecture,
download the matching binary archive and checksums.txt from the latest official
GitHub release, and verify the archive's SHA-256 before extracting it.
Install the binary in a user-writable directory on PATH (no sudo); ask before
overwriting an existing installation or changing shell configuration.
Run `bag version`, then `bag extract .` from this repository's root without
--semantic: keep extraction local and do not configure an API key.
Ask which assistant integration I want, then run
`bag install --platform <agents|claude|opencode|cursor>`.
Leave AGENTS.md and CLAUDE.md untouched; ask before replacing any existing bag skill
or rule. Do not install Git hooks or change MCP settings without asking.
If I want MCP, use `bag install --platform <claude|opencode|cursor> --mcp`
to configure the binary as a project-local stdio server alongside the skill.
Run `bag stats` and show one useful `bag query` using real symbols from this
repository. Report the files you created or changed.
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

Run these commands from the repository root. `AuthService`, `Database` and
`RateLimiter` are examples; use names from your own code.

For settings shared by `extract`, `update` and `watch`, keep exclusions in
`.bagignore` and set the output directory with `BAG_OUT_DIR` or `bag.yaml`.
`update` does not inherit the flags from a previous `extract` invocation.

## When is it useful?

- **Onboarding and architecture:** find central symbols, module groups and
  connections across files in `GRAPH_REPORT.md` and `graph.html`.
- **Tracing relationships:** use `path` and `explain` to explore calls, imports
  and type relationships before reading the relevant source.
- **AI coding assistants:** `bag query "<question>" --budget 1500` returns scoped graph context
  with source locations; `serve` exposes it through MCP.
- **Repeated local use:** `update` reuses the AST cache; `watch` keeps the graph
  fresh while you work.

`bag query` is keyword/symbol search plus graph traversal, **not an LLM-written
answer**. Specific symbol names work best. The graph complements grep, source
reading and tests; it is not a complete runtime call graph and cannot prove
that a change is safe. Code extraction and queries need no API key. Only
`extract --semantic` enables the optional model-backed pass.

To install an assistant skill, run `bag install --platform agents`
from the repository root (or choose `claude`, `opencode` or `cursor`). This
configures how the assistant uses the graph; it does **not** install the binary.
The default `agents` target writes `.agents/skills/bag/SKILL.md`; Claude and
OpenCode use `.claude/skills/bag/SKILL.md` and `.opencode/skills/bag/SKILL.md`.
Cursor gets `.cursor/rules/bag.mdc`. No target edits `AGENTS.md` or `CLAUDE.md`.

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
| `serve` | Project MCP server over stdio (queries + AST builds); `--read-only` disables builds. `--transport http` provides read-only MCP + REST API |
| `install --platform agents\|claude\|opencode\|cursor\|all [--mcp]` | Install a skill/rule; optionally configure project-local stdio MCP for a supported client |
| `languages` | Supported languages and extensions |

## Go library

Go services can embed the AST pipeline directly, without invoking the `bag` or
Graphify CLI:

```go
result, err := bag.Build(ctx, repoPath, bag.BuildOptions{
    Excludes: []string{"testdata/", "vendor/"},
    Force:    true,
})

merged, err := bag.MergeGraphs(ctx, mergedPath, graphPaths...)
```

Import `github.com/rytsh/bag`. `Build` writes the standard
`graphify-out/{graph.json,GRAPH_REPORT.md,graph.html}` layout and performs no
network or LLM calls. `MergeGraphs` applies Graphify-compatible repo prefixes,
community offsets, global external stubs, shared-type links and parked
cross-repository call resolution. Persist `bag.EngineVersion` beside validated
graphs if the host application needs to invalidate outputs after an extractor
upgrade.

## MCP

For frequent queries, MCP keeps the graph in memory instead of reloading it
for each CLI invocation. Install it together with the assistant skill/rule:

```sh
bag install --platform opencode --mcp   # OpenCode V2 project config
bag install --platform claude --mcp     # .mcp.json
bag install --platform cursor --mcp     # .cursor/mcp.json
```

Run from the project root, using the installed `bag` binary (not `go run`).
The config records the binary's absolute path, project root and graph path;
rerun installation if you move the binary or project. OpenCode installation
reuses an existing project `opencode.json(c)` (including `.opencode/` configs),
or creates `opencode.json`. Other settings, servers and JSONC comments are
preserved. A different existing `bag` entry requires `--replace-mcp`.
`--platform all --mcp` configures all three clients; the generic `agents`
target is skill-only because there is no universal MCP config location.
No installation edits `AGENTS.md` or `CLAUDE.md`.

Restart/reconnect the assistant and approve the server if prompted. The client
starts `bag serve` itself over stdin/stdout; no port or separate terminal is
needed. The skill prefers MCP tools when connected and uses CLI otherwise.

### Server modes and tools

```sh
bag serve --root /path/to/project            # stdio, queries + local AST builds
bag serve --read-only --graph /path/to/graph.json
bag serve --transport http --host 0.0.0.0 --api-key "$SECRET"
```

Query tools use Graphify's names (`query_graph`, `get_node`, `get_neighbors`,
`get_community`, `god_nodes`, `graph_stats`, `shortest_path`), so existing
assistant configs keep working. Writable stdio servers also provide:

- **`extract_graph`**: first AST build; writes graph, report, HTML and cache.
- **`update_graph`**: rebuild after code changes, reusing the per-file AST cache.

Both operate only on the fixed `--root` project and configured output directory
inside it. They run the Go pipeline directly, without spawning `bag` or making
LLM calls. `.gitignore`, `.bagignore` and `.graphifyignore` are honored. Tool
arguments cannot select another project or output path. `no_viz: true` skips
HTML. Both refuse to replace a larger graph with a smaller one unless
`force: true` is explicitly passed (e.g. after deleting source files).

The writable stdio server can start before `graph.json` exists: call
`extract_graph` first, then query it. Builds do not run automatically on startup
or source changes. `--read-only` and HTTP expose no build tools and require an
existing graph. Read-only mode can serve graphs outside the project root.

The HTTP server also exposes `/healthz` and
`/api/v1/{stats,query,path,explain}`. The graph file is hot-reloaded when it
changes, including after MCP builds. Keep HTTP on loopback unless remote access
is intended; use an API key and appropriate network protection if exposing it.

## Configuration

Config is loaded with [chu](https://github.com/rakunlabs/chu). The first
matching `bag.{toml,yaml,yml,json}` is used in this order:

1. current directory
2. `os.UserConfigDir()/bag` (`~/.config/bag` on typical Linux systems)
3. `/etc/bag`
4. `/etc`

`CONFIG_FILE_BAG` or `CONFIG_FILE` selects an explicit file instead. `BAG_*`
environment variables are applied last and override file values.

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
