# Component: Configurator — an internal Tendril that turns a natural-language request into an executable Hormonal Trigger gate script.

## Purpose

`cmd/stem/internal/configurator` is a single-file leaf (`tendril.go`) that implements the **Configurator Tendril**: an in-process route that interprets a plain-language task with the Mycorrhizal Network (an LLM) and writes the result as a **Hormonal Trigger** — an executable bash script placed under the workspace's transduction triggers directory. Where the ordinary task route hands work to the external Docker orchestrator, the Configurator route is the organism configuring itself, translating a request such as "block risky tasks" into a gate script that the security-transduction layer will later run to allow or deny work. It owns only prompt-to-script generation and file placement; the Stem decides when to route to it and the security layer decides how to evaluate what it produces.

## Responsibilities

**Does:**

- Hold the target triggers directory and an OpenAI client constructed from `OPENAI_API_KEY` (`NewConfiguratorTendril`).
- Build a fixed system prompt describing the Hormonal Trigger contract (an executable receiving a JSON payload path as `$1` with `{"persona", "task"}`; nonzero exit blocks, zero allows) and send the caller's `taskPrompt` as the user message (`Execute`).
- Call the `GPT4o` chat-completion model and take the first choice's content as the raw script (`Execute`).
- Sanitise the model output: trim whitespace, strip leading/trailing markdown code fences, and prepend `#!/bin/bash` when no shebang is present (`Execute`).
- Choose a filename — `block-trigger.sh` when the lowercased prompt contains "block", otherwise `trigger-generated.sh` — create the triggers directory (`0755`), and write the script as an executable file (`0755`, truncating any existing file) (`Execute`).
- Return a human-readable success string naming the file and directory it wrote.

**Does not:**

- Route requests to itself — `cmd/stem` (`cmdserve.go`) selects this path when the request model is `configurator`.
- Evaluate, run, or validate the generated trigger — that is `internal/security`'s `EvaluateTriggers`, invoked separately by the Stem.
- Review, lint, or sandbox the LLM output before making it executable — the returned script is written directly with the executable bit set.
- Expose CLI, REST, or MCP surface, or define governed capabilities.
- Configure the model, provider, or API key at call time — the model is hard-coded and the key is read once from the environment at construction.

## Public interface

| Symbol | Role |
| --- | --- |
| `ConfiguratorTendril` | Struct holding `TriggersDir` (exported) and an unexported `*openai.Client`. |
| `NewConfiguratorTendril(triggersDir string) *ConfiguratorTendril` | Construct the Tendril; reads `OPENAI_API_KEY` from the environment and builds the OpenAI client. |
| `(*ConfiguratorTendril).Execute(ctx, taskPrompt string) (string, error)` | Generate a Hormonal Trigger script from the prompt, write it under `TriggersDir`, and return a success message or a wrapped error. |
| Sentinel errors | none — failures are returned as inline `fmt.Errorf(...%w...)` wraps (generation failure, file-open failure, write failure). |

## Dependencies

**Fan-out:** none (leaf). Standard library plus `github.com/sashabaranov/go-openai`; no other OpenTendril internal packages.

**Fan-in:**

- **`cmd/stem`** — `cmdserve.go` imports the package and, inside the chat/run handler, routes to it only when the requested model equals `configurator`: it calls `NewConfiguratorTendril(triggersDir)` with the workspace triggers path (`./.tendril/transduction/hormonal-triggers`) and awaits `Execute`, using the returned string as the run output. Any other model falls through to the external Docker orchestrator.

## Limitations

- **No tests** — the package ships without a `_test.go`; behaviour is unverified by CI.
- **Key is not validated at construction** — `NewConfiguratorTendril` builds a client even when `OPENAI_API_KEY` is empty; the failure only surfaces on the network call inside `Execute`.
- **Model and provider are hard-coded** — `GPT4o` via the OpenAI client; there is no way to select model, provider, or temperature from the caller.
- **First-choice access is unguarded** — `Execute` reads `resp.Choices[0]` without checking that the response contains any choices, so an empty completion would panic rather than error.
- **`os.MkdirAll` error is ignored** — the directory-create return value is discarded; a create failure is only caught later when the file open fails.
- **Filenames collide by design** — only two names are ever produced, keyed on the substring "block", and writes use `O_TRUNC`; a second run with a matching prompt silently overwrites the previous trigger, and there is no uniqueness or slug per request.
- **Generated output is trusted** — the model's text is written straight to disk with the executable bit (`0755`) and no review, signing, or containment before the security layer may run it.
- **Fence-stripping is shallow** — it trims one leading and one trailing fence token; unusual model formatting (interleaved fences, language tags mid-body) can leave stray markers in the script.
- **Synchronous, non-streaming** — one blocking chat-completion call with no retry, timeout beyond the passed context, or token accounting.

## Design & rationale

The Configurator is the "internal Tendril" half of a two-way route in `cmdserve.go`: a run whose model is `configurator` is handled in-process here, while every other model is delegated to the external Docker orchestrator. The split exists because self-configuration is a different kind of work from running a Pollinator's task — it produces a piece of the organism's own governance rather than a build result, so it stays inside the Stem process and never leaves for an external body.

What it produces is a **Hormonal Trigger**: the system prompt pins the contract exactly — an executable under `.tendril/transduction/hormonal-triggers/` that is handed a JSON payload path as `$1` containing `{"persona", "task"}`, blocks the task on a nonzero exit, and allows it on zero. That contract is the same one `internal/security`'s `EvaluateTriggers` enforces when the Stem screens a task before running it, so the Configurator and the evaluator are two ends of one loop: intent in, gate script out, gate script consulted on the next task. Writing the file executable and truncating in place keeps that loop simple — the newest generation for a given intent is the active gate.

The rough edges (two fixed filenames chosen by a substring test, a hard-coded model, an unchecked first choice, output written executable without review) mark this as an early, deliberately thin slice: it demonstrates natural-language-to-gate generation end to end rather than a hardened self-configuration surface. A durable version would validate and namespace the generated triggers, make the model and provider configurable, and contain the generated script before it becomes an executable part of the transduction path. The code as built is authoritative here; this document describes what it does today, not that intended target.
