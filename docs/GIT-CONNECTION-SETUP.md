# Git connection setup — get an agent committing through Tendril

**The problem this solves:** every LLM wastes time and tokens guessing how to
authenticate to GitHub (which token? SSH? gh? App?) and often gets it wrong.
Tendril gives an agent **one pre-configured, correct git method**. The agent
calls `git.commit` / `git.push`; it never touches credentials. You configure the
connection **once**, on any machine, and every agent you authorise inherits it.

This guide is deliberately linear: follow it top to bottom.

---

## The model in one picture

- A **Substrate** is a repository.
- A **Nodule** is a *git connection* — identity + auth + signing — defined once
  and shared by every repository that uses it (one Nodule → many Substrates).
- A **grant** says which agent (subject) may run which git operation on which
  Substrate. No grant → no access (deny-closed).
- The **Stem** (Tendril's daemon) holds the secrets and does the git work.
  **Sprouts are network-sealed and never push.**

---

## Choose your connection tier

| | **Fine-grained PAT + GPG** | **GitHub App** *(recommended target)* |
|---|---|---|
| Auth | fine-grained Personal Access Token | GitHub App installation token (short-lived, auto-expiring, not tied to a person) |
| Signing | your **dedicated GPG key**, signed locally | **GitHub signs server-side** (verified) — no key material anywhere |
| Setup cost | ~5 minutes, works **today** | create an App once, then drop in one `.pem` |
| Status | fully working | auth works today; **API-signed commit creation is a separate, in-progress capability** |

> **Recommendation:** the GitHub App is the gold standard (highest security,
> survives staff turnover, no local key material) and is the documented default.
> Until the API-signed commit path lands, use the **fine-grained PAT + GPG**
> path below — it is fully secure and works now. GPG signing is a PAT-path
> option; the GitHub App signs on its own.

Do **not** use a classic Personal Access Token (blanket access). SSH keys can
push code but cannot open pull requests, so SSH alone cannot deliver full git
access.

---

## Path A — fine-grained PAT + GPG (works today)

### 1. Create a dedicated fine-grained Personal Access Token

GitHub → Settings → Developer settings → Fine-grained tokens → Generate.
Scope it to **only** the repositories Tendril should touch, with repository
permissions **Contents: Read and write** and **Pull requests: Read and write**.
Put it in your environment (never in `substrates.yaml`):

```bash
# .env  (referenced by name only; the secret never enters config files)
TENDRIL_GITHUB_PAT=github_pat_xxxxxxxx
```

### 2. Create a dedicated GPG signing key

A **dedicated** key (not your personal one), so Tendril's commits are clearly
attributable to the automation identity:

```bash
gpg --quick-generate-key "Tendril Bot <tendril-bot@your-domain>" ed25519 sign never
gpg --list-secret-keys --keyid-format LONG    # note the key id
```

Export the public key and add it to GitHub → Settings → SSH and GPG keys → New
GPG key, so commits show **Verified**.

### 3. Write the connection

`substrates.yaml` (repository root or `.tendril/`):

```yaml
credentials:                       # a credentials profile IS a Nodule
  tendril-dedicated:
    auth:     { method: pat, env: TENDRIL_GITHUB_PAT }
    sign:     { method: gpg, key: "<YOUR_KEY_ID>" }
    identity: { name: "Tendril Bot", email: "tendril-bot@your-domain" }

substrates:
  opentendril:
    url:      https://github.com/opentendril/opentendril
    profile:  tendril-dedicated
    checkout: { mode: managed }    # see "Where Tendril works" below
```

### 4. Verify

```bash
tendril git commit --substrate opentendril --message "chore: verify connection"
tendril git push   --substrate opentendril
```

The commit is signed by your dedicated key and attributed to the configured
identity — confirm with `git log --show-signature -1`.

---

## Path B — GitHub App (recommended target)

**No OAuth, no client secret.** Tendril is a server-to-server automation: the
App authenticates *as itself* (a signed JWT mints a short-lived installation
token). OAuth/client-secret is only for browser flows where a human logs in —
Tendril never needs it.

### 1. Create the App (once)

GitHub → Settings → Developer settings → GitHub Apps → New GitHub App.
Repository permissions: **Contents: Read and write**, **Pull requests: Read and
write**, **Metadata: Read** (required). Generate a private key (`.pem`) and note
the **App ID**. Install the App on the repositories it should manage.

### 2. Drop in the key and configure

```yaml
credentials:
  github:
    auth: { method: app, appId: "123456", privateKeyPath: ~/.tendril/app.pem }
    # no sign: block — the GitHub App signs commits server-side (verified)
    identity: { name: "Tendril Bot", email: "tendril-bot@your-domain" }

substrates:
  opentendril:
    url:      https://github.com/opentendril/opentendril
    profile:  github
    checkout: { mode: managed }
```

`installationId` is auto-discovered; `privateKeyEnv` may hold the PEM contents
instead of a path. **That `.pem` + App ID is the whole per-machine setup.**

---

## Grant an agent access (the security gate)

Access is **two keys**: the agent must (a) be *connected* to Tendril and (b)
have a *grant*. Missing either → denied.

### 1. Authorise the subject — `.tendril/grants.yaml` (Stem-owned, never inside a repo checkout)

```yaml
grants:
  claude:                                    # the agent's subject identity
    operationClasses: [git.commit, git.push] # commit-only? drop git.push
    substrates: [opentendril]
    expires: 2027-01-01
```

No grant → every delegated call from that subject is denied and audited.

### 2. Configure the agent's MCP connection

Point the agent at Tendril's MCP server and bind its subject (the subject is set
by the trusted launch config — an agent can never self-declare it):

