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

**A Botanist unfamiliar with OpenTendril can go from a clean machine to one
accepted Git-reviewable Fruit using the documented secure-default path, without
OpenTendril-internal knowledge or undocumented repair.**

The governed execution boundary can now support unattended, observable and
concurrent work once OpenTendril is running. The next constraint is reaching
that state without relying on the knowledge of the people who built it.

Installation, configuration and first use are part of the autonomy boundary. If
the Botanist must understand OpenTendril internals, discover hidden prerequisites,
repair configuration by hand, know which daemon must be restarted, query internal
APIs, or manually correlate opaque identifiers to understand what is happening,
then the organism still depends on expert supervision before useful delegation
can begin.

The target is therefore a short, secure and observable first-use path. Deterministic
work should be automated where practical; questions should be asked only when
Botanist intent is genuinely required. The secure path is the normal path, not an
advanced configuration the Botanist must discover after setup.

## Done when

A recorded exercise beginning from a clean machine and following only the
documented user-facing path demonstrates all of the following:

- **Prerequisites are discoverable.** Everything required before installation is
  stated or detected. A missing prerequisite produces an actionable diagnosis
  rather than requiring repository or source-code investigation.
- **The secure-default installation produces a usable governed Stem.** The
  Botanist does not need to weaken filesystem, credential, Terrarium or network
  boundaries merely to reach a first useful run.
- **Required configuration is understandable from the user-facing path.**
  Provider, Substrate, Pollinator and other required first-run choices can be
  completed without knowing OpenTendril's implementation structure or editing
  undocumented internal state.
- **A real task can be delegated through a documented supported interface.**
  The Botanist does not need private API knowledge, database access, source-code
  knowledge or ad-hoc shell repair to start useful work.
- **The first run is immediately observable from an ordinary terminal.** One
  documented command is sufficient to follow relevant activity from dispatch
  through terminal state and Fruit. It exposes useful identity, Substrate,
  lifecycle, progress, tool/activity, structured failure and Fruit information
  without exposing raw model reasoning.
- **The resulting Fruit is easy to locate and review.** A successful run produces
  independently identifiable Git branch and commit state, leaves `main`
  unchanged, and gives the Botanist enough information to review and either
  accept or reject the work.
- **No undocumented recovery knowledge is required.** Success does not depend on
  knowing to restart a daemon, alter hidden files, inspect service internals,
  issue raw HTTP requests, query persistence directly or manually reconstruct
  execution state from unrelated logs.
- **The headless path stands on its own.** Greenhouse or another graphical
  interface may improve the experience, but the governed Stem can be installed,
  operated, observed and diagnosed without requiring that optional interface.
- **The measurement records friction rather than hiding it.** Prerequisites,
  ambiguity, failures, manual interventions, working time and waiting time are
  recorded. Any intervention that depends on existing OpenTendril expertise is
  counted as a failure of the first-use path, not silently supplied by the
  tester.

The first exercise establishes the baseline; it does not need to meet an
arbitrary elapsed-time target. Subsequent work should remove measured friction
until the capability above is demonstrated without expert intervention.

A path that succeeds only because the tester already knows OpenTendril does not
satisfy this objective. Likewise, replacing understandable terminal operation
with a mandatory graphical setup surface does not by itself satisfy it:
simplicity and observability are the capability, not any particular interface.

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
