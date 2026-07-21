# Model Context Protocol configuration

`mcp-config.json` wires two servers, and the distinction between them matters.

## `opentendril` — this Ramet, as a governed surface

Gives a **Pollinator** the governed git path: `git.status`, `git.branch.list`,
`git.branch`, `git.commit`, `git.push`, `git.pr`, `git.prune`. Every delegated
call is authorised against the grants in `.tendril/grants.yaml`.

`OPENTENDRIL_POLLEN` binds the **Pollen** — the identity the grant names. It is
set here, in the trusted launch configuration, and **never** by the Pollinator
itself: a Pollen a caller could name is a workspace a caller could claim. Give
each Pollinator its own block with its own Pollen, and grant it explicitly:

```yaml
# .tendril/grants.yaml
grants:
  claude:
    operationClasses: [git.status, git.branch.list, git.branch, git.commit, git.push, git.pr]
    substrates: [opentendril]
```

No grant, no access. `git.prune` is deliberately absent from the default set —
it deletes branches, and every other operation is recoverable.

## `github` — a third-party Symbiotic Nodule

`@modelcontextprotocol/server-github` is an external service, and
`GITHUB_PERSONAL_ACCESS_TOKEN` is **that server's own** variable name, not
Tendril's. Tendril's canonical variable is `GITHUB_TOKEN` (see `AGENTS.md`),
which is what the `${GITHUB_TOKEN}` reference passes through — so one token,
named as each side expects.

This server is read-heavy and separate from the governed path: it does not pass
through the grant model, so anything it can write is ungoverned. Prefer the
`opentendril` server for anything that changes the Substrate.
