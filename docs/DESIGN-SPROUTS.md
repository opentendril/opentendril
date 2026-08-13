# Component: Sprouts

The polyglot stateless tool-execution container fleet the Stem drives over a stdin/stdout JSON protocol.

## Purpose

This component provides stateless execution bodies for OpenTendril's tools. Protocol Sprouts execute tool calls received over the JSON tool protocol, while toolchain images provide deterministic build/test environments. LLM cognition remains external in the Mycorrhizae; the Stem deterministically orchestrates lifecycle and tool execution and reaches Mycorrhizae through Roots. Sprouts do not run the LLM reasoning loop.

## Responsibilities

### Protocol Executors (`sprouts/go/main.go`, `sprouts/typescript/src/main.ts`, `sprouts/python/src/main.py`, `sprouts/node/src/main.ts`)
*   **Does:** Read JSON tool calls sequentially from `stdin`.
*   **Does:** Run local filesystem, git, and shell command tools (e.g., `readFile`, `writeFile`, `gitCommit`, `execCommand`).
*   **Does:** Return JSON-formatted execution results to `stdout`.

### Executor Images
*   **Go** serves as the minimal default executor (`opentendril-go`).
*   **TypeScript/Node** provides a rich ecosystem executor for JavaScript-heavy projects. Two images share the same executor source (`sprouts/typescript/src/main.ts`, built via `sprouts/typescript/tsconfig.json`):
    *   **`opentendril-typescript`** (`sprouts/typescript/Dockerfile`) runs on a fixed `node:22-alpine` base with a direct `ENTRYPOINT ["node", ...]`.
    *   **`opentendril-node`** (`sprouts/node/Dockerfile`) runs on `debian:bookworm-slim` with `nvm` installed. Its `entrypoint.sh` detects a `.nvmrc` or `.node-version` file in the target project and dynamically installs/switches to that project's required Node version via `nvm install`/`nvm use` before executing.
*   **Python** serves Python substrates (`opentendril-python`).

### Toolchain Images (`toolchains/go-verifier/Dockerfile`, `toolchains/go-fuzz/Dockerfile`)
*   **Does:** Provide a complete language toolchain (e.g., the Go compiler) available at container runtime.
*   **Does:** Start idle (`tail -f /dev/null`) to allow Conductor to exec commands directly into them via `terrarium.Terrarium.Run`.

### General (Does Not)
*   **Does not:** Run the LLM ReAct loop (this is handled by the host).
*   **Does not:** Import or link any OpenTendril Go package (fully decoupled).
*   **Does not:** Persist state across invocations.
*   **Does not:** Speak the tool protocol at all (applies strictly to toolchain images like `toolchains/go-verifier` and `toolchains/go-fuzz`).

## Public interface

Because the protocol executors are fully decoupled leaf programs without Go exports, their public interface is the Stdin/Stdout JSON Tool Protocol Contract.

**Input (Stdin):**
```json
{
  "tool": "toolName",
  "arguments": {
    "argKey": "argValue"
  }
}
```

**Output (Stdout):**
```json
{
  "status": "success",
  "output": { ... },
  "error": "error message if applicable"
}
```

**Standard Tool Set:**
The `sprouts/go/main.go` executor implements a base set of tools: `readFile`, `writeFile`, `listFiles`, `gitCommit`, `gitDiff`, `execCommand`, and `listAvailableTools`. `sprouts/typescript/src/main.ts` (shared source for both the TypeScript and Node images) implements the identical set. This base set is not uniform across executors — `sprouts/python/src/main.py` additionally implements two Python-specific tools, `runPytest` and `runPip`. Callers can enumerate an executor's actual tools at runtime via `listAvailableTools`; the per-language divergence is called out under Limitations.

## Dependencies

*   **Fan-out:** None. Each executor is a standalone program importing no OpenTendril package. They are fully decoupled leaves that rely only on their respective language standard libraries and runtime dependencies (e.g., Node.js for TypeScript/Node).
*   **Fan-in:** Coupling is entirely via runtime image invocation, not a Go dependency edge. The Conductor's terrarium (`cmd/stem/internal/conductor/docker.go`) builds and invokes the images (`opentendril-go:latest`, `opentendril-typescript:latest`, `opentendril-python:latest`, `opentendril-node:latest`) and execs deterministic commands into `toolchains/go-verifier` and `toolchains/go-fuzz` for verifier/Macrophage steps.

## Limitations

*   The tool sets are static and implemented independently per language (three source implementations today — Go, Python, and the shared TypeScript/Node source), and they diverge: `sprouts/python/src/main.py` exposes `runPytest` and `runPip` tools that `sprouts/go/main.go` and `sprouts/typescript/src/main.ts` do not. While `listAvailableTools` allows dynamic discovery, this divergence is intentional where a tool is genuinely language-specific (Python sprouts run Python test/dependency tooling that a Go or TypeScript sprout has no use for). Each executor has its own unit test suite (`sprouts/go/main_test.go`, `sprouts/python/src/testmain.py`, `sprouts/typescript/test/main.test.ts`) covering its own tools in isolation.


