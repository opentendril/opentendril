# Intent Translation & Use-Case Guide

Because OpenTendril enforces a strict [Synthetic Biological Taxonomy](SYNTHETIC-TAXONOMY.md), it does not use standard industry jargon (like "Agents", "Workflows", or "Tools"). 

However, users and external AI systems will inevitably send standard, messy requests like: *"Load the web scraper tool and run the agent."* 

To handle this, OpenTendril acts as an **active interpreter**. It uses a small, fast LLM step—called the **Meristem**—to intercept user intents and dynamically translate them into strict biological JSON for the Go Stem to execute.

This guide explains how the Meristem interprets common industry requests into OpenTendril architecture.

---

## The Meristem Interpreter Pattern

1. **User Request**: The user sends a standard IT/AI command.
2. **Meristem Intercept**: The Meristem (Planner Node LLM) reads the prompt. It consults the Glossary and the directory of available capabilities.
3. **Intent Translation**: The Meristem maps the standard jargon ("Tool", "Context", "Workflow") to the biological equivalent ("Tendril", "Plasmid", "Sequence").
4. **Biological Execution**: The Meristem outputs a fully structured JSON `Transcript` back to the Go Stem, which blindly executes the biological command (e.g., grafting a Tendril or growing a Sprout).

---

## Common Use Cases & Translations

### Use Case A: Requesting a "Tool" or "API"
When a user wants the system to interact with an external service (like fetching a GitHub issue or running a database query).

*   **User Intent**: *"Load the GitHub skill and pull issue #123."*
*   **Meristem Translation**: The Meristem recognizes that acting on an external API requires a **Tendril** (a Microservice Plugin / MCP Gateway).
*   **Biological Execution**: The Stem is instructed to **Graft** the `github-tendril` onto the current Branch, and the Sprout is given the Transcript to fetch issue #123 using that Tendril.

### Use Case B: Requesting "Context", "Knowledge", or "Skills"
When a user wants to provide the AI with specific documentation, rules, or text patterns without executing external code.

*   **User Intent**: *"Use the React best practices skill to write this component."*
*   **Meristem Translation**: The Meristem recognizes that providing static knowledge or prompt instructions requires a **Plasmid** (a modular context block).
*   **Biological Execution**: The Stem injects the `react-guidelines.md` Plasmid into the core **Genotype** before growing the Sprout.

### Use Case C: Requesting an "Agent Loop" or "Workflow"
When a user wants the system to perform a multi-step automated process (like writing code, testing it, and self-correcting).

*   **User Intent**: *"Run the coding agent loop on this file."*
*   **Meristem Translation**: The Meristem recognizes that a multi-step orchestrated workflow requires a **Sequence** (a predefined YAML DAG of steps).
*   **Biological Execution**: The Stem activates the requested Sequence, coordinating the sequential growth and abscission (destruction) of multiple Sprouts (e.g., a Worker Sprout followed by a Verifier Sprout).

### Use Case D: Requesting a "Sandbox" or "Isolated Environment"
When a user wants to ensure the execution doesn't harm their local machine.

*   **User Intent**: *"Run this python script in a secure sandbox."*
*   **Meristem Translation**: The Meristem recognizes that execution isolation requires a **Terrarium** (an ephemeral Docker container).
*   **Biological Execution**: The Stem grows a Sprout entirely inside a Python **Terrarium**, executing the transcript securely before destroying the container.

---

## Why this Translation Matters
By forcing all external requests through the Meristem interpreter, OpenTendril achieves two things:
1. **Frictionless UX**: Users and external AI tools can talk to OpenTendril using the terminology they already know.
2. **Taxonomic Purity**: The core engine (the Stem) and the persistent architecture never get polluted by disorganized standard IT terminology. The biological metaphor remains pristine.
