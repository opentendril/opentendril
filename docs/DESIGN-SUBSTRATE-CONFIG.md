# Substrate Configuration YAML (Issue)

This plan details the design and implementation of the project-level `substrates.yaml` configuration file to resolve named workspaces, target branches, and secure auth credentials.

---

## 1. YAML Configuration Schema

A `substrates.yaml` file located in the project root or `.tendril/` directory will define named workspaces.

```yaml
substrates:
  core:
    path: /path/to/opentendril # Optional local directory path
    url: https://github.com/opentendril/opentendril.git
    branch: main
    auth: GITHUB_TOKEN       # References env var name
    readonly: false                          # Security lock to prevent commits/pushes
  my-api:
    url: https://github.com/myorg/api.git
    branch: develop
    auth: GITHUB_TOKEN
    readonly: true
```

### Key Schema Characteristics
*   **Local Paths:** If `path` is specified and exists on disk, it is used as the workspace. If missing/empty, Go Stem will clone the repo dynamically.
*   **Secrets Isolation:** The YAML stores **references to env var names** (e.g. `auth: GITHUB_TOKEN`), never raw tokens. Go Stem resolves the secret value at runtime via `os.Getenv(config.Auth)`. For GitHub PATs, `GITHUB_TOKEN` (canonical) and `GITHUB_PERSONAL_ACCESS_TOKEN` (legacy) are interchangeable — the Stem falls back to the other name when the referenced one is unset.
*   **Read-Only Locks:** If `readonly: true` is configured, Go Stem blocks all commit/push operations on the substrate, keeping the codebase sterile.

### Extended Credential Schema

`auth` accepts **either** a bare env-var name (`auth: GITHUB_TOKEN`, treated as `method: pat`) **or** a mapping, so a substrate can pick its authentication method, sign its commits, and control where it is checked out. Reusable `credentials:` profiles avoid repeating auth/sign per repo.

```yaml
credentials:
  work:
    auth: { method: pat, env: GITHUB_TOKEN_WORK }   # one token per environment
    sign: { method: gpg, key: ABCD1234 }

substrates:
  internal-lib:
    url: git@github.com:myorg/internal-lib.git
    auth:     { method: ssh, key: ~/.ssh/id_ot }     # pat | ssh | none
    sign:     { method: ssh, key: ~/.ssh/id_ot }     # ssh | gpg
    checkout: { mode: managed }                      # ephemeral | managed | path
  other:
    url: https://github.com/myorg/other.git
    profile: work                                    # reuse the profile above
```

*   **`auth.method`** — `pat` (env-var PAT), `ssh` (key-based, no PAT injected), `none` (anonymous), or `app` (GitHub App: the Stem mints short-lived installation tokens from `appId` + `privateKeyPath`/`privateKeyEnv`; `installationId` is optional and auto-discovered from the repo — recommended for enterprise, as it's a least-privilege bot identity with no long-lived secret).
*   **`sign`** — optional `ssh`/`gpg` commit signing; `key` is a key path or GPG key id.
*   **`checkout.mode`** — `ephemeral` (default, throwaway `/tmp` clone), `managed` (persistent Tendril-owned directory under `~/.tendril/substrates/<name>`, distinct from human-editable clones, refreshed to a clean tree on each run), or `path` (explicit `checkout.path`).
*   **`profile`** — references a named `credentials:` entry (inline values override the profile).

The scalar `auth: ENV_VAR` form is fully back-compatible. Secrets are never stored here — only references (env-var names, key paths, profile names) resolved at runtime.

---

## 2. Resolution & Gating Policies (User Feedback Incorporated)

### A. Read-Only Gating (Environmental Protections)
*   If a substrate is marked `readonly: true`:
    *   The Sprout terrarium **is allowed to modify files locally** (to enable compiler checks, test executions, and validation suites inside the container).
    *   Go Stem **strictly blocks** git stashing on the host, git commits in the terrarium, and fast-forward merging back to the host workspace. All edits are safely discarded when the terrarium is torn down.
    *   Go Stem passes the environment variable `TENDRIL_READONLY=true` to the Sprout container. The Sprout's internal executor can read this env var to warn the reasoning Sprout that its edits are ephemeral.

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

#### [NEW] [orchestrator/substrates_config.go](../cmd/stem/internal/orchestrator/substrates_config.go)
*   Define the Go structs `SubstratesConfig` and `SubstrateSpec`.
*   Implement `LoadSubstratesConfig(root string) (*SubstratesConfig, error)` searching for `./substrates.yaml`, `./.tendril/substrates.yaml`, and `repoRoot/substrates.yaml`.
*   Implement a validator that logs warnings if a named substrate has invalid config.

#### [MODIFY] [orchestrator/docker.go](../cmd/stem/internal/orchestrator/docker.go)
*   Update `RunTendril` to load the substrates configuration.
*   Resolve `d.Substrate` by checking if it matches a named configuration key.
*   If a named configuration matches, override:
    *   `Substrate` path $\rightarrow$ config `path` (if exists).
    *   `SubstrateURL` $\rightarrow$ config `url` (if not overridden by `SubstrateURL` field).
    *   `SubstrateBranch` $\rightarrow$ config `branch` (if not overridden by `SubstrateBranch` field).
    *   Dynamically set the auth environment variable during git clone (e.g. using the token referenced by the config's `auth` env var name).
*   **Read-Only Gating:** If `readonly: true` is resolved, skip git stashing before boot, and disable post-flight commits and fast-forward merging back to the host, outputting: `"⚠️ Substrate is configured as READONLY. Discarding terrarium modifications."`

#### [MODIFY] [internal/api/mcp.go](../cmd/stem/internal/api/mcp.go)
*   On startup, load and validate `substrates.yaml`. Log the active named substrates found to `log.Printf`.
*   Update `sproutTendril` execution flow to resolve the named `substrate` parameter correctly before initializing the orchestrator.

#### [NEW] [substrates.yaml.example](../substrates.yaml.example)
*   Add a documented configuration example to the repository.

#### [MODIFY] [QUICKSTART.md](../QUICKSTART.md)
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
