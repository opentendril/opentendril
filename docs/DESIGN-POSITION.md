# Position: what this competes with, and what it is not

Recorded because it was reasoned out once and will otherwise be reasoned out
again, differently. `OBJECTIVE.md` says what to build next; `README.md` says what
this is; this says why it should exist at all, and what it declines to do.

## The shape is not novel, and pretending otherwise wastes everyone's time

An isolated environment where a model works freely and the result lands in version
control for a human to review is a shipped, mature product. GitHub's coding
assistant does it. So does OpenAI's cloud offering, and several others. Anyone
evaluating this project will already know that, and a pitch that ignores it reads
as ignorance rather than confidence.

**What is different is not the shape. It is where it runs, what it costs, and
whether you can see it.**

## Why it still earns its place

Measured against the hosted alternatives by someone who used one:

- **Cost.** The hosted products charge a subscription and then meter execution.
  Cost accrues per run, so the more useful it is the more it costs, which is a
  perverse shape for something meant to run unattended and often. Here the compute
  is a machine you already own and have already paid for.
- **Speed.** A modest laptop outperforms a shared hosted runner, and the runner is
  also queueing. Latency is what makes a delegation loop feel worth using.
- **Observability.** The hosted products are opaque while they work: you wait, and
  a change appears. That is the complaint that sends people back to approving each
  step, because blindness feels worse than the interruption did.
- **Reach.** A hosted assistant is contained partly *by being outside your
  network* — which is also why it cannot do work that requires being inside it: an
  internal registry, a staging database, a private service, an air-gapped
  environment. Running inside the perimeter and still contained is a position a
  hosted vendor structurally cannot take.
- **Choice of model and of boundary.** Provider-neutral, including local
  inference, with a containment boundary you configure and can attest to rather
  than one you are asked to trust.

**Cost and speed are the wedge; sovereignty is the later business.** They are
different audiences and mixing them produces a pitch that lands with neither. "It
is free and runs on your laptop" travels by word of mouth. "You can attest to the
boundary" is a procurement conversation, and it comes after.

## What nobody publishes

No vendor publishes a containment success rate. Isolation is asserted everywhere
and measured nowhere. `OBJECTIVE.md` makes measuring it the first objective, which
means the number exists here before a competitor has one — and the number is worth
something to the field independently of this project's fortunes.

## Explicit non-goals

These are not deferred. They are other projects, and saying so is kinder than
leaving them looking merely postponed.

- **Judging whether the work is any good.** Tests, continuous integration and
  human review already do this well. The rule is *report, never decide*: this
  project holds the boundary and says what happened, and acceptance is always the
  Botanist's or a gate the Botanist chose.
- **Review, history, revert, acceptance.** Version control has owned these for
  twenty years and does them better than anything built here would.
- **Selecting a winner among speculative attempts.** Judgment about work.
- **A registry of shareable capabilities.** A different product with a different
  customer.
- **A separate operating surface.** A consumer of the evidence stream, which is
  the shape of a separate thing rather than part of this one.

The test that produced this list: **when a feature needs this project to hold an
opinion about the work rather than about the containment, it belongs elsewhere.**

## Where this is weakest

Recorded so it is met deliberately rather than discovered.

- **Understanding cost.** The vocabulary that keeps the internals coherent is a
  tax at the boundary. It has to be possible to understand what this does without
  learning any of it.
- **Time to first useful run.** Never measured. If it is an afternoon, word of
  mouth cannot work regardless of how good the message is. Likely the objective
  after this one.
- **Network and dependencies.** Sealing the network makes containment easy and
  real work hard, since installing dependencies needs egress and egress is the
  exfiltration path. The current answer is to provision with network before the
  model starts and run with none — which is how container builds already work, and
  is presently done for the executor's own toolchain but not for the workspace's.
