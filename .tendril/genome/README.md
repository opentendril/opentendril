# 🧬 Tendril Genome (Context Seeds)

This directory forms the genetic blueprint of your OpenTendril agents. 

Just as DNA dictates what a cell can do, the **Genome** dictates the autonomous agent's capabilities, coding standards, and architectural rules.

## How It Works

Before an agent (`Tendril`) sprouts to execute a task, the core orchestrator concatenates every Markdown (`.md`) file in this directory and automatically injects them into the agent's system prompt.

This allows you to permanently steer the agent's behavior for this specific repository without needing to repeat instructions in every prompt.

## Usage

Simply drop Markdown files into this directory.

**Examples:**
- `naming-conventions.md` (e.g., "Always use kebab-case for filenames")
- `react-architecture.md` (e.g., "Always use Functional Components and React Hooks, never Class Components")
- `database-rules.md` (e.g., "Never use raw SQL, always use the Prisma ORM")

> **Note:** The orchestrator reads these files in alphabetical order. Keep instructions concise to save context window tokens!
