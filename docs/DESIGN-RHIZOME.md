# Component: Rhizome — the plant's code-sensing (Thigmotropism): AST symbol index, repo-map generation, and pluggable project-memory backends.

## Purpose

`cmd/stem/internal/rhizome` is the self-contained leaf that implements **Thigmotropism** and **project memory**. It walks a Substrate, extracts structural symbols (functions, types, classes, imports) through a docker-free parser stack, stores them in an encrypted local SQLite index, and renders a compact markdown **Repo Map**. The same package also owns a **MemoryBackend** surface for durable project memories (SQLite FTS default; optional Pinecone and Weaviate HTTP backends) and a markdown **Memory Map**. It owns parsing, indexing, encryption-at-rest for local stubs/content, and map rendering only — the Conductor and CLI adapters open stores, stage plasmids, and choose backends.

## Responsibilities

**Does:**

- Walk a repository tree, skip noise directories (`.git`, `node_modules`, `vendor`, `venv`, build outputs, etc.), and parse supported source files (`scanner.go`).
- Skip unchanged files on re-scan by content SHA-256 hash recorded in the index (`ScanRepository` + `IndexStore.GetFile`).
- Provide a first-match parser stack: native `go/ast` for Go, in-process pure-Go tree-sitter for Python/JavaScript/TypeScript/TSX, regex fallback for the same non-Go extensions (`parser.go`, `treesitter.go`).
- Persist symbols and file hashes in SQLite FTS5 with AES-GCM encryption of `stubContent` at rest (`schema.go`, `crypto.go`).
- Search symbols and render a markdown Repo Map grouped by file path, with `file_context` pseudo-symbols sorted first within each file (`repomap.go`).
- Store, list, search, and delete project **memories** (title/category/content/tags) via `MemoryBackend`; render a category-grouped Memory Map (`schema.go`, `memorymap.go`).
- Select a memory backend from env (`LoadMemoryConfig` / `OpenMemoryBackend`): `sqlite` (default), `pinecone`, or `weaviate`.

**Does not:**

- Own CLI, REST, or MCP surface wiring (`tendril repomap`, `tendril memory *` live in `cmd/stem`; no governed Core capability for rhizome today).
- Stage plasmids into terraria or decide when to inject maps — that is Conductor (`rhizomefacade.go`, `docker.go`).
- Commit-filter runtime artifacts — Conductor owns the “do not commit rhizome.key / rhizome.db / repomap.md” list.
- Call external embedding models. Pinecone vectors are a local character-bucket hash (`textVector` in `pinecone.go`); Weaviate search is BM25 GraphQL, not dense vectors.
- Parse languages outside the Go + py/js/ts/tsx family, or claim full language semantics for regex fallback paths.
- Delete index rows for files removed from disk (incremental scan only upserts/skips files still present).

## Public interface

