# Codebase Assessor: Thigmotropism AST Ingestion (Issue #54)

This plan details the implementation of the **Codebase Assessor**, which parses Abstract Syntax Trees (AST) to generate a compressed **Repo Map** (packages, classes, functions, and signatures) of the workspace. This map is dynamically injected as a Plasmid, giving Sprouts architectural context without bloating their prompts.

---

## 1. Botanical Metaphor: Thigmotropism

In botany, **Thigmotropism** is a plant's sense of touch, allowing tendrils to feel the shape of physical structures (like a trellis) before wrapping around them.

The **Codebase Assessor** provides this tactile sense. Before a Sprout executes code changes on a large repository, it is injected with a **Repo Map Plasmid** (`repomap.md`), allowing the agent to "feel" the architecture of the codebase and locate functions without reading entire files upfront.

---

## 2. Parser Architecture: CGO-Free Native Go Parsers

To keep the OpenTendril binary cross-compilable, portable, and extremely lightweight (avoiding complex CGO bindings for Tree-sitter which require gcc and external C libraries), we will implement a native Go parser suite:

1.  **Go Parser:** Uses Go's native standard library packages `go/parser` and `go/ast`. This is robust and extracts all structs, interfaces, methods, and functions.
2.  **TypeScript / JavaScript Parser:** A signature lexer that parses export interfaces, classes, types, functions, and class methods.
3.  **Python Parser:** A signature lexer that parses class declarations, function definitions (`def`), and signatures.

### Repo Map Output Format (`repomap.md`)
The generated map is formatted as a compact hierarchical list:

```markdown
# Repository Architecture Map (Thigmotropism)

## [Go] cmd/stem/internal/orchestrator/docker.go
- type DockerOrchestrator struct
  - func (d *DockerOrchestrator) RunTendril(ctx context.Context, taskPrompt string) (string, error)
  - func (d *DockerOrchestrator) resolveImageName(workspace string) string

## [TypeScript] sprouts/typescript/src/main.ts
- class TypeScriptExecutor
  - method executeTask(task: Task): Promise<Result>

## [Python] sprouts/python/src/main.py
- def runPytest(test_path: str) -> dict
```

---

## 3. Dynamic Terrarium Injection Lifecycle

1.  When `RunTendril` starts, before the Sprout boots:
    *   Go Stem runs the AST parser over the resolved workspace (`mountPath`), ignoring `.git`, `node_modules`, `vendor`, and `.venv` directories.
    *   Generates the compressed `repomap.md` text.
2.  Go Stem writes `repomap.md` to `mountPath/.tendril/genome/repomap.md`.
3.  Since it is staged under `.tendril/genome/`, when the Sprout boots, the host agent automatically reads it as part of the active genome prompts.
4.  Once the Sprout finishes execution, the terrarium worktree is cleaned up, safely discarding the temporary repo map from disk.

---

## 4. Proposed Changes

### Component: Go Stem Orchestrator

#### [NEW] [orchestrator/repomap.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/internal/orchestrator/repomap.go)
*   Implement `GenerateRepoMap(dir string) (string, error)`.
*   Implement `parseGoFile(path string) ([]string, error)` using `go/parser` and `go/ast`.
*   Implement `parseTypeScriptFile(path string) ([]string, error)` extracting exports and class signatures.
*   Implement `parsePythonFile(path string) ([]string, error)` extracting classes and defs.

#### [MODIFY] [orchestrator/docker.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/internal/orchestrator/docker.go)
*   Inside `RunTendril`, after staging Genotype plasmids and before starting Docker:
    *   Call `GenerateRepoMap(mountPath)`.
    *   Write the output to `mountPath/.tendril/genome/repomap.md` to inject the Thigmotropism Plasmid.

#### [NEW] [cmdrepomap.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/cmdrepomap.go)
*   Implement `tendril repomap` CLI command to print the repository map to stdout.

#### [MODIFY] [main.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/main.go)
*   Register `repomap` subcommand.

---

## 5. Open Questions

> [!IMPORTANT]
> 1.  **File Size Threshold:** In very large codebases, even the signature map can grow too large for a prompt. Should we cap the file parsing limit (e.g., skip parsing files in deep directories or only parse files with less than 2000 lines), or should the Meristem planner dynamically request AST mapping for specific directories?
> 2.  **Excluding Test Files:** Should we skip generating AST maps for test files (e.g. `*_test.go`, `*.test.ts`, `test_*.py`) to keep the map focused strictly on the core application APIs?

---

## 6. Verification Plan

### Automated Tests
*   **AST Parser Tests:** Verify that Go, TypeScript, and Python parsers correctly extract signatures from mock files.
*   **Injection Lifecycle Tests:** Verify that `repomap.md` is generated and staged inside a temporary terrarium directory.

### Manual Verification
1.  Run `tendril repomap` inside the `opentendril/core` repository.
2.  Verify that it outputs a clean, structured signature list of the Go/TS/Python source files.
