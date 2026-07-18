# Stateless Polyglot Sprouts: Go & TypeScript Executors (Issue)

This plan details the implementation of a stateless, decoupled sprout architecture. 

We will migrate the LLM reasoning loop from the Python container to the host **Go Stem**, converting Sprout containers into purely stateless, "dumb" tool execution terrariumes. We will build **Go** as the ultra-lightweight default general-purpose executor, and **TypeScript/Node** as the rich ecosystem executor, communicating over a universal stdin/stdout JSON-RPC protocol.

---

## 1. Architectural Design

```
             [ User Chat / IDE MCP Call ]
                          │
                          ▼
┌──────────────────────────────────────────────────┐
│              Go Stem Orchestrator                │
│  - Runs host-side ReAct Loop (LLM Chat calls)    │
│  - Sprouts Docker Sprout session container       │
│  - Pipes structured tool calls to Stdin          │
│  - Reads JSON tool execution outputs from Stdout │
└──────────────────────────────────────────────────┘
                          │
                          ▼
┌──────────────────────────────────────────────────┐
│      Stateless Ephemeral Sprout Container        │
│  - Reads JSON tool calls on Stdin                │
│  - Runs filesystem / git / shell command locally │
│  - Writes JSON result to Stdout                  │
└──────────────────────────────────────────────────┘
      /                   |                   \
     ▼                    ▼                    ▼
[ Go Sprout ]       [ TS Sprout ]       [ Python Sprout ]
(Default / Tiny)    (Node Ecosystem)    (Python Substrates)
```

### Stdin/Stdout JSON Tool Protocol Contract
All Sprout executors will adhere to a strict universal interface using **camelCase** naming conventions for all tools and parameters:

**Input (from Go Stem to Sprout `stdin`):**
```json
{
  "tool": "readFile",
  "arguments": {
    "path": "main.go"
  }
}
```

**Output (from Sprout `stdout` to Go Stem):**
```json
{
  "status": "success",
  "output": "package main\n..."
}
```

---

## 2. Go Stem Host-Side ReAct Loop

We will move the LLM agent loop from `tendrilloop.py` onto the host Go Stem orchestrator:
1.  **Shared LLM Client:** Extract the `callLLM` HTTP client logic from `chronicler.go` into a reusable internal package `github.com/opentendril/opentendril/roots/llm`.
2.  **Agent Loop:** Implement a Go-native agent loop in `internal/orchestrator/agent.go`. The loop binds tools (like `readFile`, `writeFile`, `gitCommit`, `execCommand`), formats tool definitions for the LLM, and processes LLM responses.
3.  **Interactive Session Containers:** Instead of sprouting a Docker container for every single tool call (which adds cold start latency), the Go Stem will sprout **one container per task session** using interactive pipes (`docker run -i --rm`). It will keep the container alive, sending JSON lines on `stdin` and reading JSON lines from `stdout` sequentially until the task is done.
4.  **Dynamic Tool Discovery:** At startup, the Go Stem will write a `listAvailableTools` command to the Sprout container. The container will return a list of JSON-defined tool schemas it supports. This allows the host to dynamically bind custom tools depending on the language/framework of the Sprout container.

---

## 3. Grafting Metaphor in OpenTendril Architecture

In botany, **grafting** is the act of joining tissues from two different plants so they grow together as a single organism. In OpenTendril, we map this to two concepts:

1.  **Stem Grafting:** Connecting a local Go Stem on a developer's machine to a remote Go Stem running on an external server or VM. This allows the local workspace to command remote execution terrariumes or delegate workloads to heavier build environments transparently.
2.  **Genotype Grafting:** Dynamically joining two genotypes (personae) or modular plasmids (skills) into a hybrid genotype (e.g., grafting a `frontendDev` persona with a `postgresDBA` persona to handle full-stack workflows).

---

## 4. Proposed Changes

### Component: Go Stem Orchestrator

#### [NEW] [llm/client.go](../roots/llm/client.go)
*   Extract the provider resolution, specification parsing, and HTTP POST chat completions calling code from `chronicler.go`.

#### [NEW] [orchestrator/agent.go](../cmd/stem/internal/orchestrator/agent.go)
*   Implement the Go-native ReAct loop.
*   Binds tool definitions to the LLM.
*   Pipes tool arguments to the active Sprout container's stdin.

#### [MODIFY] [orchestrator/docker.go](../cmd/stem/internal/orchestrator/docker.go)
*   Update `RunTendril` to launch the Sprout container in interactive mode (`-i`).
*   Establish persistent stdin/stdout readers.
*   Query `listAvailableTools` on startup to bind capabilities dynamically.
*   Implement container image selection logic based on Detected Substrate:
    *   Go project $\rightarrow$ `opentendril-go:latest` (Default).
    *   JS/TS project $\rightarrow$ `opentendril-typescript:latest`.
    *   Python project $\rightarrow$ `opentendril-python:latest`.

---

### Component: Sprout Executors

#### [NEW] [sprouts/go/Dockerfile](../sprouts/go/Dockerfile)
*   Build a minimal Alpine image containing the Go executor binary. (Target size: < 20MB).

#### [NEW] [sprouts/go/main.go](../sprouts/go/main.go)
*   Read JSON tool calls from `stdin` in a loop.
*   Implement native Go file tools (`readFile`, `writeFile`, `listFiles`), git tools (`gitCommit`, `gitDiff`), and command execution (`execCommand`).
*   Implement `listAvailableTools` returning the supported tools.
*   Write output as JSON to `stdout`.

#### [NEW] [sprouts/typescript/Dockerfile](../sprouts/typescript/Dockerfile)
*   Build a Node-alpine base image (Target size: < 150MB).

#### [NEW] [sprouts/typescript/package.json](../sprouts/typescript/package.json)
*   Include dependencies for `simple-git` and `execa`/`zx`.

#### [NEW] [sprouts/typescript/src/main.ts](../sprouts/typescript/src/main.ts)
*   Implement TypeScript tool executor reading stdin JSON lines and executing tools (`readFile`, `writeFile`, `execCommand`, `listAvailableTools`).

#### [MODIFY] [sprouts/python/Dockerfile](../sprouts/python/Dockerfile)
*   Strip out LangChain and LLM-related libraries.
*   Convert python worker into a dumb executor that only handles Python-specific tooling (`runPytest`, `runPip`) and implements `listAvailableTools`.

---

## 5. Verification Plan

### Automated Tests
*   **Go Orchestration Tests:** Run `go test ./...` in the Go stem directory to verify agent loop parsing and HTTP LLM mocking.
*   **Docker Integration Tests:** Run integration scripts to sprout `opentendril-go` and `opentendril-typescript` containers, feed mock JSON tool calls to stdin, and assert correct stdout JSON responses.

### Manual Verification
1.  Launch `tendril chat`.
2.  Ask a question requiring file edits.
3.  Confirm that Go Stem executes the LLM loop on the host, sprouts the appropriate Docker container, pipes tool commands, and returns the finished edits.
