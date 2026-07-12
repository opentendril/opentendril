# OpenTendril Glossary

> **This is the terminology-first reference — *learn or translate a term*.** Look
> a word up, or map between OpenTendril and standard IT. For the concept-first
> narrative (*what OpenTendril is* and *why*), see
> [SYNTHETIC-TAXONOMY.md](SYNTHETIC-TAXONOMY.md). Both are authoritative and kept
> in sync; the Stem injects a distilled form (`.tendril/genome/taxonomy-canonical.md`).

OpenTendril uses biological and botanical metaphors — all **real botany, nothing
invented** — rather than standard IT terminology, to emphasise the adaptable,
organic nature of LLM orchestration. Every term has **one job**.

---

## 🌿 Canonical Definitions

The single source of truth for each term, its one job, what it replaces, and its
lifespan. (This table is the basis for the Stem-injectable canonical block.)

| Term | One-line definition | Standard IT | Lifespan |
|---|---|---|---|
| **Abscission** | The Stem dropping a withered Sprout to protect the organism. | Fault isolation / fault tolerance | event |
| **Amber** | Hardened, archived, immutable logs. | Archived log storage | persistent |
| **Axillary bud** | A dormant branch point on a Phytomer; buds into a new Phytomer chain (branch, parallel, recursion). | Fork / branch point | per-run |
| **Biome** | The normative/regulatory climate the organism must adapt to (ISO 27001, ISO 42001, SDLC). | Compliance / governance regime | environment |
| **Botanist** | The human who studies and cultivates the organism — plants Seeds, prunes, reviews Fruit (the *gardening* activity), and observes through the Greenhouse. | Operator / developer | external |
| **Branch** | A capability group / security zone the Stem initialises at boot. | Capability group / security zone | persistent |
| **Epigenetics** | Distilled learnings appended after runs and fed forward into prompts. | Persistent memory / self-correction | persistent |
| **Fruit** | The deliverable a cycle yields (a PR / artifact). | Output / deliverable | per-cycle |
| **Genotype** | The base persona / system prompt (*who* a Sprout is). | System prompt / persona | definition |
| **Greenhouse** | The Command Center UI the Botanist observes through. | Dashboard / observability UI | external |
| **Hormonal Trigger** | A pre-execution security gate that can abort before a Sprout emerges. | Security gate / middleware | per-run |
| **Meristem** | A Sequence step that dynamically plans steps and translates human intent. | Planner / workflow conductor | per-run |
| **Mycorrhizal Network** | The LLM — the external mind that reasons; holds no state. | Large Language Model | external |
| **Phenotype** | Speculative parallel variants of one Transcript; first to pass survives. | Speculative parallel execution | per-run |
| **Phloem** | Vascular channel carrying diffs (outputs) down to the Substrate. | Output pipe | per-run |
| **Phytomer** | A session — one persistent interaction thread; the repeating module the Meristem initiates and Sprouts emerge from. | Session / interaction thread | persistent |
| **Plasmid** | A modular, injectable block of context or tools (*a skill*). | RAG context / tool definition | definition |
| **Resin** | Live internal logging trace. | Debug log / trace | transient |
| **Rhizome** | The background code/AST index of the Substrate. | Vector DB / code index | persistent |
| **Rhizosphere** | The **external** ecosystem the Roots tap — the LLM providers, Docker, git, MCP themselves. *(The outside world; contrast **Roots**, which is OpenTendril's own code that reaches into it.)* | Underlying tech stack / dependencies | environment |
| **Roots** | OpenTendril's **own** connectivity organ (`roots/`) — provider clients, model discovery, and tier routing that reach into the Rhizosphere to draw inference from the Mycorrhizae. | Provider client / SDK layer | persistent |
| **Seed** | The originating goal / intent / use-case; the blueprint work grows from. | Product goal / use-case / intent | definition |
| **Sequence** | An ordered YAML workflow of steps. | Agentic pipeline / workflow | definition |
| **Sprout** | An ephemeral, strictly isolated worker that does one unit of work. | Worker container (*not* "agent") | ephemeral |
| **Stem** | The lean, deterministic Go orchestrator and vascular router. | Core orchestrator / backend | persistent |
| **Substrate** | The repository being cultivated (the soil). | Target repo / codebase | persistent |
| **Symbiotic Nodule** (Nodule) | An external *service* capability the plant partners with (GCP, GitHub API). | Microservice plugin / MCP gateway | persistent |
| **Taproot** | The deep, persistent store of deliberation: conversations, RFCs, decisions + rationale, reviews. | Decision record / institutional memory | persistent |
| **Tendril** | **Concept / brand only** — the organism's reaching-out-and-integrating character. Never a component. | *(the project itself)* | — |
| **Terrarium** | The isolation boundary (filesystem + network) around a Sprout. | Sandbox / container | ephemeral |
| **Terroir** | The host OS and machine this instance grows on. | Host system / OS | environment |
| **Transcript** | One instruction for one growth (a single task). | User prompt / task | per-run |
| **Transporter** | Pub-sub emitter pushing metrics/events across the system boundary. | Telemetry / pub-sub adapter | persistent |
| **Vascular Cambium** | Tissue coordinating parallel Xylem/Phloem channels. | Concurrent step runner | per-run |
| **Xylem** | Vascular channel carrying Transcripts (inputs) up to Sprouts. | Input pipe | per-run |

---

## 🌿 OpenTendril → Standard IT (with botanical rationale)

| OpenTendril | Standard IT | Why this organ |
|---|---|---|
| **Biome** | Compliance/regulatory regime | A biome is defined by *climate* — the conditions you must be adapted to survive. |
| **Terroir** | Host OS / machine | The complete local ground that gives a specific site its character. |
| **Rhizosphere** | Underlying tech ecosystem | The active root-zone of existing life the roots tap into. |
| **Botanist** | The directing human | Studies *and* cultivates (gardens) the organism — an active tender, not passive climate. |
| **Greenhouse** | Observability UI | The glass the Botanist watches the organism through. |
| **Mycorrhizal Network** | LLM | A fungal symbiont *outside* the plant that does the thinking. |
| **Substrate** | Target repo | The soil the plant is rooted in and cultivates. |
| **Symbiotic Nodule** | Service integration | A root nodule housing a symbiont (Rhizobia) for a capability the core lacks. |
| **Taproot** | Decision/institutional memory | The deep root that stores energy across seasons. |
| **Sprout** | Ephemeral worker | A short-lived shoot; emerges, grows, matures or withers, is shed. |
| **Fruit** | Deliverable / PR | The yield; its merge returns seeds to the soil. |
| **Seed** | Goal / use-case | The blueprint (plus stored energy) a body of work grows from. |

---

## 💻 Standard IT → OpenTendril

| Standard IT / AI | OpenTendril | Note |
|---|---|---|
| Agent / autonomous worker | **Sprout** (body) + **Mycorrhizae** (mind) | Tendril deliberately *decomposes* "agent"; it is not one thing. |
| Session / interaction thread | **Phytomer** | The Meristem initiates it; Sprouts emerge from it. |
| Task / user prompt | **Transcript** (single) / **Seed** (product-level intent) | |
| Run / execute | **grow** (verb) | A Sprout *grows* its Transcript. |
| Workflow / pipeline | **Sequence** | |
| Orchestrator / backend | **Stem** | |
| LLM | **Mycorrhizal Network** | |
| Provider client layer | **Roots** (`roots/`) | |
| Target repo | **Substrate** | |
| Host OS / machine | **Terroir** | |
| Tech stack / dependencies | **Rhizosphere** | |
| Compliance regime (ISO 27001/42001) | **Biome** | |
| Microservice plugin (GCP/Datadog/GitHub API) | **Symbiotic Nodule** | |
| Code index / vector DB | **Rhizome** | |
| Decision record / memory of *why* | **Taproot** | |
| Sandbox / container | **Terrarium** | |
| System prompt / persona | **Genotype** | |
| RAG context / tool definition | **Plasmid** | |
| Speculative parallel execution | **Phenotype** | |
| Security gate | **Hormonal Trigger** | |
| Fault isolation | **Abscission** | |
| Live logs → archived logs | **Resin** → **Amber** | |
| Telemetry emitter | **Transporter** | |

---

## 🔤 Botanical Verbs

| Verb | Meaning | Replaces |
|---|---|---|
| **germinate** | activate / boot the framework from a Seed | init / start |
| **branch** | the Stem initialises a capability/security zone | — |
| **emerge** | a Sprout is created | start / launch |
| **bud** | an axillary bud activates into a new Phytomer chain | fork / branch a session |
| **grow** | a Sprout executes its Transcript | run / execute |
| **mature** | a Sprout completes successfully | complete |
| **wither** | a Sprout fails or crashes | fail |
| **abscise** | the Stem drops a withered Sprout | fault-isolate |
| **fruit** | deliver a PR / artifact | output / deliver |
| **nodulate** / *form symbiosis* | connect a Symbiotic Nodule | attach a plugin |
| **graft** | **reserved:** fuse two *like* tissues (Stem↔Stem mesh, or two Genotypes) | — |

---

## 🤖 Interpretation by the Stem

The **Canonical Definitions** table above is the source for
`.tendril/genome/taxonomy-canonical.md`, a compact block the Stem injects as a
canonical **Plasmid** into LLM context so every Sprout speaks correct OpenTendril
and cannot drift back to IT jargon. When this glossary changes, regenerate that
block from it.
