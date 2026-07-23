# Git connection setup — get a Pollinator committing through Tendril

**The problem this solves:** every LLM wastes time and tokens guessing how to
authenticate to GitHub (which token? SSH? gh? App?) and often gets it wrong.
Tendril gives a **Pollinator** — an external requester, human or Mycorrhizal —
**one pre-configured, correct git method**. It
calls `git.status` / `git.branch` / `git.commit` / `git.push` / `git.pr`, and can
tidy up afterwards with `git.branch.list` / `git.prune`; it never touches
credentials. You
configure the connection **once**, on any machine, and every pollen you authorise
inherits it.

This guide is deliberately linear: follow it top to bottom.

---

## Quick start — one command

The fastest path is the built-in setup command. It writes the connection config
and the grant, and prints the subject's Model Context Protocol block:

```bash
# GitHub App (recommended — commits signed by GitHub, no key material):
tendril git setup --substrate myrepo --repo owner/repo \
  --app-id 123456 --key ~/.tendril/app.pem --grant-pollen claude

# Fine-grained token + dedicated GPG key:
tendril git setup --posture pat --substrate myrepo --repo owner/repo \
  --token-env TENDRIL_GITHUB_PAT --sign-key <gpg-key-id> \
  --identity-name "Tendril Bot" --identity-email bot@your-domain \
  --grant-pollen claude

# Check a connection's credentials are in place (makes no commit):
tendril git setup --verify --substrate myrepo
```

Prefer to write the files by hand? The rest of this guide is the manual
reference, and the repository ships `substrates.yaml.example` /
`grants.yaml.example` as templates to copy.

---

## The model in one picture

- A **Substrate** is a repository.
- A **Nodule** is a *git connection* — identity + auth + signing — defined once
  and shared by every repository that uses it (one Nodule → many Substrates).
- A **grant** says which Pollinator (by its Pollen) may run which git operation on which
  Substrate. No grant → no access (deny-closed).
