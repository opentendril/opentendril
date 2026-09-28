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

**A Pollinator holding only OpenTendril authority can submit an exact deterministic patch for an authorized Substrate and obtain Git-reviewable Fruit without receiving repository credentials, requiring client-side Git-host authentication, or invoking Mycorrhizal reasoning for the patch handoff.**

## Done when

* **Authority is exact.** An authenticated Pollinator is authorized by the exact Pollen, `git.apply` capability, and named Substrate.
* **Application stays within delegated isolation.** The supplied patch is applied only inside that Pollinator's isolated delegated workspace.
* **Handoff freshness is exact.** The handoff requires the exact expected `HEAD`; a stale handoff fails closed.
* **Unsafe input and state fail closed.** A dirty workspace, invalid patch, unauthorized Substrate, oversized patch, or partial application all fail closed.
* **Patch application is deterministic.** Applying the patch invokes no Mycorrhizal reasoning.
* **Repository credentials stay with the Stem.** Credentials remain Stem-held and are not exposed to the Pollinator.
* **`git.apply` only applies the patch.** It does not itself branch, commit, push, open a PR, merge, or accept Fruit.
* **Separately granted Git capabilities can continue the lifecycle.** Existing separately granted capabilities can take the applied change through branch, commit, publication, and reviewable Fruit.
* **Fruit remains under Botanist review.** The default branch remains unchanged until the Botanist reviews and merges the resulting Fruit.
* **The capability is projected consistently.** Core, REST, MCP, and CLI expose consistent `git.apply` behavior.
* **A real external-client qualification succeeds.** One real qualification demonstrates an external client handing OpenTendril a patch and reaching reviewable Git output using only OpenTendril authority.

This objective does not require:

* generic `git.pull` or fetch redesign;
* direct shared-working-tree mutation;
* arbitrary host-path ingestion;
* additional forge integrations;
* Greenhouse redesign;
* Internet deployment;
* automatic merge;
* marketing or site work; or
* compound cognitive workflows such as automatic code review after publication.

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
