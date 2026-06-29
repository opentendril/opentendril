# Design Document: Dynamic Orchestration & Evolved Role Delegation

This document outlines the target design for introducing dynamic sequence planning, coordinator-level routing, role delegation, and self-debugging capabilities in the OpenTendril framework.

---

## 1. Architectural Overview

Currently, OpenTendril sequences are executed using a static, pre-defined Directed Acyclic Graph (DAG) compiled from YAML files (see [DESIGN-SEQUENCE-RUNNER.md](file:///home/dr3w/GitHub/opentendril/core/docs/DESIGN-SEQUENCE-RUNNER.md)). While highly deterministic, static sequences are unable to adapt to complex programming goals that require context-dependent branching or recursive debugging.

To solve this, we introduce **Dynamic Orchestration** using a three-tiered model:
1.  **Dual LLM Client Setup:** Routing lightweight planning and inspection tasks to a fast, cost-efficient Coordinator model, while leaving heavy code-writing tasks to the expert Worker model.
2.  **Dynamic Sequence Generation (Conductor planning):** Sprouting a coordinator-level planner that evaluates the target workspace and dynamically writes the sequence DAG at execution time.
3.  **Role Delegation & Recursive Sprouting:** Isolating planning (Thinker), editing (Worker), and verification (Verifier) tasks into specialized sprouts, and dynamically appending correction steps (Debugger sprouts) when verification fails.

---

## 2. Dual LLM Client Configuration

To reduce execution latency and costs, we split the LLM client into two providers:
*   **Worker Client (Expert Model):** The primary coding LLM (e.g. Claude 3.5 or a large local coder model). Resolves via `ResolveProviderSpec()`.
*   **Coordinator Client (Lightweight Model):** A compact model (e.g. a 1.5B–3B parameter local model) optimized for structural planning and validation. Resolves via a new `ResolveCoordinatorProviderSpec()` helper.

### Environment Variable Bindings
*   `COORDINATOR_LLM_PROVIDER`: The provider specification for the coordinator (falls back to `DEFAULT_LLM_PROVIDER` if empty).
*   `COORDINATOR_MODEL_NAME`: The coordinator model name (e.g. `qwen2.5:1.5b-instruct`). Falls back to the default provider model name.
*   `COORDINATOR_LOCAL_INFERENCE_URL`: Local endpoint for the coordinator model (falls back to `LOCAL_INFERENCE_URL`).

### Code Changes
In [client.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/internal/llm/client.go), we expose:
```go
func NewCoordinatorClientFromEnv() *Client {
	return NewClient(ResolveCoordinatorProviderSpec())
}
```

In [docker.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/internal/orchestrator/docker.go), the `DockerOrchestrator` struct is updated to track whether the active run is a coordinator-level process:
```go
type DockerOrchestrator struct {
	...
	IsCoordinator   bool
	Genotype        string
}
```

When sprouting a container, the orchestrator passes the genotype name dynamically to the environment using `TENDRIL_GENOTYPE`.

---

## 3. Dynamic Sequence Generation

Dynamic sequences replace static YAML files with planning steps designed at execution time.

### The Execution Loop
1.  The Go Stem orchestrator reads the user Transcript.
2.  It detects a "Conductor" step or sequence request and sprouts a planning container running the coordinator client.
3.  The planning sprout inspects the workspace, designs a list of execution steps, and returns them as a JSON array.
4.  The sequence runner parses this array on the host side and appends the steps directly to the active sequence.

### Code Changes
In [sequence.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/internal/orchestrator/sequence.go), when a step completes successfully, we check if it generated dynamic steps:
```go
if isConductorStep(result.stepID) {
	newSteps, err := parseDynamicSteps(result.output)
	if err == nil && len(newSteps) > 0 {
		r.appendDynamicSteps(newSteps)
	}
}
```

The `appendDynamicSteps` function dynamically appends the new steps to the queue, re-indexing dependencies (`stepByID`, `stepIndex`, `dependents`, and `remainingDeps`) to update the runnable step queue.

---

## 4. Evolved Role Delegation & Self-Correction

Instead of running file writing and test compilation in a single ReAct loop, we delegate tasks to specialized sprout roles and enable recursive debugging.

```
       ┌────────────────────────┐
       │   Thinker Sprout       │  <-- Genotype: thinker
       │   (Writes plan)        │
       └───────────┬────────────┘
                   │
                   ▼
       ┌────────────────────────┐
       │   Worker Sprout        │  <-- Genotype: typescript-dev / go-dev
       │   (Applies changes)    │
       └───────────┬────────────┘
                   │
                   ▼
       ┌────────────────────────┐
       │   Verifier Sprout      │  <-- Genotype: verifier
       │   (Runs tests)         │
       └───────────┬────────────┘
                   │
        ┌──────────┴──────────┐
        │  Did tests pass?    │
        └────┬────────────┬───┘
             │ Yes        │ No
             ▼            ▼
         (Success)    ┌────────────────────────┐
                      │   Debugger Sprout      │  <-- Genotype: debugger
                      │   (Applies patches)    │
                      └───────────┬────────────┘
                                  │
                                  ▼
                             (Re-verify)
```

### Specialized Genotypes
We define three baseline genotypes under `.tendril/genotypes/system/`:
*   `thinker.md`: Prompts the model to act as a system architect, outlining technical plans and developer instructions.
*   `verifier.md`: Prompts the model to parse test outputs, identify failures, and detail linter/compiler bugs.
*   `debugger.md`: Prompts the model to ingest error logs and apply targeted patches to code.

### Recursive Sprouting (Self-Debugging)
If a verifier step returns an error, the orchestrator intercepts the failure and dynamically sprouts a Debugger sprout.
1.  The orchestrator appends a `debugger-<step-id>` step to the sequence.
2.  The debugger sprout is initialized with the failed step's code changes and compiler/test error logs.
3.  It applies correction patches to the workspace, commits them, and resets the verifier step to `pending`.
4.  This self-correction loop scales execution compute at test-time until compilation/testing succeeds or the retry limit is hit.