| Symbol | Role |
| --- | --- |
| `ScanStats` | Counters from a scan: `FilesParsed`, `FilesSkipped`, `SymbolsStored`. |
| `ScanRepository` | Walk `root`, parse with the given (or default) parsers, upsert symbols/files into `IndexStore`. |
| `Parser` | `Parse(path, content) ([]Symbol, error)` + `Supports(path) bool`. |
| `DefaultParsers` | Ordered stack: `GoParser`, `TreeSitterParser`, `RegexParser`. |
| `GoParser` | `go/parser` + `go/ast` structural stubs (package `file_context`, types/structs/interfaces, functions/methods). |
| `RegexParser` / `NewRegexParser` | Line-oriented fallback for Python and JS/TS family extensions. |
| `TreeSitterParser` / `NewTreeSitterParser` | In-process gotreesitter engine for py/js/ts/tsx; degrades to regex on size/error/panic. |
| `GenerateRepoMap` | Markdown repo map from `IndexStore.SearchSymbols` (query `*` or empty lists; FTS otherwise). |
| `GenerateMemoryMap` | Markdown memory map from `MemoryBackend.SearchMemories`, grouped by category. |
| `FileRecord` | Indexed file row: repository name, path, content hash, last modified. |
| `Symbol` | Indexed symbol: name, type, file path, line span, stub content (plaintext in memory; encrypted in SQLite). |
| `Memory` | Project memory payload (JSON tags camelCase: `repositoryName`, `sessionId`, …). |
| `IndexStore` | Close, file get/upsert, symbol delete-for-file / upsert / search. |
| `MemoryBackend` | Store / list / search / delete memories. |
| `MemoryConfig` / `LoadMemoryConfig` | Backend name and paths/keys from `TENDRIL_*` env vars. |
| `OpenMemoryBackend` | Construct SQLite (needs encryptor), Pinecone, or Weaviate backend. |
| `SQLiteIndexStore` / `OpenSQLiteIndexStore` | Dual-purpose store: implements `IndexStore` and the SQLite `MemoryBackend` methods; requires non-nil `Encryptor`. |
| `PineconeMemoryBackend` / `NewPineconeMemoryBackend` | HTTP upsert/query/list/delete against a Pinecone index URL + API key. |
| `WeaviateMemoryBackend` / `NewWeaviateMemoryBackend` | HTTP objects + BM25 GraphQL against a Weaviate instance (`TendrilMemory` class). |
| `Encryptor` / `NewEncryptor` | AES-GCM encrypt/decrypt of strings (base64 raw ciphertext with prepended nonce). |

Package-level sentinel errors: **none**. Callers match on formatted `fmt.Errorf` strings (e.g. `"index store is required"`, `"encryptor is required"`, `"unsupported memory backend %q"`).

## Dependencies

**Fan-out:** none to other OpenTendril packages (leaf). External modules: `modernc.org/sqlite` (pure-Go SQLite), `github.com/odvcencio/gotreesitter` (+ embedded grammars). Stdlib only otherwise (`go/ast`, `net/http`, `crypto/*`).

**Fan-in:**

- **`cmd/stem`** — `cmdrepomap.go` calls Conductor’s facade (not rhizome directly). `cmdmemory.go` imports rhizome for `LoadMemoryConfig`, `OpenMemoryBackend`, `Memory` CRUD, and key material under `.tendril/rhizome.key`.
- **`internal/conductor`** — `rhizomefacade.go` opens the encryptor + SQLite index, runs `ScanRepository` + `GenerateRepoMap` / `GenerateMemoryMap` for a mount path (hard-coded list limit 2000, query `*`). `docker.go` stages `repomap.md` (required) and optional `memorymap.md` under `.tendril/genome/` before a Sprout grows. Golden tree-sitter tests exercise `NewTreeSitterParser` against fixture output. Runtime-artifact filtering skips committing `rhizome.key`, `rhizome.db*` and `repomap.md`.

## Limitations

