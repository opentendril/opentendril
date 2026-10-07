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

**A Botanist can let OpenTendril own the complete lifecycle of every Tendril-created workspace, reclaiming terminal workspace state without manual filesystem or Git-worktree cleanup while preserving anything that may still contain reviewable Fruit.**

## Done when

- Tendril-owned delegated workspaces and RunWorkspaces have a complete observable lifecycle.
- Retained workspaces report their ownership, relevant Git state, clean/dirty state, Fruit/review evidence, and deterministic retention reason.
- Terminal workspace state proven safe to discard is reclaimed automatically.
- Dirty, ambiguous, unverified, or potentially reviewable work is never destroyed automatically.
- A Botanist can explicitly abandon retained state through a bounded control-plane action when automatic reclamation is not permitted.
- Workspace removal, Git worktree metadata, and OpenTendril-owned reference state remain coherent.
- Reviewable Fruit survives disposal of execution workspace state.
- Later work does not inherit stale terminal state from unrelated earlier work.
- No Pollinator receives general workspace deletion authority.
- Normal recovery requires no direct deletion below .tendril, manual git-worktree surgery, or direct editing of OpenTendril ownership registries.

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
