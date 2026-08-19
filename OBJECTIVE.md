# Objective

> One thing the organism must be able to do next. Every brief cites it. Work that
> does not serve it is filed, not started. It changes when the condition below is
> met — deliberately, not by drift.

**The purpose it must serve** is the one stated in the first line of `README.md`:
to let a Botanist stop approving every step an LLM takes, because it works freely
inside a boundary and everything it did arrives in Git for review. That sentence
does not change when the objective does. An objective that does not advance it is
the wrong objective, however worthwhile the work — which is the check this file
exists to make possible.

## Current

**A Botanist can delegate multiple autonomous tasks against the same managed
Substrate at the same time, without coordinating their timing, and each task
works independently and arrives as its own Git-reviewable Fruit.**


The previous objective established that one unattended bounded run can work
without Botanist intervention, remain observable, stay inside its boundary, and
arrive in Git for review.


That autonomy does not yet compose. Managed runs against the same Substrate
currently share mutable checkout state. If the Botanist must serialize work
manually to prevent one run disturbing another, the Botanist is still part of
the execution scheduler.


The next capability is therefore independent work in flight. Concurrent Sprouts
may begin from the same Substrate revision and may even edit the same source, but
one run's execution state must not reset, overwrite, stash, restore, or otherwise
alter another run's execution state. Any resulting source conflict belongs at
Fruit review, where the Botanist can see and resolve it, not inside execution
where work can be silently lost.

## Done when

A recorded concurrency exercise against the same managed Substrate demonstrates
all of the following with real tasks that each produce work:


- **Two runs from the same Pollen can overlap in execution.** A single Pollinator
  can delegate more than one task without manually waiting for the first to end.
- **Runs from different Pollen can overlap in execution.** Isolation does not
  depend on all work belonging to one caller.
- **Concurrent runs do not share mutable execution state.** No run resets,
  stashes, restores, overwrites, changes branch state for, or otherwise mutates
  another run's working state.
- **Overlapping source edits remain independent during execution.** Two runs may
  change the same source from the same starting revision; any conflict appears
  later as competing Fruit rather than one run destroying or absorbing the
  other's work.
- **Failure is isolated.** A run that withers or otherwise fails does not damage,
  roll back, or contaminate a concurrent successful run.
- **Every successful run produces its own Fruit.** Each result has independently
  identifiable branch and commit state, is reviewable and revertible, and does
  not modify `main`.
- **The Botanist coordinates none of the execution after delegation.** Starting
  concurrent work does not introduce per-run approval, retry, cleanup, or
  sequencing duties.
- **The runs remain separately observable.** Their lifecycle, reach, terminal
  result, and Fruit can be correlated independently while they overlap.


A mechanism that only detects the unsafe case and refuses or serializes the
second run is a valid safety improvement, but it does **not** satisfy this
objective. The capability is multiple independent pieces of work in flight, not
merely an honest declaration that only one is supported.


Whether the resulting Fruit is worth accepting remains a separate Botanist
decision. Two perfectly isolated runs may legitimately produce conflicting or
poor work; that is not an isolation failure.

---

## How to use it

State the objective in the present tense, as a capability rather than a task:
*"a Botanist can X"*, not *"build X"*. A capability can be demonstrated; a task
can only be declared finished.

Give it a condition that someone other than the author can check. "It is safe" is
judged; "nothing outside the declared boundary was touched, across runs that each
changed something" is observed.

**The test for whether work belongs** is not whether it is worth doing — most
filed work is — but whether the objective is unreachable without it. Everything
else goes to Issues. That includes defects found while working: a real defect
discovered on the way is evidence for a *future* objective, and starting it now
is how a month disappears.

**Report, never decide.** Tendril's job is to hold the boundary and to say what
happened. Accepting the result is always the Botanist's, or a gate the Botanist
chose. A feature that requires this project to hold an opinion about the *work*
rather than about the *containment* belongs somewhere else.

**When the condition is met**, write the next objective before starting anything.
An empty objective is what a roadmap grows back into.