```json
{ "mcpServers": { "opentendril": {
  "command": "tendril", "args": ["serve", "mcp", "stdio"],
  "env": { "OPENTENDRIL_DELEGATION_SUBJECT": "claude" }
}}}
```

Give Codex / Grok / Antigravity the same block with their own subject name and a
matching grant.

### Securing the instance itself

- **MCP (stdio):** the gate is your machine + the launch config that binds the
  subject, plus the grant. Adequate for a personal machine.
- **REST / WebSocket:** set `ADMIN_TOKEN`; callers must then send
  `Authorization: Bearer <token>`. Combined with grants this is the connect +
  authorise two-key gate. (Short-lived scoped tokens for networked/multi-user
  instances are on the roadmap — see #273.)

---

## Where Tendril works, and getting changes into your checkout

`checkout.mode` decides where the code lives:

| mode | location | use when |
|---|---|---|
| `path` | **your existing (Terroir) clone** | you want Tendril to operate in your working copy |
| `managed` | **Tendril's own clone** under `~/.tendril/substrates/<name>`, separate and refreshed clean each run | *(default)* never touches your working files; portable |
| `ephemeral` | throwaway `/tmp` clone, deleted after | one-off runs |

**Local testing workflow** (Tendril builds + commits, you review before deploy):

Tendril works on a **git worktree branched from the substrate**, so a commit
lands as a **branch in the repository's object store**. To bring it into your
own checkout you have two configurable routes:

1. **Via the remote (system of record):** Tendril pushes the branch; you
   `git fetch` + check it out / review the PR. Works identically for a local or
   a remote Tendril instance.
2. **Locally, without pushing:** when you instruct Tendril to commit but *not*
   push, fetch straight from Tendril's managed clone —
   `git fetch ~/.tendril/substrates/<name> <branch>` — then check out the branch
   in your location and review or deploy to dev.

Either way the exchange medium is git, and the branch is signed and attributable
before you ever see it.

---

## Portability to another machine

The connection is a movable artifact: copy `substrates.yaml`, `.tendril/`
(grants + App `.pem`), and your `.env` secrets to the new machine, install
`tendril`, and the same agents work with the same grants. No local toolchain
sprawl, no re-guessing auth.
