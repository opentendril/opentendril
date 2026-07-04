# Design: Node Identity & Cryptographic Commit Signing

> **Status:** Design RFC — supersedes `scripts/generate-node-identity.sh`
> **Proposed CLI:** `stem identity init | show | verify`

---

## Why Node Identity Matters

Every Tendril agent that commits code, opens a PR, or emits a Sequence event does so
autonomously — without a human hand on the keyboard. Without cryptographic identity,
there is no way to distinguish:

- A legitimate, authorised Tendril commit from a compromised one
- A trusted node from an injected agent
- An auditable action from a spoofed one

Node Identity gives every Tendril deployment a unique GPG key pair. All commits,
Plasmid injection events, and Sequence completion events are signed with the node's
private key. Any consumer can verify the signature against the Trusted Nodes registry.

---

## Proposed CLI

```
stem identity init         # Generate a new Ed25519 GPG key for this node
stem identity show         # Display the current node's public key (copy to GitHub)
stem identity verify <sha> # Verify a commit or event was signed by a trusted node
```

All identity material lives at the XDG user config path:

```
~/.config/opentendril/
  identity/
    node.gpg          # Private key (GPG keyring entry)
    node.pub          # Exported public key (human-readable)
  trusted-nodes.gpg   # Public keyring of all trusted node identities
```

---

## Key Generation (from `generate-node-identity.sh`)

The original script used Ed25519 (modern, compact, well-supported):

```bash
gpg --batch --gen-key <<EOF
%no-protection
Key-Type: eddsa
Key-Curve: ed25519
Subkey-Type: eddsa
Subkey-Curve: ed25519
Name-Real: ${TENDRIL_NODE_NAME:-Tendril Node}
Name-Email: ${TENDRIL_NODE_EMAIL:-tendril@opentendril.dev}
Name-Comment: OpenTendril Autonomous Agent — $(hostname)
Expire-Date: 2y
%commit
EOF
```

The `stem identity init` CLI command wraps this with:
- XDG path management (writes key to `~/.config/opentendril/identity/`)
- Structured output (prints public key block for GitHub upload)
- Environment variable support: `TENDRIL_NODE_NAME`, `TENDRIL_NODE_EMAIL`
- Docker deployment notes: mount GPG volume or inject via secrets

---

## Git Integration

After `stem identity init`, the Stem configures git for signed commits
**scoped to the workspace** (not global):

```
git config user.signingkey  <KEY_ID>
git config commit.gpgsign   true
git config tag.gpgsign      true
git config user.name        <TENDRIL_NODE_NAME>
git config user.email       <TENDRIL_NODE_EMAIL>
```

All commits from OpenTendril agents will appear as **Verified** on GitHub
once the public key is uploaded to `https://github.com/settings/keys`.

---

## Trusted Nodes Registry

The registry is a GPG keyring at `~/.config/opentendril/trusted-nodes.gpg`.

```
stem identity trust <pubkey-file>   # Add a public key to the trusted nodes registry
stem identity untrust <fingerprint> # Remove a key from the registry
stem identity list-trusted          # List all trusted node fingerprints
```

The orchestrator verifies Plasmid signatures (see Plasmid Integrity Signing issue)
against this keyring. Unrecognised or missing signatures are rejected if the active
Genotype declares `requirePlasmidSignatures: true`.

---

## Docker Deployments

For containerised deployments, the private key should be injected via Docker secrets
or a mounted GPG volume — **never baked into the image**:

```yaml
# docker-compose.yml
services:
  stem:
    volumes:
      - tendril-identity:/root/.config/opentendril/identity:ro
secrets:
  - tendril_gpg_passphrase
```

> **Note on naming:** `tendril_gpg_passphrase` is lowercase because it is a Docker secret identifier, which by convention uses lowercase with underscores, unlike POSIX environment variables (e.g. `TENDRIL_NODE_NAME`) which are uppercase.

The `TENDRIL_NODE_NAME` and `TENDRIL_NODE_EMAIL` environment variables control
the identity without requiring interactive input.

---

## Implementation Path

1. `stem identity init` — wraps GPG generation, writes to XDG paths
2. `stem identity show` — exports public key block
3. `stem identity trust` — manages the trusted-nodes.gpg keyring
4. `stem identity verify` — verifies a commit SHA or event against the keyring
5. Wire into `commitTerrariumExecutionFn` in `sequence.go` to auto-sign Terrarium commits
