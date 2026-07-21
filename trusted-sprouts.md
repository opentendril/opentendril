# Trusted Ramets Registry

This document is the **authoritative ledger** for every **Ramet** — one running Tendril instance — authorised to submit verified commits to this repository. 

It is the human-readable counterpart to GitHub's GPG key verification. When a new Botanist or a deployed Ramet wants write access, they must submit a Pull Request adding their public key fingerprint to this file. The Pull Request must be reviewed and merged by an existing trusted administrator.

---

## Approval Process

1. The Botanist registering a new Ramet runs `scripts/generate-node-identity.sh` on its Terroir.
2. The script generates a GPG key and outputs the **Key ID** and **Public Key**.
3. The Botanist uploads the Public Key to the associated GitHub account at https://github.com/settings/keys.
4. The Botanist submits a **Pull Request** to this repository adding the Ramet's entry below.
5. An existing **Trusted Administrator** reviews and merges the PR.
6. The Ramet's commits will now show as **Verified** on GitHub.

> **Security Note:** Removing guardrails or terrarium protections in a forked copy of Tendril does not grant access to this repository. All contributions must pass through the PR review gate, and all commits on `main` must be signed by a key listed in this registry.

---

## Ramet Registry

### Administrators (Full Write Access)

| Ramet | Botanist | GPG Key ID | Terroir | Registered |
|-------|----------|------------|---------|------------|
| OpenTendril | OpenTendril | `C0AA41FA9B3B4DBD` | phn16s | 2026-04-09 |

### Deployed Ramets (Pull-Request-Only Access)

| Ramet | Purpose | GPG Key ID | Registered |
|-------|---------|------------|------------|
| *No deployed Ramets registered yet* | | | |

---

## Revoking Access

To revoke a Ramet's access:
1. Remove its entry from this registry via a Pull Request.
2. Remove its GPG key from the associated GitHub account.
3. If compromised, rotate the `SECRET_KEY` used for skill signing.