- The **Stem** (Tendril's daemon) holds the secrets and does the git work.
  **Sprouts are network-sealed and never push.**

---

## Choose your connection tier

| | **Fine-grained PAT + GPG** | **GitHub App** |
|---|---|---|
| Auth | fine-grained Personal Access Token | GitHub App installation token (short-lived, auto-expiring, not tied to a person) |
| Signing | your **dedicated GPG key**, signed locally | **GitHub signs server-side** (verified) — no key material anywhere |
| Setup | one token + one GPG key | create an App once, then drop in one `.pem` |

The **GitHub App** is the higher-security posture: installation tokens are
short-lived, not tied to a person, and there is no key material on the machine.
The **fine-grained PAT + GPG** posture is the simplest to stand up: one scoped
token and one dedicated signing key. GPG signing is a PAT-path option; a GitHub
App connection signs on its own.

Do **not** use a classic Personal Access Token (blanket access). SSH keys can
push code but cannot open pull requests, so SSH alone cannot deliver full git
access.

---

## Path A — fine-grained PAT + GPG

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
tendril git branch --substrate opentendril --branch chore/verify-connection
tendril git commit --substrate opentendril --message "chore: verify connection"
tendril git push   --substrate opentendril
tendril git pr     --substrate opentendril --title "chore: verify connection"
```

The commit is signed by your dedicated key and attributed to the configured
identity — confirm with `git log --show-signature -1`.

---

## Path B — GitHub App

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

## Authorise a Pollinator (the security gate)

Access is **two keys**: the Pollinator must (a) be *connected* to Tendril and (b)
have a *grant*. Missing either → denied.

### 1. Authorise the Pollinator — `.tendril/grants.yaml` (Stem-owned, never inside a repo checkout)

```yaml
grants:
  claude:                                             # the Pollen
    operationClasses: [git.status, git.branch, git.commit, git.push, git.pr]  # commit-only? drop the rest
    substrates: [opentendril]
    expires: 2027-01-01
```

No grant → every delegated call from that pollen is denied and audited. Each
operation is its own class and confers nothing else: a Pollinator granted
`git.commit` and `git.push` still cannot open a pull request or create a
branch, `git.pr` never pushes on your behalf, and `git.branch` never commits.
`git.status` is gated too: read-only does not mean ungated, since a status
response names branches and changed file paths.

### 2. Configure the subject's Model Context Protocol connection

Point the Pollinator at Tendril's Model Context Protocol server and bind its
pollen (the Pollinator is set by the trusted launch configuration — a Pollinator can
never self-declare it):

```json
{ "mcpServers": { "opentendril": {
  "command": "tendril", "args": ["serve", "mcp", "stdio"],
  "env": { "TENDRIL_POLLEN": "claude" }
}}}
```

Give Codex / Grok / Antigravity the same block with their own pollen name and a
matching grant.

### Securing the instance itself

- **MCP (stdio):** the gate is your machine + the launch config that binds the
  pollen, plus the grant. Adequate for a personal machine.
- **REST / WebSocket:** set `BOTANIST_KEY`; callers must then send
  `Authorization: Bearer <token>`. Combined with grants this is the connect +
  authorise two-key gate.

---

## Cleaning up after merges

Merged branches pile up. Tendril classifies them against **evidence from
GitHub**, then deletes only the ones proven merged.

```bash
tendril git branches --substrate myrepo          # classify, change nothing
tendril git prune    --substrate myrepo          # report what WOULD go
tendril git prune    --substrate myrepo --confirm  # actually delete
```

**Why evidence, and not `git branch --merged`:** a branch merged through a
squashing pull request still looks *unmerged* to git, because its commits never
enter the target's history. The same goes for diffing against the base. Both
look authoritative and both are wrong. Tendril asks GitHub which pull request a
branch's tip belongs to and whether that pull request merged — the only answer
that survives a squash merge.

A branch is deleted only when **all** of these hold:

| Classification | Deleted? |
|---|---|
| pull request **merged** | ✅ the only deletable state |
| pull request still **open** | ❌ work in flight |
| pull request **closed without merging** | ❌ rejected work; its commits may exist nowhere else |
| tip **never pushed** | ❌ local-only work no remote check can vouch for |
| tip pushed, **no pull request** | ❌ no evidence either way |
| the **default** or **current** branch | ❌ never |
| **checked out by another subject** | ❌ someone is working on it |
| no GitHub credential on the connection | ❌ nothing is deletable without evidence |

`prune` **reports by default** and deletes only with `--confirm`. That is the
opposite of the usual convention, deliberately: for an operation a Pollinator might
invoke after misreading its instructions, the safe path should be the one taken
by accident. Every deletion prints the branch's tip commit, so an unwanted prune
is a one-line `git branch <name> <head>` away.

**`git.prune` is not in the default grant.** Every other operation on the ladder
can be undone; deleting a branch cannot. Add it per pollen, knowingly:

```yaml
grants:
  claude:
    operationClasses: [git.status, git.branch.list, git.branch, git.commit, git.push, git.pr, git.prune]
```

---

## Credentials: proving a Pollen instead of declaring one

A **Pollen** names which grants apply. How a Pollinator *establishes* that Pollen
decides whether the grant model is a boundary or a record.

```bash
tendril pollinator issue --pollen claude --note "laptop"
tendril pollinator list
tendril pollinator revoke --pollen claude
```

The secret prints **once** and is never stored — only its digest is kept, so a
leaked store cannot be replayed. That secret is the **durable refresh root**.

### Minting an access token

Surfaces accept a **short-lived access token** (≤15 minutes) per request. Mint
one from a held root:

**Local (filesystem-trusted Botanist CLI):**

```bash
tendril pollinator token --pollen claude
tendril pollinator token --pollen claude --ttl 5m   # shorter; over 15m is refused
```

**Remote client** (presents the durable root only at mint):

```bash
curl -s -X POST http://127.0.0.1:8080/v1/pollinator/token \
  -H "Authorization: Bearer tendril_..." \
  -H "Content-Type: application/json" \
  -d '{}'
# → { "token": "tendrilat_...", "pollen": "claude", "expiresAt": "..." }
```

Then call data routes with the access token:

```bash
curl -X POST http://127.0.0.1:8080/v1/git/status \
  -H "Authorization: Bearer tendrilat_..." -d '{"substrate":"myrepo"}'
```

On **loopback** binds, presenting the durable root on a data route still works
for local convenience. On an **off-host** bind (`TERROIR_HOST=0.0.0.0`, …), data
routes refuse the root — mint a token first. The mint endpoint always accepts
the root. MCP stays personal-stdio; scoped tokens are a REST surface.

The Stem **derives** the Pollen from a verified credential or token. The
`X-OpenTendril-Pollen` header is ignored entirely for such callers, so holding a
secret cannot be used to act as somebody else. An unknown, revoked, expired, or
forged bearer is refused outright and never falls back to a declared identity.

A grant is still required. A credential or token says *who you are*; the grant
says *what that identity may do*.

| How the Pollen is established | What it is |
|---|---|
| Issued credential (`tendril_...`) | **proven** refresh root — mint only (or data routes on loopback) |
| Access token (`tendrilat_...`) | **proven** short-lived — signature-verified per request |
| `X-OpenTendril-Pollen` header, or `TENDRIL_POLLEN` at a terminal | **declared** — an audit control, not a boundary |

## Is the boundary actually real here?

```bash
tendril hardiness
```

It reports what is true on *this* machine: whether the Stem has its own
operating-system user, whether its credentials are readable by the Pollinators it
serves, whether credentials have been issued, and whether any grants exist.

This matters because the strongest credential model is still walkable if the Stem
and its callers are the same user — that user can read the private key, rewrite
`grants.yaml`, or ignore `tendril` entirely. On such a Terroir `hardiness` says so,
in those words. Run the Stem as its own user to make the boundary enforced rather
than advisory.

---

## Working from a terminal as a Pollinator

A Pollinator that drives a terminal — rather than connecting over the Model
Context Protocol — declares its Pollen in the environment:

```bash
export TENDRIL_POLLEN=claude
tendril git status --substrate myrepo      # authorised, audited, isolated workspace
```

With `TENDRIL_POLLEN` set, every delegated operation is checked against the
grants, the decision is written to `history.db`, and the work runs in that
Pollinator's own workspace. A refused operation exits non-zero and does nothing.

Without it, `tendril git ...` behaves exactly as it always has: you are the
**Botanist**, working in your own checkout, ungated.

> **This is an audit control, not a security boundary.** The Pollen is *declared
> by the caller*, so at a terminal a Pollinator can claim any Pollen it likes —
> including one with wider grants. And a caller running as the same operating
> system user as the Stem can read its credentials, rewrite `grants.yaml`, or
> ignore `tendril` altogether.
>
> What this **does** stop: the wrong substrate, an operation nobody meant to
> grant, and the quiet drift into working outside the governed path — the
> failures that actually happen. What it **does not** stop: a Pollinator that
> decides to lie.
>
> The boundary becomes real when the Stem runs as its **own** operating-system
> user, owning credentials no Pollinator can read. Until then, treat a declared
> Pollen as a statement of intent that is recorded, not a permission that is
> enforced.

---

## Several Pollinators at once

Tendril is built for simultaneous work, so **each authorised pollen gets its own
isolated workspace** for a repository — a private git worktree, created on first
use, keyed to that pollen.

This matters more than it sounds. Without it, two pollen sharing one checkout
will commit each other's half-finished files, onto each other's branches, under
each other's identity, with no error anywhere. With it:

- each subject's commits contain only that subject's work, correctly attributed;
- each Pollinator branches from the repository's branch, not from whatever another
  pollen happened to be doing;
- both pollen' branches are still visible in your repository, so pushing,
  pull requests, and your own review work exactly as before (a worktree shares
  the repository's object store).

You do not configure anything for this. The pollen — the one bound in
its MCP block — is the key, so isolation follows the same identity your grants
already use. `tendril git status` reports which workspace it is describing.

A workspace arrives **already on its own branch**, cut from the repository's
real default branch. The pollen is never asked which branch to work on, so it
cannot pick the wrong one — committing to your default branch is not refused,
it is impossible, because no delegated workspace is ever on it.

Those branches are **owned**: Tendril records each one when it creates it and
takes it away again when it is finished. A run that produces nothing takes its
branch with it; a branch carrying commits is always kept, published or not.
Nothing Tendril creates is left for you to clean up later.

Running `tendril git ...` yourself, at a terminal, is not delegated — it uses
your own checkout, as you would expect.

**One caution about `checkout: { mode: path }`:** that mode points Tendril at
*your* clone. Sprout runs refresh a checkout to a pristine tree before working,
so Tendril now refuses to refresh your own checkout when it has uncommitted
changes rather than discarding them. If you want Tendril working continuously
without touching your files, prefer `mode: managed` — its own clone.

---

## Look before acting — `tendril git status`

Every guardrail below exists because a Pollinator guessed something it could not
see. `git.status` is how it sees instead: one read-only, offline call that
reports what git says *and* what Tendril will do about it.

```bash
tendril git status --substrate myrepo
```

```
⛔ Commit blocked: the workspace is on "main", the repository's default branch
   (default branch "main" (from remote-head)) — create a feature branch with
   git.branch first
🌱 main · owner/repo · default: main (remote-head)
   upstream: origin/main · ahead 0, behind 2
   workspace: 3 change(s) — 2 modified, 0 added, 0 deleted, 0 renamed, 1 untracked
     modified  cmd/thing.go
     modified  README.md
     untracked notes.txt
```

It reports the current branch, the resolved default branch **and how it was
determined**, uncommitted changes with counts by kind, ahead/behind against the
upstream (or "no upstream" for a branch never pushed) — and, crucially, whether
a commit would be allowed right now, with the reason when it would not.

That prediction is computed by the *same code the commit guard runs*, so status
can never tell you a commit is fine and then have it refused. A repository with
no commits, no upstream, or a detached head is described rather than refused;
the changed-file list is capped, with the true total always reported.

---

## The default branch is protected, everywhere

The most expensive git mistakes are branch mistakes found late: work committed
straight onto the default branch, then unpicked with a rebase or reversed onto
a feature branch after the fact. Tendril treats that as a safety property, not
a convention.

**Tendril never assumes which branch is the default.** It resolves it — from the
substrate's configured `branch:`, from the repository itself, or from your
clone's record of the remote's head — and if it genuinely cannot tell, it
protects the well-known names anyway. Protection widens when Tendril is
uncertain; it never quietly switches off. A repository whose default branch is
`trunk` or `develop` is protected exactly as one on `main`.

What that means in practice:

- `git.commit` **refuses to commit onto the default branch** — before staging
  anything, so nothing has to be undone.
- `git.branch` is the way forward, and it is why the refusal is not a dead end.
- `git.pr` refuses a pull request opened *from* the default branch.
- Sprout runs auto-branch off the default branch before making changes.

```bash
# The loop, in the order the guardrails expect:
tendril git status --substrate myrepo                      # look before acting
tendril git branch --substrate myrepo --branch feat/new-leaf
tendril git commit --substrate myrepo --message "feat: grow a new leaf"
tendril git push   --substrate myrepo
tendril git pr     --substrate myrepo --title "feat: grow a new leaf"
```

If a repository legitimately commits straight to its default branch (a docs
site, a notes repository), opt out once, in the connection:

```yaml
substrates:
  mynotes:
    url: https://github.com/owner/notes
    profile: tendril-dedicated
    protectDefaultBranch: false     # knowingly loosened; absent means protected
```

`git.branch` itself is deliberately narrow — create a branch and switch to it.
No delete, no rename, no reset. An existing branch is switched to, never
force-moved; switching to an existing branch with uncommitted changes is
refused so work is never carried somewhere you did not expect (starting a *new*
branch with uncommitted work is fine — that is the usual "I started editing
before branching" recovery).

---

## Opening pull requests — the branch rules Tendril enforces

`tendril git pr` finishes the loop: commit → push → pull request, all through
the same connection, so a Pollinator never has to guess at credentials or shell out
to another tool for the last mile.

```bash
tendril git pr --substrate opentendril --title "feat: grow a new leaf" \
  --body "What changed and why." [--head B] [--base B] [--draft]
```

It is deliberately strict about branches, because the expensive failures here
are branch mistakes discovered late — rebases, merge repair, and reversing
commits off the default branch:

- **The base branch is read, never assumed.** Omit `--base` and Tendril asks the
  repository for its *actual* default branch. It never assumes `main`. An
  explicit `--base` always wins.
- **The head branch defaults to the workspace's current branch** — read from
  actual state, not guessed.
- **Opening a pull request from the default branch is refused.** If the head
  branch *is* the default branch, the work was committed to the wrong place;
  Tendril says so while it is still cheap to fix, instead of after a merge that
  has to be unpicked. There is no override flag — create a feature branch.
- **A repeat call never duplicates.** If an open pull request already exists for
  that head branch, Tendril returns it (`status: exists`) and changes nothing —
  including leaving its title and body alone, so a description you have edited
  is never overwritten.
- **It never pushes.** `git.push` and `git.pr` are separate grants; push first.

Both connection postures work. Opening a pull request needs a GitHub API
credential, so a connection using an SSH key — or none — is refused with an
error naming the two postures that do work.

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
`tendril`, and the same pollen work with the same grants. No local toolchain
sprawl, no re-guessing auth.
