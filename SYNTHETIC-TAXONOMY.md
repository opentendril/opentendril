# Synthetic Biological Taxonomy & Systematics

> **This is the concept-first document — *what OpenTendril is*.** It explains the
> organism and *why* it is built this way. For a terminology-first reference (look
> up a term, translate to/from standard IT), see [GLOSSARY.md](GLOSSARY.md). Both
> derive from the same canonical vocabulary, distilled for the Stem to inject into
> LLM context at [`.tendril/genome/taxonomy-canonical.md`](.tendril/genome/taxonomy-canonical.md).

OpenTendril replaces generic IT terminology with biological and botanical
metaphors drawn from **real botany** — nothing here is invented. By mimicking
systems that have spent billions of years optimising for resilience, modularity,
and adaptation, OpenTendril aims for an architecture that *expects* dynamic,
non-deterministic interpretation rather than rigid mechanical execution.

Every term below names a **real organ, tissue, or process**, and each has exactly
**one job**. Where a word could be overloaded, this document is the authority that
resolves it.

---

## 1. What OpenTendril *Is*

OpenTendril is a **growing organism** that roots itself in a codebase, draws
intelligence from an external mind, and reaches out to integrate with the systems
around it — cultivating software under a best-practice, secure SDLC.

**"Tendril" is the organising concept, not a component.** A tendril is the organ
of *reaching out and attaching* (via **thigmotropism** — directional growth toward
a support). That reaching-out is the whole organism's defining character, which is
why it is the brand (**OpenTendril**) and not any single instantiable part. Every
concrete "reaching" job is filled by a more precise organ — Roots draw
intelligence, a Substrate is cultivated, a Symbiotic Nodule partners with a
service. If you are naming a *thing*, it is never "a Tendril"; it is one of those.

**Naming.** The canonical, written name is **`OpenTendril`** ("Open" for
open-source, and it differentiates from other products named "Tendril"). The
short, spoken name is **`Tendril`** (matching the `tendril` CLI). **Never
abbreviate to "OT"** — in this domain "OT" already means *Operational Technology*
(the IT/OT split in security and SDLC), so the abbreviation actively collides with
a term our users rely on. (Cf. OpenTelemetry, which shortens to "OTel", never "OT".)

---

## 2. Philosophy: Escaping Determinism

Traditional software is built on state machines: a **Task** implies deterministic,
mechanical execution. But Large Language Models are not state machines — they
process language, context, and probability. They are messy, adaptable, organic.

For years the industry has tried to force these organic networks into rigid IT
boxes ("agentic loops" that behave like `while` loops), producing fragile systems
that break on unexpected input and degrade over long contexts — and handing a
stateful "agent" continuous host access is a catastrophic security risk.

OpenTendril embraces biological evolution instead: the same chaotic, adaptable
paradigm neural networks were modelled on. By modelling the system as a plant, we
*accept* that instructions require contextual interpretation, and we contain each
unit of growth so a failure is shed, not fatal.

---

## 3. The Spatial Anatomy

The system is a whole plant, read from the environment it lives in, down through
its roots, up the stem, into the canopy that does the work, and out to the fruit
it yields.

```
              Fruit   (PRs / artifacts — this cycle's yield)
                ▲
   Canopy:   Sprouts (ephemeral workers) inside Terrariums
                ▲
    Trunk:   Stem  +  Xylem / Phloem / Vascular Cambium
                ▲
   ── soil surface ──   Substrate  (the repo — cultivated; Fruit merges back in)
                │  the plant's own root organs tap and store:
   Root system:   Roots → Mycorrhizae (LLM)   ·   Symbiotic Nodules → services
                  Rhizome → code index         ·   Taproot → deliberation store
                │  reaching into …
   Rhizosphere   (the ecosystem of existing technologies Tendril is based on)
                │  all within …
   Terroir  (this machine / OS)      ⊂      Biome  (the ISO / SDLC climate)
```

