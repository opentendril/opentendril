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

**A Botanist unfamiliar with OpenTendril can go from a clean supported machine to one reviewed Git Fruit using the governed installation path and Greenhouse as the primary workbench, without needing OpenTendril-internal knowledge or separate command-line orchestration for the normal work lifecycle.**

## Done when

* **Governed installation works from a clean supported machine.** Installation succeeds through the documented public entrypoint.
* **The protected Stem boundary exists before work.** The protected Stem principal and security boundary are established before any task begins.
* **Setup requirements are finite and explicit.** Every required provider, Substrate, and Git configuration choice is documented and presented as an explicit input.
* **Setup leads naturally to Greenhouse.** The documented installation path takes the Botanist to Greenhouse as the primary workbench.
* **Authentication stays explicit.** Botanist authentication is explicit, and local connectivity does not confer authority.
* **The normal work lifecycle is handled through Greenhouse.** After setup, the Botanist can handle the task, verifier, dispatch, continuation, explicit Botanist intervention, terminal state, and Fruit review through Greenhouse.
* **First local value needs no internal orchestration.** The normal local first value does not require Pollinator credentials, token minting, DelegationGrant construction, raw REST calls, or internal identifiers.
* **Greenhouse reports Stem-owned facts.** It visibly reports the task, Substrate, execution boundary, activity, verification, failures, and resulting Fruit from facts owned by the Stem.
* **Fruit remains under Botanist review.** The default branch remains unchanged until the Botanist reviews and merges the resulting Fruit.
* **Re-entry preserves authoritative state.** Refresh, reconnect, and restart preserve authoritative state without duplicating work.
* **Supported failures are actionable.** Failures on the supported path provide diagnostics that tell the Botanist what to do next.
* **Clean-machine qualification records friction.** The final qualification records elapsed time, manual decisions, and friction.

This objective does not require:

* public-Internet ingress;
* GitHub-native Pollinators;
* automatic Fruit acceptance or merge;
* enterprise SSO or multi-user administration;
* removing advanced CLI or MCP surfaces; or
* exposing private Mycorrhizal reasoning.

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