- **Parser language coverage is narrow.** First-class: Go (`go/ast`); Python, JavaScript, TypeScript, TSX via tree-sitter; regex covers the same non-Go extensions (including `.jsx`/`.mjs`/`.cjs`/`.mts`/`.cts`). No Rust, Java, Ruby, C/C++, etc.
- **Tree-sitter is size-capped and fail-soft.** Files above 2 MiB, grammar load failures, parse errors, and panics fall back to regex **inside** `TreeSitterParser.Parse` so one bad non-Go file cannot abort the whole scan. **`GoParser` and bare `RegexParser` still return errors**, and `ScanRepository` fails the entire walk on any parser error.
- **Incremental scan is hash-skip only.** Unchanged content is skipped; changed files are re-parsed. There is **no sweep that removes symbols for deleted paths**, so removed files can leave stale index rows until an external purge.
- **SQLite encryption scope is partial.** Only symbol `stubContent` and memory `content` are AES-GCM encrypted. Names, paths, types, tags, categories, and titles are plaintext FTS columns. Remote backends (Pinecone metadata, Weaviate properties) store memory fields **in the clear** over HTTPS.
- **Encryptor is mandatory for SQLite.** `OpenSQLiteIndexStore` rejects a nil encryptor; key lifecycle (`.tendril/rhizome.key` or `OPEN_TENDRIL_INDEX_KEY`) lives in Conductor/CLI adapters, not in this package.
- **Pinecone is not real semantic embedding.** `textVector` folds lowercase bytes into a small fixed-dimension bag (default dimension 8 from `TENDRIL_PINECONE_DIMENSION`). Useful as a wire-shaped backend, not as quality RAG.
- **Weaviate search is BM25**, not vector similarity; class name is hard-coded `TendrilMemory`. Schema creation is not performed by this package (caller/ops must provision the class).
- **Conductor memory-map path always opens SQLite**, not `OpenMemoryBackend`. Pluggable Pinecone/Weaviate backends are reachable from `tendril memory` CLI, not from terrarium map injection.
- **No package tests for `pinecone.go` or `weaviate.go`.** Coverage is SQLite store/scan/maps, Go/regex/tree-sitter parsers, and crypto round-trip.
- **No package-level sentinel errors** for stable `errors.Is` checks across adapters.
- **Repo Map markdown shape is flat** (`# Repo Map` → files → symbol stubs), not the language-tagged hierarchical sketch in the legacy Codebase Assessor plan.

## Design & rationale

**Thigmotropism (AST / Repo Map).** Before a Sprout grows, the plant needs a cheap structural “feel” of the Substrate so it does not load entire files into the Mycorrhizal context window. Rhizome is that sense organ: walk → parse → symbol stubs → FTS index → markdown map. The shipped model keeps the Stem **cgo-free and docker-free**: Go uses the standard library AST; non-Go uses the pure-Go gotreesitter runtime with grammars embedded in-process; regex is the last-resort safety net. First-match precedence in `DefaultParsers` means Go never hits tree-sitter, and tree-sitter never claims a path regex would not also support. Symbol types stay language-shaped (`function`, `method`, `class`, `struct`, `interface`, `type`) plus the synthetic `file_context` import summary sorted first under each file so readers see dependencies before internals. Encryption at rest on stubs keeps proprietary source fragments off the local filesystem in plaintext when the default SQLite index is used.

**Project memory (RAG-shaped backend).** Cross-session decisions and notes live beside the symbol index. `MemoryBackend` is a narrow CRUD+search port; SQLite FTS is the default local implementation sharing the same DB file and encryptor as the symbol index. Pinecone and Weaviate adapters exist for managed/off-box storage and match the historical “abstract vector store” direction, but as built they are **HTTP adapters with simplified retrieval** (hash vectors / BM25), not a full embedding pipeline. `GenerateMemoryMap` turns search hits into a category-sectioned markdown plasmid for genome injection.

**Two facets, one leaf.** AST indexing and project memory share the SQLite schema module, the encryptor, repository-name namespacing, and the Conductor’s single “open rhizome under `.tendril/`” lifecycle. They are different product surfaces (code structure vs operator/Sprout notes) but one operational artifact (`rhizome.db` + key). Whether that remains one package or splits into index vs memory is an open structure question; as built they are deliberately co-located.

Where the legacy plans (`DESIGN-CODEBASE-ASSESSOR`, `DESIGN-RHIZOME-RAG`) and the code diverge, the code is authoritative: parsers live under `cmd/stem/internal/rhizome` (not orchestrator-only helpers); tree-sitter is in-process pure-Go rather than CGO or a container path; `GenerateRepoMap` takes an `IndexStore` + query/limit rather than scanning a directory in one shot; memory is a first-class `MemoryBackend` with optional remote stores, not “symbols only”; map markdown formats and injection paths are those implemented by Conductor + this package, not the illustrative sketches in the plans.
