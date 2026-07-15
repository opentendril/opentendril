---
name: Design RFC (Gate A)
about: Utopian target model for medium/high-risk work. Defines what, not how. Output from Architect Agent. Does NOT contain implementation steps.
title: '[DESIGN RFC] '
labels: control-plane, needs-human-review
assignees: ''
---

<!--
  Gate A artifact. Governing standard: docs/development-process/control-plane-decision-standard-v1.md
  ARCHITECT AGENT: Fill all sections. Do NOT include implementation steps or slice breakdown.
  HUMAN: Review design intent. If approved, comment exactly: approved
  That comment triggers Gate B — Architect will generate the Implementation Plan.
  Lanes: L1=CLI & Gateway  L2=Orchestration & Tools  L3=Terrarium  L4=Storage & Config
-->

## Status
- [ ] proposed
- [ ] approved
- [ ] blocked
- [ ] superseded

## Decision owner
Human Control Plane

## Problem statement
<!-- What is broken, missing, or needs to change? Why now? -->

## Taxonomy placement
<!-- REQUIRED before proposing a target model. Which organ does this capability belong to, and WHY?
     Classify against SYNTHETIC-TAXONOMY.md §5 "How to classify a new capability". Heuristic:
       • local computation on the plant's own code → a core organ
         (e.g. the Rhizome parses the Substrate; go/ast & tree-sitter live here — a parser is never a Nodule)
       • an external network *service* the plant partners with → a Symbiotic Nodule (GitHub API, GCP)
       • a repo the plant works on → a Substrate;  our own outward connectivity code → a Root
     State the classification and the reasoning. If this changes where an existing capability lives, say so. -->

## Target model
<!-- The utopian design. What should be true after this change lands? -->
<!-- Be specific enough that two builders would build compatible things, but do not prescribe HOW. -->

## Invariants and non-negotiables
<!-- What must always remain true? What constraints cannot be violated? -->
-

## What must FAIL after this change (negative requirements)
<!-- What security behaviors, error paths, or invalid inputs must be explicitly blocked? -->
-

## Out of scope
<!-- What this RFC explicitly does NOT authorize or address. -->
-

## Lane impact
- [ ] L1 — CLI & Gateway (Go / MCP)
- [ ] L2 — Agent Orchestration & Tools (Python)
- [ ] L3 — Terrarium & Execution (Docker / gVisor / Firecracker)
- [ ] L4 — System Storage & Config (SQLite / Memory / env)
- [ ] Cross-lane (requires Control Plane approval before build starts)

## Source of truth references
<!-- Cite exact doc paths. Do not write "see docs". -->
-

## Gate A approval
<!-- HUMAN: If this design is approved, comment on this issue: approved -->
