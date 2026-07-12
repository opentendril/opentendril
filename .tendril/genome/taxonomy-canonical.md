# OpenTendril Canonical Taxonomy (Stem-injected)

You are operating inside **OpenTendril**, an organism modelled on a plant. Use
these terms exactly — each has ONE meaning. Never substitute IT jargon (agent,
task, run, sandbox, tool) inside the system. This block is the authority; the full
docs are `SYNTHETIC-TAXONOMY.md` (concept) and `GLOSSARY.md` (reference).

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
germinate (boot) · branch · emerge (create a Sprout) · **grow** (execute; NOT "run") · mature (succeed) · wither (fail) · abscise (drop a withered Sprout) · fruit (deliver) · nodulate / form symbiosis (connect a Nodule).
**graft** is RESERVED for fusing two *like* tissues: Stem↔Stem (mesh) or two Genotypes. Never for attaching a Nodule or merging changes.

## Hard rules
1. **"Tendril" is the concept/brand only — never a component.** A worker is a **Sprout**. A persistent integration is a **Substrate** (repo) or **Symbiotic Nodule** (service).
2. A **Sprout** is sealed in its **Terrarium** and cannot reach out. External calls are **Stem-mediated**: the Mycorrhizae decides intent → the **Stem** invokes the Nodule/Substrate → the Sprout consumes the result (Xylem in, Phloem out).
3. Use **grow** for execution, not "run". Use **Sprout** for a worker, not "agent". Use **Transcript** for a task.
4. *agent / task / run* are permitted ONLY at the external boundary (`AGENTS.md`, Meristem/intent translation) — never in internal architecture or code.
5. The brand is **OpenTendril** (written) / **Tendril** (short). **Never abbreviate to "OT"** — that means Operational Technology in this domain.
