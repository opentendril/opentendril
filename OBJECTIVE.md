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

**A Pollinator holding only OpenTendril authority can synchronize the remote branch state of an authorized Substrate through a bounded governed Git fetch, without receiving repository credentials, modifying Botanist-owned local work, or invoking Mycorrhizal reasoning.**

## Done when

* **Authority is exact.** The exact Pollen, `git.fetch` capability, and named Substrate authority are required.
* **Network authority is configured.** The configured Substrate defines network authority, and a local `origin` identity mismatch fails closed before network mutation.
* **Synchronization is branch-only.** Only branch state under `refs/remotes/origin/*` is synchronized, including bounded pruning.
* **Botanist-owned local state is unchanged.** No tags, `FETCH_HEAD`, local branches, owned refs, `HEAD`, index, or working-tree state is modified.
* **An existing checkout is required.** No delegated workspace is created or rotated.
* **Shared repository state is serialized.** Repository-scoped serialization is used where required.
* **Credentials remain contained.** Credentials stay Stem-held and destination-contained.
* **Interfaces remain in parity.** REST, MCP, and CLI project the same governed Core contract.
* **`git.status` remains offline.** It observes refreshed state only when separately called.
* **Real external-Pollinator qualification succeeds.** Fetch works using only OpenTendril authority while preserving the declared state and credential boundaries.

This objective does not require:

* `git.pull`, merge, rebase, reset, checkout, switch, or local branch synchronization;
* arbitrary remotes or refspecs;
* forge API fetch;
* automatic pre-operation fetch or automatic merge; or
* delegated workspace refresh, creation, or rotation.

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
