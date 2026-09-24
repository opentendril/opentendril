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

**A Botanist can identify the deterministic lifecycle stage of a failed Sprout through bounded, durable observation, without inspecting private Mycorrhizal reasoning, credentials, arbitrary host paths, unrestricted internal errors, or Stem-shell state.**

## Done when

Recorded exercises establish all of the following:

* **Pre-provider failure is identifiable.** A deliberately induced Substrate access failure leaves durable observation identifying `failureStage=substrate-resolution` and `diagnosticCode=substrate-access-denied`, while preserving `failureCategory=execution-failed` and `providerRequestAttempted=false`.
* **Failure stage is deterministic.** Failure-stage and diagnostic-code values use a closed deterministic vocabulary distinct from existing `FailureCategory` semantics.
* **Typed facts originate at the failure boundary.** The deterministic lifecycle layer that encounters a failure supplies the applicable typed facts; observation persistence and read surfaces preserve them.
* **Adapters do not diagnose.** REST, MCP, CLI, and Greenhouse surfaces transport or render typed observation and do not infer lifecycle stages by parsing free-form error strings.
* **Observation remains bounded and safe.** Delegated observation exposes no credentials, raw provider payloads, arbitrary host paths, repository contents, private Mycorrhizal reasoning, or unrestricted internal errors.
* **Failure evidence survives the run.** The diagnostic observation remains durably available after the failed Sprout terminates without requiring Stem-shell access.
* **Existing authority and lifecycle semantics remain unchanged.** Sprout outcome semantics, provider selection, authorization, DelegationGrants, Terrarium containment, Git authority, and Fruit acceptance remain unchanged.

This objective does not require:

* changing existing `FailureCategory` semantics;
* changing provider selection or Sprout success/failure behavior;
* changing DelegationGrants, Pollen authority, or Terrarium containment;
* adding reasoning to the Stem;
* inferring typed facts from arbitrary logs or free-form error strings;
* exposing unbounded diagnostics or private execution details;
* completing the parked public-Internet qualification;
* activating the GitHub-native Pollinator objective; or
* Greenhouse redesign beyond rendering the same safe observation contract if required.

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
