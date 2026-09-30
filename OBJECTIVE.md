# Objective

> One thing the organism must be able to do next. Every brief cites it. Work that
> does not serve it is filed, not started. It changes when the condition below is
> met deliberately, not by drift.

**The purpose it must serve** is the one stated in the first line of `README.md`:
to let a Botanist stop approving every step an LLM takes, because it works freely
inside a boundary and everything it did arrives in Git for review. That sentence
does not change when the objective does. An objective that does not advance it is
the wrong objective, however worthwhile the work, which is the check this file
exists to make possible.

## Current

**A Pollinator holding only OpenTendril authority can carry one coherent governed Git change from synchronized remote state through an isolated feature branch, deterministic patch, commit, push, and Git-reviewable pull request, regardless of configured commit posture, without receiving repository credentials or modifying the default branch.**

## Done when

* The same governed workflow works for local and GitHub App/API commit modes:
  `git.fetch -> git.status -> git.branch -> git.apply -> git.status -> git.commit -> git.status -> git.push -> git.pr`.
* A successful `git.commit` leaves the delegated workspace on the same feature branch, clean, with `HEAD` equal to the returned commit OID.
* API-mode commit can safely establish only its exact feature branch at the exact expected pre-commit OID.
* Existing remote state at another OID fails closed and is never overwritten.
* Ambiguous remote mutations are reconciled from bounded read-only evidence before any retry.
* One commit intent cannot create duplicate remote commits.
* `git.push` remains independently authorized and is idempotent after an API-mode commit already published the same OID.
* `git.fetch` retains its branch-tracking-only contract.
* Exact Pollen, operation-class, and named Substrate authority remains required.
* Credentials remain Stem-held.
* REST, MCP, and CLI remain projections of the same governed Core semantics.
* The repository default branch remains unchanged.
* Real external Pollinator qualification reaches a draft Git-reviewable pull request using only OpenTendril authority.

This objective does not require general pull, reset, checkout, switch, merge,
rebase, arbitrary remote-ref mutation, or automatic merge.

---

## How to use it

State the objective in the present tense, as a capability rather than a task:
*"a Botanist can X"*, not *"build X"*. A capability can be demonstrated; a task
can only be declared finished.

Give it a condition that someone other than the author can check. "It is safe" is
judged; "nothing outside the declared boundary was touched, across runs that each
changed something" is observed.

**The test for whether work belongs** is not whether it is worth doing (most
filed work is) but whether the objective is unreachable without it. Everything
else goes to Issues. That includes defects found while working: a real defect
discovered on the way is evidence for a *future* objective, and starting it now
is how a month disappears.

**Report, never decide.** Tendril's job is to hold the boundary and to say what
happened. Accepting the result is always the Botanist's, or a gate the Botanist
chose. A feature that requires this project to hold an opinion about the *work*
rather than about the *containment* belongs somewhere else.

**When the condition is met**, write the next objective before starting anything.
An empty objective is what a roadmap grows back into.