**The Botanist** (the human) tends the whole plant from outside it: planting Seeds
(intent), pruning, and reviewing Fruit through the **Greenhouse** (the observer UI).

---

## 4. The Cultivation Loop

> The **Botanist** plants a **Seed** (a goal / use-case) into the **Substrate**
> (the repo), within the **Biome** (the ISO 27001 / ISO 42001 / SDLC climate). The
> organism draws intelligence through its **Roots** from the **Mycorrhizal Network**
> (the LLM), routes it up the **Stem's** vascular system, and grows **Sprouts**
> (ephemeral workers) that transform the Substrate. The reasoning behind each
> decision is stored in the **Taproot**; the yield is **Fruit** (a PR), which the
> Botanist reviews through the **Greenhouse** — feeding the next Seed.

Each force in that loop is a distinct organ. None overloads another.

### Lifecycle order

The Glossary and canonical block list terms **alphabetically** (for lookup). This
is the other axis — the primary identifiers in the **order they occur** in one
growth cycle:

1. **Seed** — the Botanist plants intent.
2. **germinate** — the framework boots from the Seed.
3. **Meristem → Phytomer** — the Meristem opens a session (Phytomer).
4. **emerge** — a **Sprout** emerges into its **Terrarium**.
5. **grow** — the Sprout executes its **Transcript**, drawing on the **Roots**
   (Mycorrhizae) and the **Rhizome**; the **Xylem** carries inputs in.
6. **bud** *(optional)* — an **axillary bud** branches into a new Phytomer
   (parallel work or recursion).
7. **mature** / **wither** — success, or failure → **abscise** (the Sprout is dropped).
8. **merge** — the **Phloem** carries the diff home to the **Substrate**; the
   rationale is stored in the **Taproot**.
9. **fruit** — the **Fruit** (PR / artifact) is delivered; the Botanist reviews it
   through the **Greenhouse**, feeding the next **Seed**.

---

## 5. Component Anatomy

### The Environment (not part of the plant)

*   **Biome** — the **normative/regulatory climate** the organism must be adapted
    to: ISO 27001 (infosec), ISO 42001 (AI management), and the SDLC discipline.
    A biome is defined by its climate; grow out of tolerance and you do not
    survive (audit/compliance failure). The Biome sets the conditions; the
    **Immune System** (§7) keeps the organism adapted to them.
*   **Terroir** — the **host OS and machine** this instance grows on. *Terroir* is
    the complete local ground — soil, topography, microclimate — that gives a
    specific site its character; here, the particular machine/OS that shapes what
    can grow. (Formerly referred to plainly as the "Host System".)
*   **Rhizosphere** — the **external ecosystem of existing technologies
    OpenTendril is based on**: the LLM providers, the container runtime, git, MCP
    — the inherited world the roots tap into. It is what Tendril *stands on*, kept
    distinct from what Tendril *grows*. **Roots vs Rhizosphere:** the Rhizosphere
    is the *outside* world (the providers/tools themselves); the **Roots** (below)
    are OpenTendril's *own* code that reaches into it.
*   **Botanist** — the **human** who studies and cultivates the organism. The
    Botanist *gardens* it — plants **Seeds** (intent), prunes, reviews **Fruit** —
    and *studies* it, working from outside the plant and observing through the
    **Greenhouse**. ("Gardener" names the tending activity; the Botanist is the
    person.)
