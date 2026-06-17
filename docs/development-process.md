# Development Process

## Status
Summary index for the project-agnostic AI-SDLC lifecycle. The detailed Control Plane decision standard and template live under `docs/development-process/`.

## Purpose
Reusable AI-SDLC starter for the lifecycle from goal to spec to build to review to merge to closeout-reconcile.

## Authority and Scope
This document is project-agnostic. It summarizes the operating model and points to the repo-specific rules in `AGENTS.md`. Control Plane decision mechanics are defined in:

* [control-plane-decision-standard-v1.md](development-process/control-plane-decision-standard-v1.md)
* [control-plane-decision-template-v1.md](development-process/control-plane-decision-template-v1.md)

This page does not restate them. Repo engineering conventions live in `GUARDRAILS.md`. If this document ever conflicts with either source, the higher-authority document wins.

## Roles
* **Human Control Plane:** Approves the target model, slices, and merge decision.
* **Architect:** Drafts the decision artifact and keeps the slice boundaries coherent.
* **Builder:** Implements the approved slice and records validation evidence.
* **Reviewer:** Checks drift from the approved decision and the repo policy.

## Lifecycle
1. **Goal:** Name the change and the outcome.
2. **Spec / Issue:** Record the approved target model, invariants, and negative expectations.
3. **Build:** Implement one approved slice with minimal scope.
4. **Review:** Compare the PR to the approved decision and note drift.
5. **Merge:** Human approves the merge when checks and review are clean.
6. **Closeout-Reconcile:** Update the affected core persistent doc(s) with the shipped outcome, or record `no core-doc change`.

## Control Plane
The approval model, required artifact shape, and template live in the relocated Control Plane docs linked above.

## Closeout-Reconcile
After merge, reconcile the living core docs with reality. Update the affected core persistent doc(s) so they match the shipped outcome, or record `no core-doc change` when the slice does not change the core contract.

## Validation and Evidence
Keep validation close to the PR and the issue. Use the repo's guardrails for exact commands, and keep evidence in the review trail unless a higher-level policy requires durable records.

## Stop Conditions
Stop and ask for alignment if the target model is unclear, if documents conflict, or if a slice would blur policy, contract, or lifecycle boundaries.
