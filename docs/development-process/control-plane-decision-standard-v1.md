# Control Plane Decision Standard v1

## Status
This document is the normative reference for Control Plane decisions in OpenTendril. It defines the decision quality bar for work that later becomes slices, GitHub Issues, and PRs.

If this document conflicts with `AGENTS.md`, `AGENTS.md` wins for repository workflow and guardrail mechanics.

---

## Purpose
Define the operating model for how OpenTendril should make, record, approve, and trace Control Plane decisions before builders start implementation work.

The goal is to keep product, security, architecture, and sequencing decisions explicit and reviewable so builders do not have to guess policies from current runtime behavior, stale issues, or PR discussion.

---

## Modeling Stance
This document is intentionally aspirational. It does not ratify current repository behavior. Current code, historical issues, and open PRs are inputs for analysis only. They are not the source of truth for target behavior unless an approved decision explicitly says so.

---

## Authoritative Inputs and Precedence
Primary inputs:
* `AGENTS.md`
* `TESTING.md`
* The current source-of-truth policy/spec/validation documents for the affected area.

Precedence:
1. `AGENTS.md` wins for repository guardrails, workflows, stop conditions, and execution rules.
2. Domain policy/spec documents win for the behavior of the area they govern.
3. This document defines how Control Plane decisions should be formed, approved, and traced.
4. Issues, comments, and PR descriptions may apply these rules, but they do not override them.

If no current source-of-truth document exists for a risky area, the Control Plane decision must either create one first or explicitly designate the decision artifact as the temporary source of truth.

---

## Lane Definitions
These lane definitions follow `AGENTS.md` and are repeated here so Control Plane decisions can be read without guessing the stream labels:
* **`L1`:** CLI & Gateway (Go / MCP)
* **`L2`:** Agent Orchestration & Tools (Python)
* **`L3`:** Terrarium & Execution (Docker / gVisor / Firecracker)
* **`L4`:** System Storage & Config (SQLite / Memory / env)
* **`Cross-lane`:** Work that spans more than one stream lane and therefore requires explicit Control Plane coordination before build work starts.

---

## Decision Locks
1. Builders implement approved decisions. They do not invent product policy, security policy, or cross-lane scope.
2. A PR is execution evidence, not the place where unresolved policy is decided.
3. One meaningful decision should resolve one coherent question. If scope expands into a second question, open a second decision artifact.
4. Cross-lane changes require explicit Control Plane approval before build work starts.
5. Foundation work should be approved before exposure work. Contracts and authorization foundations should land before UI, public endpoints, or rollout expansion.
6. Current runtime behavior is never sufficient justification for a target model by itself.
7. Every decision must define both allowed outcomes and forbidden outcomes.
8. Every decision must be traceable from source-of-truth to issue to slice to PR.
9. Decisions must have explicit supersession. Newer intent does not silently invalidate prior guidance.
10. If repository docs, runtime, and issue instructions disagree materially, implementation stops until one source of truth is re-established.

---

## When a Control Plane Decision is Required
A Control Plane decision is mandatory when any of the following apply:
* The work crosses `L1`/`L2`/`L3`/`L4` lane boundaries.
* The work changes authorization, security boundaries, secrets, public endpoints, terrarium environments, or database systems.
* The work introduces or changes external contracts such as API behavior, JSON shapes, enum values, or long-lived schema semantics.
* The work changes deploy policy, runtime config, CLI options, or CI/workflows.
* The work introduces a naming exception or other boundary exception that will affect future slices.
* The work touches a single-thread area (e.g. `AGENTS.md`, workflows) and needs explicit coordination.
* The work depends on choosing between conflicting docs, conflicting runtime behavior, or unresolved architectural alternatives.
* The work is hard to unwind (such as terrarium isolation levels, database schemas, or public API structures).

---

## Required Decision Artifact
Every Control Plane decision must exist as a durable artifact, normally a GitHub Issue linked to any governing docs.

For medium and high-risk work, two artifacts are required:
* **Design RFC** (`.github/ISSUE_TEMPLATE/design-rfc.md`) — defines the target model, invariants, and what must fail (Gate A).
* **Implementation Plan** (`.github/ISSUE_TEMPLATE/implementation-plan.md`) — code-grounded current state, delta, risks, and slice breakdown (Gate B).

For simple or low-risk work (docs-only changes, isolated bug fixes, test additions, mechanical renames), a single **Architect Spec** issue (`.github/ISSUE_TEMPLATE/architect-spec.md`) is sufficient.

---

## Decision Quality Bar
A decision is build-ready only when it is specific enough that two different approved builders would produce materially compatible slices.

That means the artifact must:
* Describe the current state using repository-grounded facts rather than assumptions.
* Define the target state clearly enough to reject incorrect implementations.
* Define what remains out of scope.
* Identify the dangerous shortcuts that are not allowed.
* Describe the sequencing constraints between foundation work and exposure work.
* Point to the validation standard or acceptance criteria for the slice.

---

## Preferred Sequencing Model
For risky or ambiguous work, OpenTendril prefers this order:
1. **Policy / Utopia:** Define the target model and invariants (*Design RFC issue — Gate A*).
2. **Gap Audit:** Compare current `main` against the target model (*Implementation Plan issue — Gate B, first section*).
3. **Foundation Slice:** Contracts, data model, authorizer, terrarium configuration, or other non-user-facing primitives.
4. **Surface Slice:** One API surface or module adopts the new foundation.
5. **Exposure Slice:** Public endpoints, CLI flags, or wider product exposure.
6. **Cleanup / Hardening:** Drift removal, migration cleanup, validation expansion.

This sequence is preferred because it minimizes the chance that an interface starts implying semantics the backend has not yet earned.

---

## Recommended Decision Issue Template
Use the structure defined in `.github/ISSUE_TEMPLATE/implementation-plan.md` for Gate B planning.

## Recommended PR Trace Block
Use this shape in the PR body to keep execution traceable:

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
