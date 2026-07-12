# OpenTendril Glossary

> **This is the terminology-first reference — *learn or translate a term*.** Look
> a word up, or map between OpenTendril and standard IT. For the concept-first
> narrative (*what OpenTendril is* and *why*), see
> [SYNTHETIC-TAXONOMY.md](SYNTHETIC-TAXONOMY.md). Both are authoritative and kept
> in sync; the Stem injects a distilled form (`.tendril/genome/taxonomy-canonical.md`).

OpenTendril uses biological and botanical metaphors — all **real botany, nothing
invented** — rather than standard IT terminology, to emphasise the adaptable,
organic nature of LLM orchestration. Every term has **one job**; overloads are
called out in [§ Corrected & Deprecated](#-corrected--deprecated-usages).

---

## 🌿 Canonical Definitions

The single source of truth for each term, its one job, what it replaces, and its
lifespan. (This table is the basis for the Stem-injectable canonical block.)

| Term | One-line definition | Standard IT | Lifespan |
|---|---|---|---|
| **Biome** | The normative/regulatory climate the organism must adapt to (ISO 27001, ISO 42001, SDLC). | Compliance / governance regime | environment |
| **Terroir** | The host OS and machine this instance grows on. | Host system / OS | environment |
| **Rhizosphere** | The ecosystem of existing technologies Tendril is based on (LLM providers, Docker, git, MCP). | Underlying tech stack / dependencies | environment |
| **Botanist** | The human who studies and cultivates the organism — plants Seeds, prunes, reviews Fruit (the *gardening* activity), and observes through the Greenhouse. | Operator / developer | external |
| **Greenhouse** | The Command Center UI the Botanist observes through. | Dashboard / observability UI | external |
| **Mycorrhizal Network** | The LLM — the external mind that reasons; holds no state. | Large Language Model | external |
| **Roots** | The LLM provider connectivity layer (`roots/`). | Provider client / SDK layer | persistent |
| **Substrate** | The repository being cultivated (the soil). | Target repo / codebase | persistent |
| **Symbiotic Nodule** (Nodule) | An external *service* capability the plant partners with (GCP, GitHub API). | Microservice plugin / MCP gateway | persistent |
| **Rhizome** | The background code/AST index of the Substrate. | Vector DB / code index | persistent |
| **Taproot** | The deep, persistent store of deliberation: conversations, RFCs, decisions + rationale, reviews. | Decision record / institutional memory | persistent |
| **Stem** | The lean, deterministic Go orchestrator and vascular router. | Core orchestrator / backend | persistent |
| **Xylem** | Vascular channel carrying Transcripts (inputs) up to Sprouts. | Input pipe | per-run |
| **Phloem** | Vascular channel carrying diffs (outputs) down to the Substrate. | Output pipe | per-run |
| **Vascular Cambium** | Tissue coordinating parallel Xylem/Phloem channels. | Concurrent step runner | per-run |
| **Branch** | A capability group / security zone the Stem initialises at boot. | Capability group / security zone | persistent |
| **Sprout** | An ephemeral, strictly isolated worker that does one unit of work. | Worker container (*not* "agent") | ephemeral |
| **Terrarium** | The isolation boundary (filesystem + network) around a Sprout. | Sandbox / container | ephemeral |
| **Seed** | The originating goal / intent / use-case; the blueprint work grows from. | Product goal / use-case / intent | definition |
| **Fruit** | The deliverable a cycle yields (a PR / artifact). | Output / deliverable | per-cycle |
| **Genotype** | The base persona / system prompt (*who* a Sprout is). | System prompt / persona | definition |
| **Plasmid** | A modular, injectable block of context or tools (*a skill*). | RAG context / tool definition | definition |
| **Transcript** | One instruction for one growth (a single task). | User prompt / task | per-run |
| **Sequence** | An ordered YAML workflow of steps. | Agentic pipeline / workflow | definition |
| **Meristem** | A Sequence step that dynamically plans steps and translates human intent. | Planner / workflow conductor | per-run |
| **Phenotype** | Speculative parallel variants of one Transcript; first to pass survives. | Speculative parallel execution | per-run |
| **Hormonal Trigger** | A pre-execution security gate that can abort before a Sprout emerges. | Security gate / middleware | per-run |
| **Abscission** | The Stem dropping a withered Sprout to protect the organism. | Fault isolation / fault tolerance | event |
| **Epigenetics** | Distilled learnings appended after runs and fed forward into prompts. | Persistent memory / self-correction | persistent |
| **Resin** | Live internal logging trace. | Debug log / trace | transient |
| **Amber** | Hardened, archived, immutable logs. | Archived log storage | persistent |
| **Transporter** | Pub-sub emitter pushing metrics/events across the system boundary. | Telemetry / pub-sub adapter | persistent |
| **Tendril** | **Concept / brand only** — the organism's reaching-out-and-integrating character. Never a component. | *(the project itself)* | — |

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
| **emerge** | a Sprout is created | spawn |
| **grow** | a Sprout executes its Transcript | run / execute |
| **mature** | a Sprout completes successfully | complete |
| **wither** | a Sprout fails or crashes | fail |
| **abscise** | the Stem drops a withered Sprout | fault-isolate |
| **fruit** | deliver a PR / artifact | output / deliver |
| **nodulate** / *form symbiosis* | connect a Symbiotic Nodule | attach a plugin |
| **graft** | **reserved:** fuse two *like* tissues (Stem↔Stem mesh, or two Genotypes) | — |

---

## ⚠️ Corrected & Deprecated Usages

| Deprecated usage | Use instead | Why |
|---|---|---|
| "Tendril" for the ephemeral worker | **Sprout** | Tendril is now the concept/brand only; a worker is a Sprout. |
| "Tendril" for a persistent integration | **Substrate** (repo) or **Symbiotic Nodule** (service) | Two distinct real organs, not one overloaded word. |
| "graft" for attaching a Nodule | **nodulate** / *form symbiosis* | Grafting fuses two *plants*; a nodule is a symbiosis. |
| "graft" for merging changes home | **Phloem transport** / **merge** | Changes flow down the Phloem to the Substrate. |
| "run/execute" a task (internal) | **grow** | Botanical verb for a Sprout doing its work. |
| "Host System" | **Terroir** | The machine/OS as the local ground. |
| "OT" (abbreviation) | **OpenTendril** (written) / **Tendril** (short) | "OT" means *Operational Technology* in security/SDLC — never abbreviate the brand to it. |
| *agent / task / run* in architecture or code | botanical equivalents | These survive **only** at the external boundary (`AGENTS.md`, Meristem/intent layer). |

---

## 🤖 Interpretation by the Stem

The **Canonical Definitions** table above is the source for
`.tendril/genome/taxonomy-canonical.md`, a compact block the Stem injects as a
canonical **Plasmid** into LLM context so every Sprout speaks correct OpenTendril
and cannot drift back to IT jargon. When this glossary changes, regenerate that
block from it.
