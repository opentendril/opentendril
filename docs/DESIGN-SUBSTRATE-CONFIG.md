# Substrate Configuration YAML (Issue #56)

This plan details the design and implementation of the project-level `substrates.yaml` configuration file to resolve named workspaces, target branches, and secure auth credentials.

---

## 1. YAML Configuration Schema

A `substrates.yaml` file located in the project root or `.tendril/` directory will define named workspaces.

```yaml
substrates:
  core:
    path: /home/dr3w/GitHub/opentendril/core # Optional local directory path
    url: https://github.com/opentendril/core.git
    branch: main
    auth: GITHUB_PERSONAL_ACCESS_TOKEN       # References env var name
    readonly: false                          # Security lock to prevent commits/pushes
  my-api:
    url: https://github.com/myorg/api.git
    branch: develop
    auth: GITHUB_PERSONAL_ACCESS_TOKEN
    readonly: true
```

### Key Schema Characteristics
*   **Local Paths:** If `path` is specified and exists on disk, it is used as the workspace. If missing/empty, Go Stem will clone the repo dynamically.
*   **Secrets Isolation:** The YAML stores **references to env var names** (e.g. `auth: GITHUB_PERSONAL_ACCESS_TOKEN`), never raw tokens. Go Stem resolves the secret value at runtime via `os.Getenv(config.Auth)`.
*   **Read-Only Locks:** If `readonly: true` is configured, Go Stem blocks all commit/push operations on the substrate, keeping the codebase sterile.

---

## 2. Resolution & Gating Policies (User Feedback Incorporated)

### A. Read-Only Gating (Environmental Protections)
*   If a substrate is marked `readonly: true`:
    *   The Sprout sandbox **is allowed to modify files locally** (to enable compiler checks, test executions, and validation suites inside the container).
    *   Go Stem **strictly blocks** git stashing on the host, git commits in the sandbox, and fast-forward merging back to the host workspace. All edits are safely discarded when the sandbox is torn down.
    *   Go Stem passes the environment variable `TENDRIL_READONLY=true` to the Sprout container. The Sprout's internal executor can read this env var to warn the reasoning agent that its edits are ephemeral.

### B. On-Demand Remote Clones
*   If a named substrate declares a `url` but no local `path` exists:
    *   Go Stem treats this as a **remote-only substrate**.
    *   It clones the repository to a temporary directory (`/tmp/opentendril-substrate-<name>-<random>`) for the duration of the execution.
    *   It executes the task inside this temporary workspace.
    *   Upon completion (if not read-only), Go Stem commits the changes, pushes the branch to the remote origin Git repository (satisfying the State-in-Git principle), and cleans up the temporary directory.
    *   If the developer wants persistent local files, they must specify the `path` key in `substrates.yaml`.

---

## 3. Proposed Changes

### Component: Go Stem Orchestrator

#### [NEW] [orchestrator/substrates_config.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/internal/orchestrator/substrates_config.go)
*   Define the Go structs `SubstratesConfig` and `SubstrateSpec`.
*   Implement `LoadSubstratesConfig(root string) (*SubstratesConfig, error)` searching for `./substrates.yaml`, `./.tendril/substrates.yaml`, and `repoRoot/substrates.yaml`.
*   Implement a validator that logs warnings if a named substrate has invalid config.

#### [MODIFY] [orchestrator/docker.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/internal/orchestrator/docker.go)
*   Update `RunTendril` to load the substrates configuration.
*   Resolve `d.Substrate` by checking if it matches a named configuration key.
*   If a named configuration matches, override:
    *   `Substrate` path $\rightarrow$ config `path` (if exists).
    *   `SubstrateURL` $\rightarrow$ config `url` (if not overridden by `SubstrateURL` field).
    *   `SubstrateBranch` $\rightarrow$ config `branch` (if not overridden by `SubstrateBranch` field).
    *   Dynamically set the auth environment variable during git clone (e.g. using the token referenced by the config's `auth` env var name).
*   **Read-Only Gating:** If `readonly: true` is resolved, skip git stashing before boot, and disable post-flight commits and fast-forward merging back to the host, outputting: `"⚠️ Substrate is configured as READONLY. Discarding sandbox modifications."`

#### [MODIFY] [internal/api/mcp.go](file:///home/dr3w/GitHub/opentendril/core/cmd/stem/internal/api/mcp.go)
*   On startup, load and validate `substrates.yaml`. Log the active named substrates found to `log.Printf`.
*   Update `sproutTendril` execution flow to resolve the named `substrate` parameter correctly before initializing the orchestrator.

#### [NEW] [substrates.yaml.example](file:///home/dr3w/GitHub/opentendril/core/substrates.yaml.example)
*   Add a documented configuration example to the repository.

#### [MODIFY] [QUICKSTART.md](file:///home/dr3w/GitHub/opentendril/core/QUICKSTART.md)
*   Add a documentation section outlining how developers can configure `substrates.yaml` for multi-repository codebases.

---

## 4. Verification Plan

### Automated Tests
*   **YAML Config Parser Tests:** Verify YAML parsing, env-var resolution, and override precedence.
*   **Read-Only Gating Tests:** Assert that a mock run on a read-only substrate does not commit/merge changes back to the mock host repository.

### Manual Verification
1.  Create a `substrates.yaml` with a read-only named substrate.
2.  Run `tendril chat` pointing to the read-only substrate, and request it to create a file.
3.  Verify that sprout completes but the file is **not** committed or written to your host workspace.
