# Genotype Progressive Disclosure: Metadata Indexing & On-Demand Plasmids (Issue #61)

This plan details the implementation of **Genotype Progressive Disclosure** to optimize LLM token usage. Instead of loading the full instructions of all genotypes and plasmids upfront, we will use a two-tier metadata loading pattern.

---

## 1. Architectural Design

```
                     [ IDE / Client Sprout Boot ]
                                  │
                       MCP resources/list call
                                  │
                                  ▼
                    ┌────────────────────────────┐
                    │  Go Stem loads index.yaml  │
                    │  Returns metadata only:    │
                    │  - Name: "frontend-dev"    │
                    │  - Desc: "You write React" │
                    └────────────────────────────┘
                                  │
                  LLM selects a specific genotype
                                  │
                                  ▼
                    ┌────────────────────────────┐
                    │  MCP resources/read call   │
                    │  Loads full JSON from:     │
                    │  genotypes/frontend-dev.json│
                    └────────────────────────────┘
                                  │
                         Sprout execution
                                  │
                                  ▼
                    ┌────────────────────────────┐
                    │  On-Demand Plasmid Sync    │
                    │  Injects declared plasmids │
                    │  into terrarium worktree     │
                    └────────────────────────────┘
```

---

## 2. Genotype Index YAML Schema

We will introduce a central metadata index at `.tendril/genotypes/index.yaml`:

```yaml
genotypes:
  - name: frontend-dev
    description: "Senior React/Frontend engineer. Handles UI components and CSS styling."
  - name: go-engineer
    description: "Senior Go developer. Handles CLI command routers and backend APIs."
```

If the index file is missing, Go Stem will automatically generate it on startup by scanning the `.json` files in the genotypes directory and using a truncated preview of the `instructions` field as the default description.

---

## 3. Plasmid Injection On-Demand

Currently, the orchestrator loads all Markdown files in `.tendril/genome/` as the active genome prompt context.

To implement progressive disclosure for Plasmids:
1.  Genotype JSON files can declare a list of required plasmids:
    ```json
    {
      "name": "frontend-dev",
      "instructions": "You write React code.",
      "plasmids": ["react-conventions", "tailwind-styling"]
    }
    ```
2.  When a sprout runs with a specific genotype, the Go Stem will **dynamically inject** the declared plasmids (cloning/linking them from `.tendril/genotypes/plasmids/` into the terrarium's `.tendril/genome/` folder) *prior* to starting the execution session.
3.  This ensures the Sprout is loaded with the necessary skills, but the developer's main host repository `.tendril/genome/` remains completely clean of task-specific plasmid files.

---

## 4. Proposed Changes

### Component: Go Stem Orchestrator

#### [MODIFY] [internal/api/mcp.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/internal/api/mcp.go)
*   **`resources/list`:**
    *   Load `.tendril/genotypes/index.yaml` (automatically generate/sync it from `.json` files if missing or out of date).
    *   Return a list of resource objects populating `uri` (`genotype://<name>`), `name`, and `description`.
*   **`resources/read`:**
    *   Read the full JSON file `.tendril/genotypes/<name>.json` containing instructions and plasmids, returning it as the resource text.

#### [MODIFY] [internal/orchestrator/docker.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/internal/orchestrator/docker.go)
*   Parse the Genotype JSON file during sprouting.
*   If the Genotype contains a `plasmids` array, call the plasmid injection helper to copy those files from `.tendril/genotypes/plasmids/` into the terrarium worktree's `.tendril/genome/` directory before starting the Sprout container.

---

## 5. Open Questions

> [!IMPORTANT]
> 1.  **Auto-Generating Descriptions:** If `index.yaml` is missing, should we auto-truncate the genotype's `instructions` field to 20 words for the description, or leave the description empty and log a warning prompting the developer to write one?
> 2.  **Updating Index in Config Uploads:** When a user dynamically uploads a new genotype via the REST/config API (`/v1/config/genotypes`), should Go Stem automatically append the new genotype details to `index.yaml`?

---

## 6. Verification Plan

### Automated Tests
*   **Index Parser Tests:** Verify YAML parsing, serialization, and auto-fallback generation.
*   **MCP resource read/list Tests:** Verify that `resources/list` returns only metadata descriptions, and `resources/read` fetches full JSON.

### Manual Verification
1.  Connect Claude Desktop or VS Code MCP to OpenTendril.
2.  Verify that only the genotype names and short descriptions are listed as available resources initially.
3.  Run a sprout task with `frontend-dev` and verify that the required plasmids are injected dynamically only inside the terrarium.
