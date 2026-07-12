# OpenTendril Canonical Taxonomy (Stem-injected)

You are operating inside **OpenTendril**, an organism modelled on a plant. Use
exactly the vocabulary below — each term has ONE meaning — for all internal
architecture and code. (Standard IT phrasing belongs only at the external
boundary: `AGENTS.md` and the Meristem / intent-translation layer.) This block is
the authority; fuller docs are `SYNTHETIC-TAXONOMY.md` (concept) and
`GLOSSARY.md` (reference).

## Components (nouns)
- **Stem**: the Go orchestrator/router. Not a brain; no business logic.
- **Roots**: the LLM provider connectivity layer (`roots/`).
- **Mycorrhizal Network**: the LLM (external mind; reasons; holds no state).
- **Rhizosphere**: the existing tech Tendril is based on (providers, Docker, git, MCP).
- **Substrate**: the repository being cultivated (the soil).
- **Symbiotic Nodule** (Nodule): an external *service* capability (GCP, GitHub API).
- **Rhizome**: the code/AST index of the Substrate.
- **Taproot**: the persistent store of deliberation (conversations, RFCs, decisions+rationale, reviews).
- **Sprout**: an ephemeral, isolated worker that does one unit of work.
- **Terrarium**: the isolation boundary (filesystem+network) around a Sprout.
- **Branch**: a capability group / security zone.
- **Seed**: the originating goal / intent / use-case.
- **Fruit**: the deliverable a cycle yields (a PR / artifact).
- **Botanist**: the human who studies and cultivates (plants Seeds, prunes, reviews); observes via the Greenhouse.
- **Greenhouse**: the observer UI.
- **Terroir**: the host OS / machine.
- **Biome**: the normative climate (ISO 27001, ISO 42001, SDLC).
- **Genotype** (persona) · **Plasmid** (skill/context) · **Transcript** (one task) · **Sequence** (workflow) · **Meristem** (planner step) · **Phenotype** (parallel variant).
- **Hormonal Trigger** (security gate) · **Abscission** (fault isolation) · **Epigenetics** (fed-forward learnings) · **Resin**→**Amber** (live→archived logs) · **Transporter** (telemetry).

## Verbs
germinate (boot) · branch · emerge (create a Sprout) · **grow** (execute) · mature (succeed) · wither (fail) · abscise (drop a withered Sprout) · fruit (deliver) · nodulate / form symbiosis (connect a Nodule) · merge (return changes home, via the Phloem).
**graft** = fuse two *like* tissues: Stem↔Stem (mesh) or two Genotypes.

## Rules
1. **Tendril** names the whole organism and brand. Name the ephemeral worker a **Sprout**; name a persistent integration a **Substrate** (a repo) or a **Symbiotic Nodule** (a service).
2. A **Sprout** is sealed inside its **Terrarium**; it does not reach the network. External access is **Stem-mediated**: the **Mycorrhizae** decides intent → the **Stem** invokes the Nodule/Substrate → the Sprout consumes the result (Xylem in, Phloem out).
3. For execution say **grow**; for a worker say **Sprout**; for one instruction say **Transcript**; for a workflow say **Sequence**.
4. Write the brand as **OpenTendril**; say **Tendril** for short.
