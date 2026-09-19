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

**A Botanist can delegate a real coding task on a large Substrate without
manually curating repository context, because OpenTendril supplies the
Mycorrhiza with a bounded, task-specific, provenance-visible set of current
Substrate evidence while preserving the existing execution, isolation, and
Git-review boundaries.**

## Done when

Recorded exercises establish all of the following:

- **Automatic task-specific context.** A Transcript can enter normal governed
  work without the Botanist manually selecting every file, symbol, test,
  document, or memory the Mycorrhiza should inspect first.
- **Bounded independently of repository size.** The evidence supplied before a
  Mycorrhizal turn has an explicit hard bound. A larger Substrate cannot cause
  whole-repository context injection or unbounded prompt growth.
- **Current evidence.** Repository evidence is bound to the actual execution
  workspace and current Git state used by the Sprout. Stale indexed evidence
  cannot silently be presented as current.
- **Task specificity is observable.** Different materially different
  Transcripts against the same Substrate can produce different bounded
  evidence sets, while direct file and symbol anchors named by a Transcript are
  deterministically represented when they exist.
- **Provenance is visible and safe.** The Botanist can inspect which evidence
  was supplied to a Sprout, where it came from, why it was admitted, its current
  revision or content identity, and whether it was truncated or omitted by a
  bound. Provenance follows the existing authorization and redaction boundaries
  and does not require exposing secret values or private reasoning.
- **Existing repository knowledge remains authoritative.** Context assembly
  uses existing repository knowledge and execution state where that evidence can
  be safely attributed to the active Substrate, rather than creating a second
  repository index or hidden knowledge store.
- **Growth refreshes context.** A later Sprout growth, including a later Seed
  iteration, assembles against the workspace state it will actually execute
  against rather than reusing an unverified earlier snapshot.
- **Source boundaries remain deterministic.** Context assembly cannot read
  outside the declared Substrate or widen its evidence sources, limits,
  credentials, network access, tools, or execution authority through
  Transcript content. Evidence whose binding to the active Substrate cannot be
  established is omitted rather than admitted ambiguously.
- **The Stem remains non-cognitive authority.** Any Mycorrhizal reasoning remains
  on the cognitive side of the boundary. Deterministic Stem policy controls
  admissible sources, limits, execution authority, and what is injected.
- **Context assembly does not become work product.** OpenTendril-generated
  context state is not attributed to the Sprout, committed as Fruit, or allowed
  to contaminate the Botanist's repository state.
- **Observation does not expose private reasoning.** Context provenance and
  selection facts can be reviewed without requiring raw Mycorrhizal reasoning
  to be persisted or displayed.
- **Real governed work uses the mechanism.** At least one recorded coding run
  against a Substrate whose available repository evidence materially exceeds
  the configured context bound consumes the assembled context and produces
  independently identifiable Git-reviewable Fruit.
- **Protected state remains protected.** Context assembly does not advance the
  protected/default branch, accept Fruit, or change the existing
  Sprout/Terrarium containment boundary.

This objective does not require:

- persistent repository cognition or a new durable knowledge model;
- replacing the Rhizome or MemoryBackend;
- semantic embeddings, vector search, LSP, or any particular retrieval
  technology;
- exposing context assembly as a Pollinator-facing governed capability;
- changing DelegationGrants, credentials, egress, Terrarium authority, or Fruit
  acceptance;
- Greenhouse redesign;
- public Pollinator ingress or GitHub-native Pollinator work;
- whole-repository context loading;
- automatic Fruit acceptance or merge; or
- OpenTendril deciding whether the Mycorrhiza's code or reasoning is good.

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
