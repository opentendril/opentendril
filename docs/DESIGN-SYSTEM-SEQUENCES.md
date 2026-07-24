# System Sequences: Pre-built Git & SDLC Workflows

> **Status:** Design RFC
> **See also:** [DESIGN-CONDUCTOR.md](./DESIGN-CONDUCTOR.md)

---

## The Two Layers of a Workflow

When you want OpenTendril to perform a repeated, structured task, there are two distinct concerns that must not be conflated:

| Layer | Term | "What it is" | "Where it lives" |
|---|---|---|---|
| **1. The instructions** | **Genotype** | *Who* the Tendril is and *how it should think* | the control plane (trusted) or the workspace (never trusted) |
| **2. The workflow** | **Sequence** | *What steps* to run, in what order, with what Genotype | the control plane (trusted) or the workspace (never trusted) |

A Sequence **orchestrates** Genotypes. A Genotype **cannot** run a Sequence. This separation is fundamental to security: a Genotype operating inside a Terrarium cannot trigger new Sequences or elevate its own scope — only the Stem can initiate Sequences, and only a human (or a trusted host-level trigger) can call `stem sequence grow`.

---

## System Sequences

Just as System Genotypes ship with OpenTendril as immutable, pre-built AI personas, **System Sequences** are pre-built workflow definitions that ship in the Stem's control plane and cover the standard SDLC lifecycle. They are:

- **Immutable from within Terrariums** — Sprouts cannot modify them
- **Parameterised** — accept user-supplied inputs (branch name, PR number, etc.)
- **Composable** — each step delegates to a specific System Genotype with a constrained Plasmid set

---

## Candidate System Sequences (Phase 1)

### `pr-close-cycle`
Automates the documentation and housekeeping tasks required to properly close a feature PR.

```yaml
name: pr-close-cycle
system: true
description: >
  Analyses all commits on a PR branch, posts a structured changelog comment,
  updates relevant architecture docs, and reports on dirty workspace files.
parameters:
  - name: branch
    description: The PR branch name to analyse
    required: true
  - name: pr_number
    description: GitHub PR number
    required: true
steps:
  - id: analyse-commits
    genotype: github-ops
    description: "Read all commits on the branch and classify by type (feat/fix/docs/chore)"
    transcript: |
      List all commits on branch {{ branch }} since it diverged from main.
      For each commit, extract: SHA (7 chars), conventional commit type, and a one-sentence summary.
      Output as structured JSON.

  - id: write-changelog
    genotype: github-ops
    description: "Post a structured commit-by-commit changelog comment to the PR"
    dependsOn: [analyse-commits]
    transcript: |
      Using the commit analysis from the previous step, post a Markdown comment
      to PR #{{ pr_number }} with a table titled "Commit-by-Commit Changelog".
      Group commits by type. Include a Security Notes section for any commits
      touching security-sensitive paths (orchestrator/, security/, auth/).

  - id: update-affected-docs
    genotype: code-writer
    description: "Update .github/SECURITY.md, ARCHITECTURE.md, or relevant design docs for any architectural changes"
    dependsOn: [analyse-commits]
    transcript: |
      Review the commit list and identify any changes that should be reflected
      in .github/SECURITY.md, ARCHITECTURE.md, or docs/. Update the relevant documents
      to reflect new behaviour, security posture changes, or new components.

  - id: check-workspace
    genotype: codebase-reader
    description: "Report on uncommitted/untracked files and recommend commit vs discard for each"
    transcript: |
      List all modified and untracked files in the workspace.
      For each file, classify as: COMMIT (valuable, should be versioned),
      DISCARD (runtime artefact, should be gitignored), or REVIEW (unclear, needs human decision).
      Output a table with your recommendation and reasoning.
```

---

### `feature-branch-open`
Scaffolds a new feature branch with the correct naming convention, creates the GitHub Issue, and writes an initial RFC design doc.

```yaml
name: feature-branch-open
system: true
description: >
  Creates a new feature branch, opens a tracking GitHub Issue,
  and scaffolds a design RFC document in docs/.
parameters:
  - name: issue_title
    required: true
  - name: issue_body
    required: true
  - name: branch_name
    required: true
steps:
  - id: create-issue
    genotype: github-ops
    transcript: "Create a GitHub Issue titled '{{ issue_title }}' with body: {{ issue_body }}"

  - id: scaffold-rfc
    genotype: code-writer
    dependsOn: [create-issue]
    transcript: |
      Create a new file at docs/DESIGN-{{ branch_name | upper | replace('-', '-') }}.md.
      Write a standard RFC template including: Goal, Proposed Changes, Implementation Sketch,
      and Verification Plan. Reference the issue number from the previous step.
```

---

### `pr-review-request`
Performs a structured pre-merge review: runs tests, checks for security-sensitive changes, validates commit message conventions, and posts a review summary.

```yaml
name: pr-review-request
system: true
description: >
  Automated pre-merge review: tests, security scan, commit convention check,
  and a structured review summary comment.
parameters:
  - name: pr_number
    required: true
  - name: branch
    required: true
steps:
  - id: run-tests
    genotype: test-runner
    transcript: "Run the full test suite and report pass/fail with any error output."

  - id: security-scan
    genotype: codebase-reader
    transcript: |
      Review all changed files in branch {{ branch }}.
      Flag any changes to: orchestrator/, security/, auth/, substrates.yaml, genotypes/.
      For each flagged file, summarise the security implication.

  - id: convention-check
    genotype: github-ops
    transcript: |
      Check all commits on PR #{{ pr_number }} conform to Conventional Commits format
      (feat/fix/docs/chore/refactor/test/style). List any non-conforming commits.

  - id: post-review
    genotype: github-ops
    dependsOn: [run-tests, security-scan, convention-check]
    transcript: |
      Post a structured PR review comment to #{{ pr_number }} summarising:
      ✅/❌ Test results, ⚠️ Security flags, 📋 Convention violations.
      Recommend APPROVE, REQUEST_CHANGES, or NEEDS_DISCUSSION.
```

---

## Structured Logging & Reply Format

A key requirement for System Sequences is **consistent, parseable output** at each step. Every Sequence step should emit structured events that the Stem can log, surface in the UI, and pipe to external systems (e.g. Slack, GitHub comments, CI).

### Step Event Schema

```json
{
  "sequence": "pr-close-cycle",
  "step_id": "write-changelog",
  "status": "complete",
  "genotype": "github-ops",
  "started_at": "2026-07-04T03:51:00Z",
  "completed_at": "2026-07-04T03:51:42Z",
  "output_summary": "Posted changelog comment with 6 commit entries.",
  "artifact_urls": [],
  "errors": []
}
```

### Reply Text Conventions

Each Genotype's system prompt should include a **structured reply contract** specifying the expected output format. For example, the `github-ops` Genotype's prompt should instruct:

> *"When you complete an action, always end your response with a JSON block tagged `ACTION_RESULT` containing: `actionType`, `target` (URL or file path), `summary` (one sentence), and `success` (bool)."*

This allows the Stem to parse Tendril outputs programmatically without relying on fragile regex over free-text LLM responses.

---

## Implementation Path

1. **Phase 1 (Issue #116):** Define the `system: true` flag in the Sequence YAML schema. Implement control-plane discovery for sequences. Ship `pr-close-cycle.yaml` and `feature-branch-open.yaml`.
2. **Phase 2:** Implement the structured `ACTION_RESULT` reply contract in System Genotype prompts. Wire step event emission to the EventBus.
3. **Phase 3:** Build a Sequence audit log viewer in the Web UI (connects to Issue #14 — Thought-Stream Visualizer).
