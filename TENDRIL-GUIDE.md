# OpenTendril: Material & Architecture Guide

This document is the practical companion to the [`SYNTHETIC-TAXONOMY.md`](SYNTHETIC-TAXONOMY.md). While the taxonomy defines the *biological concepts* of OpenTendril, this guide defines the *engineering materials*. It explains which languages and technologies are used for each layer, and provides a step-by-step guide on how to construct a new Tendril executor.

## The Core Principle

> **The language choice should be made by the process, not the developer.**

Each layer of OpenTendril is constructed from the material that best serves its purpose. No single language dominates the framework. If a task requires manipulating a Python Abstract Syntax Tree, the executor must be Python. If a task requires high-concurrency file orchestration via MCP, the executor should be TypeScript.

---

## Material Mapping Matrix

| Layer | Material | Rationale |
|---|---|---|
| **Stem (Orchestrator)** | **Go** | Single binary, zero-dependency deployment. Goroutines for concurrent Tendril dispatch. Extremely fast startup. |
| **Hormonal Triggers** | **Shell → Go → OPA** | Progressive complexity. Simple gates use Bash. Complex auth/RBAC is compiled into Go. Future declarative policy will use Open Policy Agent (Rego). |
| **Genotypes / Plasmids / Sequences** | **YAML + Markdown** | Configuration and prompt data are not code. They must be human-readable, editable without an IDE, and git-diffable. |
| **Sprout** | **OCI / Docker** | The isolation mechanism. The Sprout itself is language-agnostic; the image *inside* the Sprout changes per task. |
| **Default Executor** | **TypeScript / Node** | Used when the Substrate language is unknown or mixed. MCP SDK is TypeScript-first. Excellent async I/O. Tiny Docker image (`node:alpine`). Type-safe JSON boundaries. |
| **Node Executor** | **Node (Debian + NVM)** | Dynamically instantiated when the Substrate is a Node project (detected via `package.json`). Decouples the Tendril's internal runner requirements from the Substrate's Node version (via `.nvmrc`) so it can natively execute ecosystem tools like `npm test` without version conflicts. |
| **Python Executor** | **Python** | Only instantiated when the Substrate *is* a Python project. Essential for `pytest`, `pip`, Python AST manipulation, and tree-sitter bindings. |
| **Go Executor** | **Go** | Only instantiated when the Substrate *is* a Go project. Used for `go test`, `go build`, and understanding Go module structure. |
| **AST / Repo Map** | **Go + Tree-sitter** | Pre-flight analysis that runs inside the Go Stem binary *before* sprouting a Tendril. |
| **Memory / RAG** | **MCP Sidecar** | Persistent memory is handled by external MCP servers (e.g., `mcp-memory-service` or `pgvector`), not bespoke internal code. |

---

## The Tendril JSON API Contract

Every Tendril executor—regardless of language—must implement a strict universal protocol. The Tendril does **not** contain LLM reasoning loops. It is a dumb worker that receives a fully resolved instruction and executes it.

**The Protocol:**
1. Read exactly one line of JSON from `stdin`.
2. Parse the instruction and execute the appropriate local tools.
3. Write exactly one line of JSON to `stdout`.
4. Exit.

### Input Schema (`stdin`)
```json
{
  "transcript": "Read the file README.md and return its contents",
  "genotype": "python-dev",
  "workspace": "/app"
}
```

### Output Schema (`stdout`)
```json
{
  "status": "success",
  "output": "# OpenTendril\n..."
}
```

### Error Schema (`stdout`)
```json
{
  "status": "error",
  "output": "File not found: README.md"
}
```

---

## How to Build a New Tendril Executor

When introducing support for a new Substrate ecosystem (e.g., Rust, Java, Ruby), you must construct a new Tendril executor. Follow these steps:

### 1. Language Decision Checklist
*   **Is this a general-purpose task?** (e.g., "Review this PR", "Edit this Markdown file"). Use the **Default Executor (TypeScript)**. Do not build a new executor.
*   **Does the task require native ecosystem tools?** (e.g., `cargo test`, `maven build`). You must build a language-specific executor (e.g., a Rust executor, a Java executor).

### 2. Define the Genotype
Create the identity for this Tendril in `.tendril/genotypes/<name>.json`. 
A Genotype is defined as a JSON file containing the Tendril's name and its core system instructions (the persona, constraints, and default behaviors). 
For example:
```json
{
  "name": "thinker",
  "instructions": "You are the OpenTendril Thinker. Analyze the user's task request..."
}
```

### 3. Create the Executor Scaffold
In the `tendrils/<language>/` directory, create:
1.  **Dockerfile:** Use the smallest possible base image (e.g., Alpine). Do NOT install LLM dependencies (no LangChain, no SDKs). Install only the language runtime and necessary ecosystem tools (e.g., test runners, linters).
2.  **Protocol Binding:** Implement the JSON `stdin`/`stdout` loop.
3.  **Tool Implementations:** Write the wrappers for file I/O, Git operations, and language-specific commands. Keep them as thin functions.

*Note: Reference `tendrils/typescript/` for the gold standard implementation of an executor.*

### 4. Write the CI Test Stub
Ensure your executor can be verified without an LLM. Write a local unit test in the executor's language that:
1. Pipes a mock JSON transcript to `stdin`.
2. Asserts that the executor performs the action.
3. Asserts that valid JSON is emitted on `stdout`.
