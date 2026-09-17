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

**A Botanist can establish and manage a named Substrate through deterministic
Stem control-plane operations from any working directory, and use that
declaration to produce Git-reviewable Fruit without hand-editing OpenTendril
configuration or allowing a Substrate checkout to control credentials or
delegation authority.**

## Done when

Recorded exercises establish all of the following:

- **Deterministic source of truth.** Ordinary Botanist Substrate administration
  resolves one canonical mutable registry independently of the process working
  directory.
- **No incidental checkout authority.** Merely launching OpenTendril inside a
  repository cannot silently replace the operator-managed Substrate registry.
- **Supported creation.** A Botanist can add a named Substrate using the
  existing supported connection and checkout postures without hand-editing
  YAML.
- **Safe inspection.** Named Substrates can be listed and inspected without
  exposing PAT values, PEM contents, private signing material, Botanist
  credentials, Pollinator credentials, or other secret values.
- **Patch-safe updates.** A Botanist can change explicitly selected Substrate
  fields without replacing unspecified fields or unrelated Substrates and
  credential profiles.
- **Canonical verification.** A named Substrate can be verified using the
  existing deterministic readiness policy rather than a second lifecycle-
  specific policy.
- **Dependency-safe removal.** A Substrate with active delegation references
  cannot be removed. Removing an unreferenced Substrate does not corrupt
  shared credential profiles or delete external secret material, Fruit,
  managed Git history, or remote repository state.
- **Management remains Botanist authority.** Pollinators cannot create, update,
  remove, or otherwise administer persistent Substrate configuration.
- **Delegation remains separate.** Substrate lifecycle operations do not
  silently create, widen, narrow, or remove DelegationGrants.
- **Working-directory independence.** The same ordinary configuration is
  observed when the Botanist invokes the lifecycle from their home directory,
  a Substrate checkout, or another unrelated directory.
- **First-use path reaches Fruit.** Starting without a configured Substrate, a
  Botanist can establish and verify one through supported commands, establish
  explicit delegation authority through the existing grant lifecycle, run
  governed work, and receive Git-reviewable Fruit without manually editing
  OpenTendril YAML.
- **Protected state remains protected.** Configuration lifecycle operations do
  not merge Fruit, mutate the protected/default branch, or perform unintended
  remote Git operations.

This objective does not require:

- redesigning DelegationGrants;
- per-task repository credentials;
- arbitrary repository selection by Pollinators;
- changing Seed, Phytomer, Sprout, Terrarium, or Fruit semantics;
- Greenhouse configuration editing;
- public Pollinator ingress changes; or
- automatic Fruit acceptance or merge.

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
