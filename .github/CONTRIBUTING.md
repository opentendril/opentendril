# Contributing to OpenTendril Core

Thank you for your interest in contributing to OpenTendril! We are building a secure, zero-friction, terrariumed execution kernel for Pollinators, and we welcome contributions from human developers.

---

## 🚀 Quick Local Setup

OpenTendril is a Go Stem orchestrator with optional containerized UI and test services.

### Prerequisites:
* **Go:** Version 1.23 or newer.
* **Python:** Version 3.11 or newer.
* **Docker:** Required only for the optional UI and containerized test targets.
* **Node.js:** Version 18 or newer — only needed to work on the Command Center UI (`ui/`).

### Installation Steps:
1. **Fork and Clone:**
   ```bash
   git clone https://github.com/your-username/opentendril.git
   cd opentendril
   ```
2. **Build the CLI:**
   ```bash
   make stem      # builds cmd/stem/tendril (use 'make install' to put it on your PATH)
   ```
3. **Set Up Python Virtualenv:**
   ```bash
   python3 -m venv venv
   source venv/bin/activate
   pip install -r sprouts/python/requirements.txt
   ```
4. **Verify Your Environment:**
   Run the full lint and test suites to verify your local configuration:
   ```bash
   make check-all
   ```

To start the local orchestrator during development, run `make up`. This launches
`go run ./cmd/stem serve` directly on the Stem API port at `http://localhost:8080`;
it does not start a full Docker Compose stack. The optional UI can be started with
`docker compose --profile ui up`.

### Command Center UI (optional):
If you are working on the visual frontend, install its dependencies and run the
dev server (which proxies to a running `tendril serve`):
```bash
cd ui
npm install
STEM_TARGET=http://localhost:8080 npm run dev   # http://localhost:5173
npm run build                                   # type-check + static build to ui/dist/
```
See [`ui/README.md`](../ui/README.md) for the full UI guide.

---

## ✍️ Signed Commits (do this before your first push)

**Every commit pushed to this repository must be signed.** The default branch is
protected by a ruleset with a `required_signatures` rule, so an unsigned commit
is rejected at push time — not at review, not by a linter. If you have never set
signing up, your first push will fail, and the error does not explain how to fix
it. Five minutes now saves that.

Signing proves a commit came from the key you control. In a repository where
Pollinators also commit, it is what keeps "who wrote this" answerable.

### The simplest route: SSH signing

If you already push over SSH you have a key, and git can sign with it:

```bash
git config --global gpg.format ssh
git config --global user.signingkey ~/.ssh/id_ed25519.pub
git config --global commit.gpgsign true
```

> [!IMPORTANT]
> **Upload the key to GitHub a second time, as a signing key.** Settings → SSH
> and GPG keys → New SSH key → **Key type: Signing Key**. An authentication key
> is *not* automatically a signing key, and this is the step almost everyone
> misses: pushes succeed, but every commit shows **Unverified** and the ruleset
> rejects it. The same public key file can be uploaded for both purposes.

### Or GPG

```bash
gpg --quick-generate-key "Your Name <you@example.com>" ed25519 sign never
gpg --list-secret-keys --keyid-format LONG    # note the key id
git config --global user.signingkey <KEY_ID>
git config --global commit.gpgsign true
gpg --armor --export <KEY_ID>                 # paste into GitHub → New GPG key
```

Use the same email address on the key as on your commits, or GitHub will not
associate the two.

### Check it before you need it

```bash
git commit --allow-empty -m "signing check"
git log --show-signature -1
```

You want to see a good signature. Drop the empty commit afterwards with
`git reset --hard HEAD~1`.

### If you already have unsigned commits on a branch

Signing applies from now on; it does not retrofit. Re-sign the commits you have
already made:

```bash
git rebase --exec 'git commit --amend --no-edit -S' main
```

---

## 🎨 Coding and Naming Standards

To prevent visual noise and scanning friction, we enforce strict language casing boundaries across the filesystem and code. Before writing code, review [GUARDRAILS.md](../GUARDRAILS.md).

