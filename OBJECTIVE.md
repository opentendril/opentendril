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

**A Botanist can use the Greenhouse as the primary workbench for a local OpenTendril Stem: initiate and continue meaningful governed work through existing Stem capabilities, watch and understand what the work is doing and where it is allowed to operate, perform explicit Botanist actions when required, and review the resulting Git Fruit without needing OpenTendril-internal identifiers, raw telemetry, or separate command-line orchestration for the normal workflow.**

## Done when

* **Meaningful governed work can be driven from Greenhouse.** A Botanist can select an existing configured Substrate, give OpenTendril a meaningful coding task, initiate governed work, continue that work where the existing lifecycle permits it, and reach a terminal result through the local Stem rather than through Greenhouse-owned execution logic.
* **The work is understandable while it happens.** Greenhouse presents the human-recognisable task, Substrate, execution boundary, current activity, verification progress, failures, and terminal state from Stem-owned observation so a Botanist can understand what is happening without interpreting raw EventBus traffic.
* **The boundary is visible.** The Botanist can see enough authoritative information to understand where the work is permitted to operate and what isolation or execution boundary is in force without Greenhouse inventing containment facts.
* **Normal Botanist interaction stays inside existing Stem authority.** Greenhouse uses documented Botanist-authenticated Stem interfaces for actions and observation. Business logic, authorization, capability decisions, lifecycle decisions, and execution remain in the Stem.
* **Existing adapter architecture remains intact.** Governed capabilities continue to be owned by the Stem Core and projected through CLI, REST, and MCP adapters. Greenhouse uses browser-appropriate REST and WebSocket projections rather than invoking CLI processes or implementing capability logic itself.
* **Locality does not confer authority.** Local transport provides connectivity only. Botanist control-plane operations require explicit Stem authentication, and Greenhouse does not gain access to the Stem principal, protected state, credentials, executable, Substrate backing stores, or Terrarium runtime authority.
* **Botanist-only authority is not leaked into Pollinator surfaces.** A Botanist control-plane operation needed by Greenhouse must use or gain an appropriate Botanist-authenticated local projection. It must not be exposed through Pollinator MCP merely to create transport parity.
* **Fruit is the visible outcome.** At terminal completion the Botanist can understand what changed, whether verification passed, and where the Git-reviewable Fruit can be inspected.
* **Internal identifiers are secondary.** Phytomer IDs, Sprout-run IDs, Seed handles and similar identifiers remain available for precise inspection and diagnostics but do not dominate the primary workflow.
* **Deep evidence remains available.** Detailed lifecycle evidence, tool activity, raw telemetry, provider diagnostics and identifiers remain inspectable through progressive disclosure without overwhelming the normal work view.
* **Refresh and reconnect preserve truth.** Greenhouse reconstructs active and recent workbench state from Stem-owned durable observation and does not become an independent lifecycle store.
* **Private Mycorrhizal reasoning is not required.** Visibility means observable actions, boundaries, tool activity, verification, failures and results, not hidden reasoning or unrestricted model internals.

This objective does not require:

* redesigning Stem Core capability ownership;
* making Greenhouse an execution engine;
* invoking local CLI commands from the browser;
* exposing Botanist control-plane authority through Pollinator MCP;
* completing public-Internet ingress qualification;
* activating GitHub-native Pollinators;
* implementing a separate terminal observation surface;
* automatic Fruit acceptance or merge; or
* completing the broader fresh-install and first-value onboarding objective, which follows this workbench capability.

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