*   **Pollinator** — an **external requester** that reaches *in* and asks the
    organism for governed work: a **Botanist** at a terminal, or an external
    **Mycorrhizal** mind connected over a governed interface. In botany a
    pollinator is not part of the plant — it arrives, exchanges, and leaves, and
    what it brings is what precedes **Fruit**. Here it brings intent and receives
    a deliverable, and the chain reads exactly as botany does: *Pollinator →
    growth → Fruit*.

    A Pollinator is identified by its **delegation subject** — the bound identity
    a grant names — and may run only the operation-classes it has been granted;
    no grant, no access. The mutualism is real but not assumed: pollinators are
    also how disease travels between plants, which is precisely why the grant
    exists.

    **Direction distinguishes it from a Symbiotic Nodule.** A Nodule is an
    external *service* the organism reaches **out** to (GitHub's interface, a
    cloud provider). A Pollinator reaches **in**. Same boundary, opposite
    direction — and the security posture differs accordingly.

    **This is the word that replaces "agent"** for an external requester. Note
    what it is *not*: not a **Sprout** (Tendril's own ephemeral, network-sealed
    worker), and not the **Mycorrhizal Network** itself (the reasoning mind,
    which a Pollinator may *be* or may merely *use*). Collapsing those into one
    word is exactly the imprecision the taxonomy exists to prevent — a Sprout is
    sealed off from the network, a Pollinator is the thing being granted mediated
    access to it.
*   **Greenhouse** — the **Command Center UI**. The pane of glass through which the
    Botanist watches the headless organism grow via telemetry (REST/WebSockets).

### The Root System (acquire, symbiose, remember)

*   **Roots** (`roots/`) — OpenTendril's **own connectivity organ**: the provider
    clients, model discovery, and tier routing (`roots/`) that reach *into* the
    Rhizosphere to draw inference from the Mycorrhizae. The Roots are part of the
    plant; the Rhizosphere is the ecosystem they tap.
*   **Mycorrhizal Network** — **the LLM.** In nature, plant roots join vast fungal
    networks that process signals and feed the plant. In OpenTendril the LLM is the
    Mycorrhizae: it sits *outside* the plant, does all the thinking and predicting,
    and passes command signals in through the Roots. It holds no state of its own.
*   **Substrate** — the **repository being cultivated** — the soil the plant is
    rooted in and feeds on. Distinct from the **Terroir** (the OS the soil sits on)
    and from the **Fruit** (what the plant yields). Changes travel *down* to the
    Substrate via the Phloem, and merged **Fruit** enriches it for the next cycle.
*   **Symbiotic Nodule** (everyday: **Nodule**) — an **external *service*
    capability** the plant partners with (GCP, Datadog, the GitHub *API*), just as
    legume roots host nitrogen-fixing Rhizobia in root nodules. A Nodule interfaces
    with the **Stem**, not with a Sprout. (Contrast: a *repo* is a Substrate; the
    *API* that opens its PRs is a Nodule.)
*   **Rhizome** — the **code index**. A continuous underground stem that stores and
    connects; here, a background scanner that parses the Substrate into a
    topological SQLite/AST map for the organism to draw from.
*   **Taproot** — the **deep, persistent store of deliberation**: conversations,
    Design RFCs, decisions *and their rationale*, reviews, and working drafts — the
    *why and how* a product was built. A taproot stores energy across seasons for
    the plant to draw on; the Taproot is the organism's memory of its own intent,
    the highest-value knowledge the Mycorrhizae (LLM) cannot hold between runs.
    *(New capability — see the Taproot Design RFC.)*

### The Trunk (support & vascular routing)

*   **Stem** — the lean, deterministic **Go orchestrator** (`cmd/stem`). It is
    explicitly **not** a brain (plants have none); it is a vascular routing highway
    that receives signals from the Mycorrhizae and routes them to a Branch,
    triggering a Sprout to grow. It holds no business logic.
*   **Vascular System (Xylem & Phloem)** — the transport tubes. **Xylem** carries
    Transcripts (inputs) *up* to the Sprouts; **Phloem** carries git diffs
    (synthesised energy) *down* from the Sprouts to the Substrate.
*   **Vascular Cambium** — the tissue coordinating parallel Xylem/Phloem tubes: the
    concurrent step runner managing multiple isolated Terrarium channels.
*   **Branch** — a **Capability Group / Security Zone** the Stem initialises at
    boot to organise capabilities and limit access scope for Sprouts.

### The Canopy (do the work, yield)

*   **Sprout** — an **ephemeral, strictly isolated worker** dynamically grown on a
    Branch to perform one unit of work. Leaves/sprouts are short-lived — a Sprout
    emerges, grows, and either matures or withers, then is shed.