### Crucial File Naming Rule:
* **No underscores or hyphens in code filenames.** All Python modules and Go source files must use **merged lowercase** only (e.g., `llmrouter.py`, `mcpserver.go`).
* **Exceptions:** Go test files must end with `_test.go` (e.g., `mcp_test.go`) as required by the Go toolchain. Platform-required canonical files, such as GitHub's `CODE_OF_CONDUCT.md`, may also use underscores when their exact names are required for automatic discovery. No general underscore naming is allowed on the filesystem.
* **Non-code files** (like `docker-compose.yml`) use `kebab-case`.
* **The `ui/` frontend follows React/TypeScript convention instead**, scoped to that tree: `PascalCase.tsx` for components (`GardenCanvas.tsx`), `camelCase.ts` for modules (`garden.ts`), and `PascalCase/` folders for component families (`Garden/`). Do **not** rename these to merged-lowercase — it is a deliberate, tooling-idiomatic exception. JSON payload keys stay `camelCase` and API paths stay `kebab-case` as everywhere else.

### Code Casing Rules:
* **Python Code:** Use `snake_case` for variables and methods, and `PascalCase` for classes. Python is the *only* language where `snake_case` is allowed.
* **Go Code:** Use `camelCase` for local variables and `PascalCase` for exported names. **Do not use `snake_case` in Go.**
* **JSON Properties:** Must use `camelCase` only.
* **API Endpoints:** Must use `kebab-case` (e.g., `/api/mcp-tools`).

---

## 🔀 Workflow & Pull Requests

OpenTendril is an **AI-SDLC** workspace, meaning you will see issues and draft Pull Requests generated automatically by Pollinators (like Antigravity). 

As a human contributor, your workflow is simple and respects the same quality gates:
1. **Sign your commits.** See the section above. This is not optional — the push is rejected otherwise.
2. **Create a Branch:** Create a branch from `main` prefixed with `feat/` or `fix/` (e.g., `feat/add-github-provider`).
3. **Implement Changes:** Stay focused on a single issue/purpose. Keep diffs minimal.
4. **Verify Locally:** Before pushing, ensure all tests and linters pass:
   ```bash
   make check-all
   ```
5. **Submit a PR:** Push your branch and open a Pull Request into `main`.
6. **Review:** A maintainer will review your code. Once approved and the required checks pass, it will be merged.

### What the default branch enforces

These are ruleset rules, not conventions — they fail your push or block your merge rather than earning a review comment:

* **Signed commits**, on every branch.
* **Linear history** — no merge commits into `main`; rebase rather than merge when updating your branch.
* **No force-pushes and no deletion** of the default branch.
* **A pull request is required** — you cannot push straight to `main`.
* **Review threads must be resolved** before merging.
* **Required status checks** must pass: `Native PR Gate` (which aggregates the Go and Python suites according to what your change touched), `verify-commits`, and the six Source Hygiene checks — GitHub references, default-branch assumptions, delegated isolation, branch deletion, protected-path ownership, and taxonomy.

`make check-all` covers the language suites locally. The hygiene checks are individual scripts under `scripts/`, and you can run any of them directly — for example `bash scripts/check-taxonomy.sh origin/main`.

### Changing a protected path

Some files are listed in [`.github/protected-paths`](protected-paths) — the kernel: the capability registry, the CI workflows, the governance documents, the guard itself. Editing them is normal; open a pull request as usual. What differs is that `CODEOWNERS` requests a maintainer's review, and the Stem refuses to merge a Sprout-authored change to them at all. See [GUARDRAILS.md](../GUARDRAILS.md) for which layers enforce and which only record.

> [!NOTE]
> **Setting up a Ramet is a different task from setting up a development environment.** The Quick Local Setup above gets you building and testing the code. Running OpenTendril as a governed service — its own principal, credentials no caller can read — is [docs/GUIDE-INSTALL.md](../docs/GUIDE-INSTALL.md).
