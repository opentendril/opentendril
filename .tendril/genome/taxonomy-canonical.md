# OpenTendril Canonical Taxonomy (Stem-injected)

You are operating inside **OpenTendril**, an organism modelled on a plant. Use
exactly the vocabulary below — each term has ONE meaning — for all internal
architecture and code. (Standard IT phrasing belongs only at the external
boundary: `AGENTS.md` and the Meristem / intent-translation layer.) This block is
the authority; fuller docs are `SYNTHETIC-TAXONOMY.md` (concept) and
`GLOSSARY.md` (reference).

## Components (nouns, A–Z)
- **Abscission**: the Stem dropping a withered Sprout to protect the organism.
- **Amber**: hardened, archived, immutable logs.
- **Biome**: the normative climate (ISO 27001, ISO 42001, SDLC).
- **Botanist**: the human who studies and cultivates (plants Seeds, prunes, reviews); observes via the Greenhouse.
- **Branch**: a capability group / security zone.
- **Epigenetics**: distilled learnings fed forward into future prompts.
- **Fruit**: the deliverable a cycle yields (a PR / artifact).
- **Genotype**: the base persona / system prompt (*who* a Sprout is).
- **Greenhouse**: the observer UI.
- **Hormonal Trigger**: a pre-execution security gate.
- **Meristem**: a planner step; the shoot apical meristem — it initiates Phytomers.
- **Mycorrhizal Network**: the LLM (external mind; reasons; holds no state).
- **Phenotype**: speculative parallel variants; the first to pass survives.
- **Phloem**: vascular channel carrying diffs (outputs) down to the Substrate.
- **Phytomer**: a session — one persistent interaction thread; the Meristem initiates it and Sprouts emerge from it. (An **axillary bud** on it buds into a new Phytomer chain: a branch, parallel line, or recursion.)
- **Plasmid**: a modular, injectable block of context or tools (*a skill*).
- **Pollen**: the identity a Pollinator presents and a grant names — the trust-root a delegated invocation is authorised as. Never the actor itself: Pollen is *what is presented*, a Pollinator is *who presents it*.
- **Pollinator**: an *external* requester that reaches IN and asks for governed work — a Botanist, or an external Mycorrhizal mind over a governed interface. Identified by a **Pollen**; granted operation-classes; no grant, no access. The mirror of a Symbiotic Nodule (which the organism reaches OUT to). Never call it an "agent": it is neither a Sprout (sealed internal worker) nor the Mycorrhizal Network itself.
- **Ramet**: one running Tendril instance — a single individual on one Terroir. Many Ramets of the same Genome form a **Genet**. Never call an instance an "agent".
- **Resin**: live internal log trace (hardens into Amber).
- **Rhizome**: the code/AST index of the Substrate.
- **Rhizosphere**: the *external* ecosystem the Roots tap — the providers, Docker, git, MCP themselves (the outside world).
- **Roots** (`roots/`): OpenTendril's *own* connectivity organ — provider clients, model discovery, tier routing that reach into the Rhizosphere to draw inference from the Mycorrhizae.
- **Genet**: all Ramets grown from the same Genome — the genetic individual they are copies of.
- **Seed**: the originating goal / intent / use-case.
- **Sequence**: an ordered YAML workflow of steps.
- **Sprout**: an ephemeral, isolated worker that does one unit of work.
- **Stem**: the Go orchestrator/router. Not a brain; no business logic.
- **Stoma**: the single controlled aperture in a Terrarium wall — the guest-side listener through which commands enter and results leave. A Terrarium has exactly one.
- **Substrate**: the repository being cultivated (the soil).
- **Symbiotic Nodule** (Nodule): an external *service* capability (GCP, GitHub API).
- **Taproot**: the persistent store of deliberation (conversations, RFCs, decisions+rationale, reviews).
- **Terrarium**: the isolation boundary (filesystem+network) around a Sprout.
- **Terroir**: the host OS / machine.
- **Transcript**: one instruction for one growth (a single task).
- **Transporter**: pub-sub telemetry emitter.
- **Xylem**: vascular channel carrying Transcripts (inputs) up to Sprouts.

## Verbs (A–Z)
abscise (drop a withered Sprout) · branch · bud (activate an axillary bud into a new Phytomer) · emerge (create a Sprout) · fruit (deliver) · germinate (boot) · **graft** (fuse two *like* tissues: Stem↔Stem mesh, or two Genotypes) · **grow** (execute) · mature (succeed) · merge (return changes home, via the Phloem) · nodulate / form symbiosis (connect a Nodule) · wither (fail).

## Rules
1. **Tendril** names the whole organism and brand. Name the ephemeral worker a **Sprout**; name a persistent integration a **Substrate** (a repo) or a **Symbiotic Nodule** (a service).
2. A **Sprout** is sealed inside its **Terrarium**; it does not reach the network. External access is **Stem-mediated**: the **Mycorrhizae** decides intent → the **Stem** invokes the Nodule/Substrate → the Sprout consumes the result (Xylem in, Phloem out).
3. For execution say **grow**; for a worker say **Sprout**; for one instruction say **Transcript**; for a workflow say **Sequence**.
4. Write the brand as **OpenTendril**; say **Tendril** for short.