*   **Terrarium** — the **isolation boundary** (filesystem + network) wrapping a
    Sprout, protecting the host. A sealed Sprout cannot reach out on its own —
    external calls are Stem-mediated (see §6).
*   **Phytomer** — a **Session**: one persistent interaction thread, and the
    repeating module the organism iterates to build itself (a phytomer = node +
    internode + leaf + bud). The **Meristem** initiates a Phytomer; **Sprouts
    emerge** from it over time; at maturity it roots its deliberation into the
    **Taproot**. *(Two cadences are reserved as telemetry metrics, adopted when the
    Greenhouse needs them: the **plastochron** — the interval between new Phytomers;
    and the **phyllochron** — the rate at which Sprouts emerge within one.)*
*   **Axillary bud** — a **dormant branch point** carried by a Phytomer. It stays
    dormant, or **buds** — activates into a new Phytomer chain: a branch, a
    parallel line, or a recursive sub-task (in grasses, a *tiller*). This is how
    work forks and recurses; the Conductor already does it (e.g. budding a
    recursive Debugger).
*   **Fruit** — the **deliverable**: the PR or artifact a cycle yields. Fruit is
    delivered, and its merge returns seeds to the Substrate (soil) for the next
    cycle.
*   **Seed** — the **originating goal / intent / use-case**: the blueprint (plus
    stored energy) from which a body of work grows. A single task's intent is a
    **Transcript**; the product-level intent is a **Seed**. Seed (forward intent)
    and **Taproot** (recorded rationale) are the two ends of the organism's
    memory of purpose.

### Microservices & Symbiosis

*   **Symbiotic Nodules** are OpenTendril's microservices: rather than bloating its
    core DNA (a monolith), the organism partners with specialised external
    organisms for capabilities it does not natively build in.
*   **Abscission** — biological fault tolerance. If a Sprout withers (crash, panic,
    LLM timeout), the Stem **abscises** it — drops it and routes around — and the
    core organism survives.

### How to classify a new capability (decision heuristic)

Before proposing *any* new capability, classify **which organ it belongs to**. Placement is a
taxonomy decision, not just a naming one — getting it wrong bloats the wrong organ.

*   **Local computation on the plant's own code/state → a core organ, in the Stem.** The **Rhizome**
    *is* the parser ("parses the Substrate into an AST map"), so `go/ast` and the in-process tree-sitter
    engine live here — **a parser is never a Nodule.**
*   **An external network *service* the plant partners with → a Symbiotic Nodule.** GCP, Datadog, the
    GitHub *API*. A Nodule interfaces with the Stem and keeps the core unbloated (does one thing well,
    off-binary).
*   **A repository the plant works on → a Substrate.** (Contrast: the *repo* is a Substrate; the *API
    that opens its PRs* is a Nodule.)
*   **OpenTendril's own connectivity code reaching into the outside world → a Root** (`roots/`); the
    outside world it taps is the **Rhizosphere** — never OpenTendril's own code.

**Litmus test:** does it run locally on our own material, or call out to a foreign service? Local ⇒ a
core organ (Rhizome/Stem); foreign service ⇒ a Nodule/Root. State this classification in every Design
RFC (the "Taxonomy placement" section).

---

## 6. Who Reaches Out? (the call path)

A sealed Sprout inside a Terrarium is network-isolated and **cannot** call an
external system itself. The path is:

> **Mycorrhizae (LLM) decides** an external capability is needed and emits the
> intent → **the Stem invokes** the Nodule/Substrate (it holds the credentials and
> network) → **the Sprout consumes** the result (injected via the Xylem) and yields
> code (returned via the Phloem).

So it is neither the LLM nor the Sprout that "calls GitHub" — **the Stem does**, on
intent the LLM produced. This matches the security model (the Terrarium forbids a
Sprout from reaching out) and the implementation (clone/push run Stem-side, and in
enterprise are delegated further via Stem Grafting to a Central Stem).

