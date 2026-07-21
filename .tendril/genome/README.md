# 🧬 Tendril Genome (Context Seeds)

This directory forms the genetic blueprint of your OpenTendril Sprouts. 

Just as DNA dictates what a cell can do, the **Genome** dictates a Sprout's capabilities, coding standards, and architectural rules.

## How It Works

Before a Sprout emerges to execute a Transcript, the core orchestrator concatenates every Markdown (`.md`) file in this directory and automatically injects them into the Sprout's system prompt.

This allows you to permanently steer a Sprout's behaviour for this specific repository without needing to repeat instructions in every prompt.

## Usage

Simply drop Markdown files into this directory.

**Examples:**
- `naming-conventions.md` (e.g., "Always use kebab-case for filenames")
- `react-architecture.md` (e.g., "Always use Functional Components and React Hooks, never Class Components")
- `database-rules.md` (e.g., "Never use raw SQL, always use the Prisma ORM")

> **Note:** The orchestrator reads these files in alphabetical order. Keep instructions concise to save context window tokens!
