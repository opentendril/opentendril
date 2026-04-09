# Trusted Nodes Registry

This document is the **authoritative ledger** for all Tendril instances that are authorized to submit verified commits to this repository. 

It is the human-readable counterpart to GitHub's GPG key verification. When a new maintainer or deployed Tendril node wants write access, they must submit a Pull Request adding their public key fingerprint to this file. The PR must be reviewed and merged by an existing trusted administrator.

---

## Approval Process

1. The new node operator runs `scripts/generate-node-identity.sh` on their machine.
2. The script generates a GPG key and outputs the **Key ID** and **Public Key**.
3. The operator uploads their Public Key to their GitHub account at https://github.com/settings/keys.
4. The operator submits a **Pull Request** to this repository adding their node entry below.
5. An existing **Trusted Administrator** reviews and merges the PR.
6. The node's commits will now show as **Verified** on GitHub.

> **Security Note:** Removing guardrails or sandbox protections in a forked copy of Tendril does not grant access to this repository. All contributions must pass through the PR review gate, and all commits on `main` must be signed by a key listed in this registry.

---

## Node Registry

### Administrators (Full Write Access)

| Node Name | Operator | GPG Key ID | Hostname | Registered |
|-----------|----------|------------|----------|------------|
| OpenTendril Agent | dr3w | `C0AA41FA9B3B4DBD` | phn16s | 2026-04-09 |

### Deployed Agents (PR-Only Access)

| Node Name | Purpose | GPG Key ID | Registered |
|-----------|---------|------------|------------|
| *No deployed agents registered yet* | | | |

---

## Revoking Access

To revoke a node's access:
1. Remove their entry from this registry via PR.
2. Remove their GPG key from the associated GitHub account.
3. If compromised, rotate the `SECRET_KEY` used for skill signing.