---

## 7. The Immune System (Security & Quality)

*   **Hormonal Triggers** — pre-execution security gates (lightweight scripts) that
    can instantly *block growth* (abort) before a Sprout even emerges.
*   **Automated Test Suite** — the adaptive immune response, running in sterile
    Terrariums to reject harmful mutations (failing PRs) before they integrate.
*   **System vs Workspace definitions** — *Workspace* Genotypes/Sequences live in
    `.tendril/` and are user-editable, so they are **not trusted** for privileged
    decisions (a Sprout can modify them). *System* Genotypes/Sequences ship with
    OpenTendril, are never mounted into any Terrarium, and carry immutable deny
    lists. *(Trusted-tier location — `~/.tendril` vs a separate `~/.opentendril` /
    `/etc/opentendril` — is an open reconciliation.)*

---

## 8. The Genetic Prompt Hierarchy & Verbs

**Genetics.** **Genotype** = the base persona/system prompt (*who* a Sprout is).
**Plasmid** = a modular, injectable block of context or tools (*a skill*).
**Transcript** = one instruction for one growth (*a single task*). **Sequence** = an
ordered YAML workflow of steps (*a pipeline*), triggered only by the Stem or the
Botanist — never by a Sprout. **Meristem** = a Sequence step that dynamically plans
new steps and translates messy human requests into strict botanical instructions;
botanically the *shoot apical meristem* is where **Phytomers** (§5, sessions)
initiate, so the Meristem is what opens a new Phytomer. **Phenotype** = speculative
parallel variants of one Transcript, of which the first to pass the fitness test
survives (natural selection).

**Strict botanical verbs** (each has a fixed meaning):

| Verb | Meaning | Replaces |
|---|---|---|
| **abscise** | the Stem drops a withered Sprout | fault-isolate |
| **branch** | the Stem initialises a capability/security zone | — |
| **bud** | an axillary bud activates into a new Phytomer chain | fork / branch a session |
| **emerge** | a Sprout is created | start / launch |
| **fruit** | deliver a PR / artifact | output / deliver |
| **germinate** | activate / boot the framework from a Seed | init / start |
| **graft** | **reserved:** fuse two *like* tissues into one organism | — |
| **grow** | a Sprout executes its Transcript | **run / execute** |
| **mature** | a Sprout completes successfully | complete |
| **merge** | changes return home to the Substrate (via the Phloem) | merge / integrate |
| **nodulate** / *form symbiosis* | connect a Symbiotic Nodule | attach a plugin |
| **wither** | a Sprout fails or crashes | fail |

**"Graft" is reserved.** Botanically, grafting fuses tissue from **two different
plants** into one organism. It is therefore correct **only** for **Stem Grafting**
(joining a local Stem to a remote Stem — the mesh) and **Genotype Grafting**
(fusing two personae/plasmids into a hybrid). It is **wrong** for attaching a
Nodule (that is *symbiosis*) or for merging changes home (that is **Phloem**
transport / a **merge**).

---

## 9. The Translation Boundary

The words *agent*, *task*, and *run* are not purged everywhere —
they survive **only at the deliberate external boundary** where OpenTendril meets
people who speak IT jargon: the `AGENTS.md` builder-instructions file (a cross-tool
standard *filename*) and the **Meristem** / intent-translation layer, whose job is
exactly to translate "run an agent workflow" into botanical instructions. Inside
the organism — architecture docs and code identifiers — the language is botanical.

---

## 10. Interpretation by the Stem (Machine Use)

These two documents are the **human-facing** authority. Their vocabulary is
distilled into a compact, unambiguous, machine-injectable block at
[`.tendril/genome/taxonomy-canonical.md`](.tendril/genome/taxonomy-canonical.md),
which the Stem loads as a canonical **Plasmid** so every Sprout — and the
Mycorrhizae (LLM) it consults — speaks the correct language and cannot drift back
to IT jargon. When these documents change, that canonical block is regenerated
from them; it is the single source the organism reads about itself.
