# Control Plane Decision Template v1

Use this template for medium-risk or high-risk work that needs an explicit Control Plane decision before build work starts.

For the governing standard, see `docs/development-process/control-plane-decision-standard-v1.md`.

---

## Lane Abbreviations
* **`L1`:** CLI & Gateway (Go / MCP)
* **`L2`:** Sprout Orchestration & Tools (Python)
* **`L3`:** Terrarium & Execution (Docker / gVisor / Firecracker)
* **`L4`:** System Storage & Config (SQLite / Memory / env)
* **`Cross-lane`:** Spans more than one lane and needs explicit Control Plane coordination.

---

## Decision Issue Template

```md
# <decision title>

## Status
- proposed | approved | blocked | superseded

## Decision owner
- Human Control Plane

## Source of truth
- <doc path or issue URL>

## Current behavior
- <repo-grounded facts only>

## Target behavior
- <the intended model>

## Decision locks
- <non-negotiable rules>

## Non-goals
- <what this decision does not authorize>

## Risks / forbidden outcomes
- <what must fail or remain disallowed>

## Lane impact
- L1 | L2 | L3 | L4 | Cross-lane

## Control Plane requirement
- Yes | No
- Why: <reason>

## Minimum safe implementation order
1. <foundation>
2. <surface adoption>
3. <exposure>

## Approved slice candidates
- <slice 1>
- <slice 2>

## Validation expectation
- <commands or acceptance doc references>

## Assigned Builder
- <only after approval>
```

---

## PR Trace Block

```md
## Decision trace
- Refs: <ISSUE_URL>
- Source of truth: <doc path(s)>
- Drift from approved decision: none | <describe>

## Validation run
- <command actually run>
- <command actually run>

## Scope guard
- Single-purpose slice: yes
- Cross-lane approved: yes | no | n/a
```

---

## Quick Quality Check

Before approving build work, confirm:
* Current behavior is described with repository-grounded facts.
* Target behavior is explicit enough that builders do not have to guess.
* Forbidden outcomes are stated.
* Minimum safe sequencing is stated.
* Slice candidates are small and single-purpose.
* Validation expectations are explicit.
