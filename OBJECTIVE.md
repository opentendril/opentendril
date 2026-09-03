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

**A developer unfamiliar with OpenTendril can use it directly as a lightweight
interactive coding runtime, or delegate to it from an existing Pollinator, give
it real coding work, observe and continue the same Stem-mediated interaction,
and receive Git-reviewable Fruit without granting the model ambient host
authority.**

The governed installation and first-Fruit path are now qualified. The next
constraint is the developer interaction once OpenTendril is available.

The visible experience should be deliberately small: connect a model, identify a
Substrate, state a coding task, observe useful progress, provide further intent
when required, and review the resulting Fruit. A developer should not need to
understand Seeds, Sprout lifecycle internals, private APIs, persistence layout or
OpenTendril implementation structure merely to perform normal coding work.

OpenTendril should support this experience in two forms. It can be used directly
as the lightweight coding interface, without requiring another development
harness, or an existing development tool, IDE or automation system can act as the
Pollinator and delegate work to OpenTendril. Changing Pollinators must not create
another execution architecture or move governed business logic out of the Stem
Core.

Local evaluation may run the Stem under the developer's existing OS principal.
That posture does not claim the full governed deployment boundary or principal
separation. It should nevertheless use the same Stem-owned interaction,
execution and Git-result contracts so that moving to the separate-principal
governed posture does not require learning or implementing another developer
interaction model.

Basic model/tool execution and safe observation substantially exist. The
principal missing interaction is continued intent within an active Phytomer. A
Pollinator must not gain a direct channel into a Sprout or Terrarium to provide
that experience. Additional intent must remain Stem-mediated, authorized,
attributable to the Pollen, persisted against the Phytomer and delivered to the
Mycorrhizae through an explicit lifecycle contract.

The interaction state therefore belongs behind the Stem boundary, not in a
terminal interface or transport adapter. This keeps the same contract usable by
a local terminal, an external development tool and, later, a remote Pollinator
without making remote deployment part of this objective.

## Done when

Recorded exercises against real Substrates demonstrate all of the following:

- **The direct experience is small.** From a documented local/evaluation setup, a
  developer can connect a model, select a Substrate, state a real coding task,
  observe it and receive Fruit through a lightweight terminal experience without
  operating another development harness.
- **An existing development tool can use Tendril as its execution target.** At least one
  external development tool can act as a Pollinator through a supported Tendril
  transport, delegate the same class of real coding work and receive the result
  without vendor-specific execution logic in the Stem.
- **Changing Pollinators does not create another implementation.** A second
  suitable Pollinator can exercise the same Stem-owned capability through
  configuration or transport integration rather than a new execution path.
- **The interaction continues inside one governed context.** Additional
  developer intent can be supplied after work has begun without abandoning and
  manually reconstructing the execution context. The continued interaction is
  associated with the same intended Phytomer and remains attributable to the
  exercising Pollen.
- **Steering remains Stem-mediated.** A Pollinator cannot communicate directly
  with a Sprout or Terrarium. Continued intent is authorized and persisted by
  the Stem and reaches the Mycorrhizae only through the implemented interaction
  lifecycle.
- **Useful progress is observable.** The developer can see identity, Substrate,
  lifecycle, meaningful activity, failures, iteration state and Fruit without
  exposure of raw model reasoning or credentials.
- **The model has no ambient host authority.** Normal coding work occurs through
  declared capabilities and isolated execution rather than giving the model an
  unrestricted host shell, host filesystem, privileged credentials or ambient
  network authority.
- **The result remains Fruit.** Successful work produces independently
  identifiable Git branch and commit state, leaves the protected/default branch
  unchanged, and gives the developer enough information to review and accept or
  reject the result.
- **The lightweight interface does not become an authority layer.** Durable
  interaction and execution state is owned behind the Stem boundary. CLI, MCP,
  REST or terminal presentation remains transport/presentation rather than a
  second implementation of governed capability semantics.
- **The measurement records product friction.** Setup steps, Tendril-specific
  concepts exposed to the developer, manual interventions, interaction
  round-trips, token/context overhead and failures are recorded rather than
  hidden. The first exercise establishes a baseline; no arbitrary elapsed-time
  or token threshold is required.

The objective does not require secure remote deployment, GitHub-native workers,
multi-instance workload routing, a particular terminal UI framework or support
for every external development tool. Those are follow-on capabilities.

The test is whether one lightweight Stem-owned interaction model works directly
for a developer and through another Pollinator while preserving OpenTendril's
authority, isolation, observation and Git-review boundaries.

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
