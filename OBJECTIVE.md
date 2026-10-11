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

**A Botanist can see retained workspaces and Fruit through Greenhouse, understand why each remains, and safely manage their lifecycle without manual filesystem or Git administration.**

## Done when

- Greenhouse shows RunWorkspaces, delegated workspaces, and Fruit as distinct entries.
- Each entry reports its identity, ownership or evidence-source availability, Git or review evidence, and a deterministic retention reason.
- A missing or unavailable evidence source stays visible as unavailable. It never appears as an empty inventory or as safe to discard.
- Workspace inspection, reconciliation, and abandonment use the existing Stem Core lifecycle policy. Greenhouse shows the exact target and its consequences before confirmation, and does not classify the evidence itself.
- Automatic reclamation removes only workspace state Core has proven disposable. Dirty, ambiguous, unverified, or potentially reviewable work stays retained.
- A Botanist can abandon one exact retained workspace through Greenhouse. The action is Botanist-only and requires confirmation.
- Removing an execution workspace leaves its reviewable Fruit available.
- Workspace removal, Git worktree metadata, and OpenTendril-owned reference state remain coherent.
- Fruit Git review state, Botanist disposition, and inventory visibility stay independent records.
- Archiving a Fruit claim changes inventory visibility only. The original claim, its evidence, and its audit history remain. No Git ref, commit, workspace, or provenance record is deleted.
- Unknown or unavailable Fruit leaves the active inventory only after the Botanist acknowledges that its Git disposition is unverified. The unknown review state and its reason remain recorded.
- Archived Fruit stays discoverable in an archived view.
- No Pollinator receives workspace or Fruit deletion authority, and neither public REST nor MCP gains any.
- Workspace reclamation, Fruit inventory archival, and deletion of a Git Fruit ref stay separate operations. This objective does not delete Git Fruit refs.
- A Botanist can inspect retained state, reclaim proven-disposable workspace state, abandon one exact retained workspace, and archive one exact Fruit claim through Greenhouse, without deleting anything below .tendril, repairing a Git worktree by hand, or editing an ownership registry.

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
