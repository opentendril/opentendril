# CODEX.md — Codex CLI Integration Spec

> This file defines how **Codex CLI** integrates as an external tool within OpenTendril's orchestration pipeline. Codex is one of many external tools that OpenTendril can invoke via its `execCommand` tool surface. The long-term goal is for OpenTendril itself to orchestrate tools autonomously. This document exists to formalize that integration.

---

## 1. What Codex Is (In OpenTendril Terms)

Codex CLI is an **external LLM-backed coding tool** — in botanical terms, another branch of the Mycorrhizal Network. OpenTendril treats it as an `execCommand` target: a subprocess the Tendril can invoke with a task prompt and receive a code diff or file output in return.

```
OpenTendril Stem
  └─ Sprout (Docker Terrarium)
       └─ Tendril (main.py)
            └─ execCommand: "codex ..."   ← Codex is invoked here
```

The goal is **pass-through first, then progressive ownership**: start by routing tasks to Codex via `execCommand`, observe the output, and over time replace that call with OpenTendril's own Sprout doing the same work.

---

## 2. How OpenTendril Invokes Codex

Any Sprout Tendril can invoke Codex via the `execCommand` tool. Example call shape:

```json
{
  "tool": "execCommand",
  "arguments": {
    "command": "codex --approval-mode full-auto -q \"<task prompt here>\"",
    "cwd": ".",
    "timeoutSeconds": 300
  }
}
```

The `--approval-mode full-auto` flag tells Codex to execute without interactive prompts, making it compatible with non-interactive Sprout execution. `-q` suppresses verbose output.

### Environment Requirements

Codex CLI must be installed on the host and available in PATH inside the Sprout or on the host where the Tendril runs. The following env var must be set:

```
OPENAI_API_KEY=<key>   # already in docker-compose.yml environment passthrough
```

---

## 3. Codex Operating Rules (When Invoked via OpenTendril)

When OpenTendril calls Codex as a tool, Codex operates under these constraints — these are the rules the Tendril's task prompt should instruct Codex to follow:

### Git Preflight (Codex must verify before editing)
```sh
git status -sb                                  # must be clean
git fetch origin --prune
git rev-list --left-right --count origin/main...main  # must be "0	0"
```

### Branch Naming
```
feature/<short-description>
```
Never commit to `main`. Never force-push.

### Commit Identity & Signing
```sh
git config user.name "OpenTendril"
git config user.email "273992813+opentendril@users.noreply.github.com"
git config commit.gpgsign true
git config user.signingkey C0AA41FA9B3B4DBD
```

### Scope Discipline
- Only modify files explicitly listed in the task prompt.
- No drive-by refactors or speculative changes.
- If a problem is found outside scope, note it in the PR description — do not fix it.

### Naming & Casing (Non-Negotiable)

| Context | Convention |
|---|---|
| Go/JS/TS source filenames | `mergedlowercase.go` |
| Go test files | `merged_test.go` (only underscore exception) |
| Directories & config | `kebab-case/` |
| Go exported symbols | `PascalCase` |
| Go unexported symbols | `camelCase` |
| Python functions/vars | `snake_case` |
| JSON keys | `camelCase` |
| HTTP endpoints | `kebab-case` |
| Env vars / DB keys | `SCREAMING_SNAKE_CASE` |

### Build & Test Gate
Codex must run before opening any PR:
```sh
cd cmd/stem && go build ./...
cd cmd/stem && go test ./...
```

### PR Rules
- Open as **Draft PR** only.
- Title: `<type>(<scope>): <description>`
- Body must include: originating issue link, change summary, terminal output of build/test.
- No merge. No auto-merge. No branch deletion.

---

## 4. Genotype: codex-delegator

A Tendril Genotype that instructs a Sprout to delegate a task to Codex and return its output. Stored at `.tendril/genotypes/codex-delegator.json`.

This allows `tendril sequence` or a chat prompt to trigger Codex via OpenTendril without any manual shell invocation.

---

## 5. Forbidden Actions (Codex must never do these)

| Action | Status |
|---|---|
| Push to `main` | ❌ Never |
| Merge a PR | ❌ Never |
| Enable auto-merge | ❌ Never |
| Delete a remote branch | ❌ Never |
| Touch files not in the task prompt | ❌ Never |
| Use `snake_case` in Go/TS code | ❌ Never |
| Commit without GPG signature | ❌ Never |

---

## 6. Roadmap: From Pass-Through to Native

```
Phase 1 (Now):      OpenTendril calls Codex via execCommand — testing the pipeline
Phase 2:            OpenTendril generates the task prompt autonomously from an Issue
Phase 3:            OpenTendril's own Sprout replaces Codex for most tasks
Phase 4 (Target):   Codex becomes optional — invoked only for tasks requiring its
                    specific model capabilities (e.g. very large context windows)
```

---

## 7. Botanical Glossary (Quick Reference)

| Term | Maps To |
|---|---|
| Stem | Go orchestrator (`cmd/stem`) |
| Sprout | Ephemeral Docker container |
| Tendril | Python/Go worker runtime inside the Sprout |
| Terrarium | Isolated execution boundary |
| Rhizome | Background AST indexer (`internal/rhizome`) |
| Genotype | System prompt / persona (`.tendril/genotypes/`) |
| Plasmid | Modular context block injected into a Genotype |
| Transcript | One-off task prompt for a single execution |
| Sequence | Chained multi-step workflow |
| Mycorrhizal Network | External LLMs — Codex, Antigravity, Ollama, etc. |
| Substrate | Host repository being worked on |
| Epigenetics | Accumulated learnings in `.tendril/genome/` |
