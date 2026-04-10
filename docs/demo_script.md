# Tendril v0.1.0 Demo Script

This script is designed for a **2-minute terminal recording** (e.g., via `asciinema`) to show the world that the Tendril Kernel actually works on external codebases.

## Prerequisites
- Tendril running in Docker (`docker compose up`)
- Env vars set up to point to an external project (e.g., `/home/dr3w/GitHub/jurnx`)
- `tendril-cli` built (`make cli`)

---

## The Scene (0:00 - 0:30)
**Goal:** Show the setup and the "surprising" file listing.

1. **Start recording.**
2. `ls -F` (Show you are in the `opentendril/core` directory).
3. `cat .env | grep TENDRIL_PROJECT_PATH` (Show it's pointing to `jurnx`).
4. `./cli/tendril-cli` (Launch the client).

## The Request (0:30 - 1:00)
**Goal:** Ask for something that only a project-aware agent can do.

5. **Type:** `list all files in the project`
   - *Observation:* Tendril returns the `jurnx` file tree (1,300+ files), not its own source. This proves it successfully mounted and surveyed the external workspace.
6. **Type:** `find where medical records are ingested in the Go API`
   - *Observation:* Tendril searches and identifies the correct handler or service (e.g., `services/med-api-go/internal/app/ingestion/`).

## The Action (1:00 - 1:45)
**Goal:** Make a real, surgical edit.

7. **Type:** `read services/med-api-go/README.md and summarize the API routes`
   - *Observation:* Tendril reads the file and provides a concise summary.
8. **Type:** `add a comment to the top of that file saying "API verified by Tendril v0.1.0"`
   - *Observation:* Tendril generates a diff, applies it, and shows you the result.

## The Proof (1:45 - 2:00)
**Goal:** Commit and verify.

9. **Type:** `commit the change with message "tendril: Day 1 proof of life"`
   - *Observation:* Tendril interacts with git and returns the commit hash.
10. **Exit CLI** (`Ctrl+C`).
11. `cd /home/dr3w/GitHub/jurnx && git log -1`
    - *Observation:* Proof of commit.
12. **End recording.**

---

## Pro-Tips for Recording
- Use a high-contrast terminal theme (e.g., Catppuccin or Dracula).
- Clear the terminal before starting (`clear`).
- If using `asciinema`, you can edit the `.cast` file later to speed up long LLM response wait times.
