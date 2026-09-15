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

**An authorised Pollinator on the public Internet can reach a governed
OpenTendril Stem through a deliberately exposed Pollinator ingress, exercise the
existing bounded Seed/Phytomer interaction contract, and receive Git-reviewable
Fruit without gaining Botanist, Stem-host, Substrate, Sprout, Terrarium,
provider, repository, or infrastructure authority.**

## Done when

Externally demonstrable exercises with an authorised Pollinator, a governed
Stem, and a granted Substrate establish all of the following:

- **Internet-remote qualification.** The Pollinator reaches the Stem from a
  genuinely Internet-remote network location, not a shared LAN, local bridge,
  local tunnel, or private same-network host.
- **Supported public exposure.** The ingress uses an explicit, supported public
  Pollinator exposure arrangement; qualification does not depend on
  undocumented port forwarding.
- **TLS before credentials.** The Pollinator completes normal TLS certificate-
  chain and hostname/SAN verification before presenting any Pollinator
  credential.
- **Location is not authority.** Being on the public Internet or reaching the
  ingress grants no capability authority. Governed access still requires
  authenticated Pollen and a matching live DelegationGrant.
- **Roots are not data-route authority.** A durable Pollinator root does not
  become long-lived normal data-route authority over the public Internet;
  access-token expiry is enforced and bounded.
- **Management-plane boundary.** The public Pollinator ingress does not expose
  Botanist or other control-plane authority. The management-plane boundary is
  explicit and remains distinct from Pollinator ingress.
- **Existing governed lifecycle.** Requests reuse the existing Stem Core
  capability authority and bounded Seed/Phytomer interaction lifecycle,
  including safe observation and continuation of the same work.
- **Live revocation.** Root revocation prevents subsequent access-token minting,
  and grant removal or narrowing blocks governed admissions under the live
  grant, without relying on a Stem restart.
- **Safe interruption.** Internet interruption and reconnect preserve safe,
  idempotent Seed/Phytomer interaction; retries do not create replacement work
  or deliver continuation more than once.
- **Unchanged execution boundary.** The existing Sprout/Terrarium isolation is
  unchanged, and there is no Pollinator-to-Sprout or Pollinator-to-Terrarium
  channel. Normal sealed Terrariums receive no implicit Stem environment file
  or ambient Stem credential material; any execution environment is explicitly
  declared by the governed Terrarium/Sprout contract.
- **Fail-closed requests.** Malformed, unauthenticated, wrong-Pollen,
  absent-grant, invalid-TLS, and forwarding-metadata spoof attempts fail closed
  without exercising governed authority.
- **Bounded resource use.** Unauthenticated or minimally authenticated traffic
  cannot cause unbounded resource exhaustion; bounded protection and failure
  behavior are demonstrable.
- **Reviewable Fruit.** Successful work yields Git-reviewable Fruit while
  protected/default state remains unchanged. Fruit is not accepted or merged
  automatically; the Botanist retains that decision.
- **Operational friction is recorded.** The qualification records DNS,
  certificate lifecycle, firewall/NAT or equivalent exposure requirements,
  provisioning, revocation, reconnect, and failure recovery.

This objective does not require:

- GitHub-native workers;
- GitHub Actions identity;
- Greenhouse public deployment;
- Stem federation or multi-instance routing;
- general enterprise IAM;
- automatic Fruit acceptance or merge; or
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
