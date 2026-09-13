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

**An authorised Pollinator on another machine can connect to a governed
OpenTendril Stem, delegate and continue real coding work on a granted Substrate,
safely observe the same Phytomer, and receive Git-reviewable Fruit without
receiving Stem, host, Substrate, Sprout, Terrarium, or infrastructure
credentials.**

## Done when

Recorded exercises with a real Pollinator on another machine and a granted
Substrate demonstrate all of the following:

- **Authentication is explicit, bounded and attributable.** Access uses an
  explicit grant with defined authority and limits, is attributable to a Pollen,
  and revocation fails closed: a revoked or expired grant is denied on every
  subsequent request. A Pollinator cannot create or widen its own grant.
- **Substrate authority stays Stem-side.** The Stem retains target Substrate
  authority and credentials. The remote Pollinator receives no Stem-host shell,
  filesystem, service-management, Docker, Git, provider, or infrastructure
  credentials, nor credentials for the Stem, target Substrate, Sprout, or
  Terrarium.
- **Interaction remains Stem-owned.** Seed growth, safe observation, and
  continuation follow the existing Stem-owned interaction model. The Pollinator
  can delegate real coding work and continue it within the same Phytomer without
  a Pollinator-to-Sprout or Pollinator-to-Terrarium channel.
- **Observation is safe.** The Pollinator can inspect useful identity,
  Substrate, lifecycle, activity, failures, iteration state, and Fruit, but
  cannot observe raw model reasoning or credentials.
- **Terrarium isolation is unchanged.** Remote delegation neither bypasses nor
  weakens the existing Terrarium isolation boundary.
- **Interruption is safe and resumable.** Transport interruption and reconnect
  continue the same Seed and Phytomer; they do not create replacement Seeds or
  deliver continuation more than once.
- **The result remains Fruit.** Successful work produces independently
  reviewable Git branch and commit state, leaves protected/default state
  unchanged, and provides enough information for the Botanist to review and
  accept or reject it.
- **Operational friction is recorded.** Setup steps, manual interventions,
  reconnect and revocation behavior, and failures are recorded rather than
  hidden.

This objective does not require:

- GitHub-specific workers;
- GitHub Actions identity;
- multi-instance routing or Stem federation;
- shared enterprise scheduling;
- Greenhouse implementation;
- arbitrary public Internet exposure;
- auto-merge; or
- weakening Terrarium isolation.

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
