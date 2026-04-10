# The Genesis of Tendril: Project History

This document chronicles the "why" and "how" behind the birth of The Root Agent. It is a record of the strategic pivots made during the first 48 hours of development, moving from a private experiment to a public-facing engine.

---

### Day 1: The Fragmentation Problem (2026-04-08)

Project Tendril began as an attempt to solve the "Agentic Fragility" problem. Existing tools like AutoGPT or early OpenClaw forks were powerful but fragile—they broke, they hallucinated, and they couldn't fix their own environment.

**The Strategic Pivot:** We decided that Tendril shouldn't just be an "executor." It should be a **Root Agent**—the system that builds and repairs the other agents.

**Key Milestones:**
- **The /edit Endpoint:** The first "primitive" allows the agent to modify its own source code via volume-mounted files.
- **The Chronicler:** Realizing that our AI conversations were getting scattered across threads, we built the `chronicler.py` to allow the agent to log its own progress in `PROGRESS.md`.
- **The Unified Credit System:** We established a "Local-First" architecture that allows developers to bring their own keys, but simplifies the scaling path through a unified billing mode for the cloud.

---

### Day 2: The Branding & Scaling Pivot (2026-04-09)

As the core stabilized, we looked at the competition (OpenClaw). While OpenClaw grew to 250k stars, it remained a "Tool-First" project. Tendril needed to be an **Infrastructure-First** project.

**The Strategic Decisions:**
- **The MIT Commitment:** We discarded more restrictive licenses in favor of pure MIT. Our moat is not the code—it is the **Velocity** and the **Managed SaaS Experience.**
- **The Go Gateway:** We realized that Python sockets couldn't handle the concurrency required for a global agent fleet. We began extracting the chat transport into a Go WebSocket gateway (`gateway/`).
- **The Security Hardening:** After reviewing OpenClaw's security model, we implemented a zero-trust HTTP Relay for code execution, ensuring that the AI can test its own code without ever touching the host system's database or environment.

---

---

### Day 3: The Honest Assessment & v0.1.0 (2026-04-10)

As the kernel hit the 72-hour mark, we took a brutal look at the state of the system. We had built significant infrastructure for "Evolution 2" (multi-agent orchestration, credit systems, marketing bots), but we hadn't proven the core: **Can Tendril reliably edit code on someone else's project?**

**The Tactical Pivot:** We hit the brakes on all secondary features. We stripped away the complexity and focused on one thing: **The Stable Kernel.**

**Milestones of the v0.1.0 Sprint:**
- **External Project Mode:** We verified that Tendril can be pointed at complex, 1000+ file repos (like Jurnx) and successfully understand/edit them.
- **The Go-First Pipeline:** We solidified the Go CLI and Gateway as the primary interaction layer, providing a low-latency, "pro-tool" feel.
- **Proof of Life:** On Day 1 of the sprint, Tendril successfully diagnosed and committed a change to an external Go/React codebase.

**The State of the Union:**
Tendril v0.1.0 is not a finished product; it is a **working engine.** It proves that a self-hosted agent can be trusted to operate on a project with its own rules, its own tech stack, and its own LLM keys.

---

### The Vision: The Agent that Builds Agents

Tendril is being built in public. Every major strategic decision—from the choice of PostgreSQL for vector memory to the shift back to the Stable Kernel—is documented here.

We are currently in the **"Stable Core"** phase. We aren't just building a product; we are building the Root Agent that will eventually build the next 100 products.

**Join the journey.**
- [View the v0.1.0 Release](https://github.com/opentendril/core/releases)
- [Watch the Progress](PROGRESS.md)
- [Check the Genesis](HISTORY.md)
