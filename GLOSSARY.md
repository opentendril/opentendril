# OpenTendril Glossary

OpenTendril uses biological and botanical metaphors rather than standard IT terminology to emphasize the chaotic, adaptable, and organic nature of LLM orchestration. 

This glossary provides a quick bidirectional reference between OpenTendril terminology and standard Software Engineering / AI terminology.

---

## 🌿 OpenTendril to Standard IT

| OpenTendril Term | Standard IT / AI Equivalent | Description |
|------------------|-----------------------------|-------------|
| **Amber** | Persistent Log Storage | Resin that has hardened over time (e.g., archived logs or local SQLite log dumps). |
| **Epigenetics** | Persistent Memory / Self-Correction State | Feedback and learnings appended back to the system after a task completes, ensuring future runs don't make the same mistakes. |
| **Genotype** | System Prompt / Base Persona | The core foundational prompt that dictates *who* the AI is and how it should behave (e.g. "You are a Python Engineer"). |
| **Hormonal Trigger** | Security Gate / Middleware Guard | Pre-execution checks or policies (like Bash scripts or OPA rules) that can instantly abort an unsafe request. |
| **Meristem** | Planner Node / Workflow Conductor | A node in a Sequence that dynamically plans or generates new sub-steps during execution. |
| **Mycorrhizal Network** | Large Language Model (LLM) | The external "brain" (e.g. Claude, GPT, Ollama) that does the actual reasoning and predicting. |
| **Phenotype** | Speculative Execution / Parallel Trial | Multiple parallel containers running the same prompt with slight variations to see which succeeds first. |
| **Plasmid** | RAG Context / Tool Definition | Modular, reusable blocks of context or API tool definitions injected into a Genotype on the fly. |
| **Resin** | Local Log Trace / Debug Log | The internal byproduct of the system's metabolism that flows through the system capturing errors and state. |
| **Rhizome** | Vector DB / Indexed Knowledge Graph | The local SQLite/Vector index of the Substrate that the AI queries for context. |
| **Sequence** | Agentic Pipeline / CI Workflow | A predefined YAML list of steps that orchestrates multiple Tendrils in a specific order. |
| **Sprout** | Docker Container / Ephemeral Process | The actual fresh, isolated process or Docker container spun up for a single execution run. |
| **Stem** | Core Orchestrator / Backend Server | The Go binary that serves the API, manages networking, and coordinates the entire framework. |
| **Substrate** | Host Repository / Target Codebase | The git repository or codebase that the Tendril is modifying or interacting with. |
| **Tendril** | Worker Runtime / AI Agent | The stateless script executor (e.g., Node or Python runner) operating inside the Sprout. |
| **Terrarium** | Sandbox / Container Environment | The isolated filesystem and network boundaries (usually within the Sprout) protecting the host machine. |
| **Transcript** | User Prompt / Single Task | The specific instruction or task fed to the Tendril for one execution loop. |
| **Transporter** | Telemetry Emitter / Pub-Sub Adapter | Molecular pumps (ABC transporters) that actively push metrics and events across the system boundary to external platforms (Kafka, Datadog). |

---

## 💻 Standard IT to OpenTendril

| Standard IT / AI Term | OpenTendril Equivalent | Context |
|-----------------------|------------------------|---------|
| **Agentic Pipeline / Workflow** | **Sequence** | Orchestrates multiple steps and models automatically. |
| **Archived Logs / Persistent Storage** | **Amber** | Hardened, immutable historical logs. |
| **Backend Server / Orchestrator** | **Stem** | The central Go process managing everything. |
| **Docker Container** | **Sprout** (or **Terrarium**) | The ephemeral execution sandbox. |
| **Large Language Model (LLM)** | **Mycorrhizal Network** | The remote brain doing the reasoning. |
| **Local Logs / Debug Traces** | **Resin** | Real-time debugging byproducts flowing through the system. |
| **Memory / Self-Correction** | **Epigenetics** | Learnings passed down to future agent runs. |
| **Planner / Conductor** | **Meristem** | A step that dynamically decides the next steps. |
| **RAG Context / Tool List** | **Plasmid** | Reusable snippets injected into the prompt. |
| **Repository / Target Codebase** | **Substrate** | The code the AI is modifying. |
| **Sandbox Environment** | **Terrarium** | The secured boundary wrapping the execution. |
| **Security Gate / Middleware** | **Hormonal Trigger** | Fast, pre-flight abort mechanisms. |
| **Speculative Execution** | **Phenotype** | Running multiple variations concurrently. |
| **System Prompt / Persona** | **Genotype** | The core identity of the worker. |
| **Task / User Prompt** | **Transcript** | The single instruction given to the AI. |
| **Telemetry / Pub-Sub Emitter** | **Transporter** | Molecular pumps sending events to external platforms. |
| **Vector DB / Knowledge Graph** | **Rhizome** | The indexed map of the codebase. |
| **Worker Script / Agent** | **Tendril** | The actual language runtime (Go/Python/Node) executing the commands. |
