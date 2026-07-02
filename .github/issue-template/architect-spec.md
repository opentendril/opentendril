---
name: Architect Spec (Control Plane Decision)
about: Simple or low-risk work (docs-only, isolated bug fixes, test additions, mechanical renames). For medium/high-risk work use Design RFC + Implementation Plan templates instead.
title: "[SPEC] "
labels: control-plane, needs-human-review
assignees: ''
---

<!--
  Governing standard: docs/development-process/control-plane-decision-standard-v1.md
  ARCHITECT AGENT: Fill all sections. Leave "Assigned Builder" blank.
  HUMAN: Review, then fill "Assigned Builder" and change Status to approved.
  Lanes: L1=CLI & Gateway  L2=Orchestration & Tools  L3=Terrarium  L4=Storage & Config
-->

## Status
- [ ] proposed
- [ ] approved
- [ ] blocked
- [ ] superseded

## Decision owner
Human Control Plane

## Source of truth
<!-- Cite exact doc paths. Do not write "see docs". -->
-

## Current behavior
<!-- REQUIRED: Cite file:line for every behavioral claim. No memory-based descriptions. -->
- (source: `path/to/file:line`)

## Target behavior
<!-- Specific enough that two builders produce compatible output. -->

## Decision locks
<!-- Non-negotiable rules this decision enforces. -->
-

## Non-goals
<!-- What this spec does NOT authorize. -->
-

## Risks / forbidden outcomes
<!-- What MUST FAIL or remain disallowed after this change. Required for auth/IAM/lifecycle work. -->
-

## Lane impact
- [ ] L1 — CLI & Gateway (Go / MCP)
- [ ] L2 — Agent Orchestration & Tools (Python)
- [ ] L3 — Terrarium & Execution (Docker / gVisor / Firecracker)
- [ ] L4 — System Storage & Config (SQLite / Memory / env)
- [ ] Cross-lane (requires Control Plane approval before build starts)

## Control Plane decision required?
- [ ] Yes — Why:
- [ ] No

## Minimum safe implementation order
1.
2.
3.

## Approved slice candidates
- Slice 1:
- Slice 2:

## Validation expectation
-

## Assigned Builder
<!-- Leave blank. Human fills after approving the spec. -->
