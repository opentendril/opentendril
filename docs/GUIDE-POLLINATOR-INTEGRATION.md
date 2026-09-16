# Pollinator Integration Guide

OpenTendril is a governed Git path and isolation boundary for **Pollinators** —
external requesters, human or Mycorrhizal, that request work from the Stem. A
Pollinator never receives a raw Git token. It talks to the Stem through a
transport adapter, commonly `tendril-mcp` over Model Context Protocol. The Stem
resolves the Substrate, uses credentials it holds, and runs work inside an
isolated Sprout within a Terrarium. The Substrate entry names the target
repository and the environment variable holding a Git credential. The Stem
clones, runs, and publishes from its side while the Sprout stays sealed inside
its Terrarium.

## Architecture

A governed installation and a single-user installation use different MCP
executables.

**Governed installation**

```text
Pollinator
    -> tendril-mcp
    -> named connection profile
    -> authenticated Stem HTTPS identity
    -> profile credential reference
    -> durable Pollinator root used only for minting
    -> short-lived access token for governed frames
    -> governed Stem
    -> seedGrow(detached, idempotencyKey)
    -> phytomerContinue
    -> sproutWatch
```

`seedGrow` requires `seed.grow`. `phytomerContinue` requires `phytomer.continue`.
`sproutWatch` requires `sprout.watch`. Each grant is checked independently.
Pollen comes from the governed credential. Continuation Substrate comes from
Stem-owned Phytomer state; the caller cannot self-declare it. `sproutWatch` is a
view, not a governed command.

`tendril-mcp` is the Pollinator-side client. It authenticates the Stem TLS peer
before it reads or presents the Pollinator's durable root. It uses the root
only at the mint endpoint, then forwards MCP frames with the short-lived access
token. Authorization and Pollen derivation stay at the Stem. The client cannot
construct a Stem and has no in-process mode. Do not launch the protected
`tendril` binary from a governed Pollinator account.

For a Pollinator on the Stem host, configure literal-loopback HTTP:

```bash
tendril-mcp connection set local --endpoint http://127.0.0.1:8080 --credential codex
```

For a remote Pollinator, use the Stem's HTTPS name covered by its certificate:

```bash
tendril-mcp connection set remote \
  --endpoint https://stem.example.net:8443 \
  --credential codex \
  --trust-anchor stem-ca
tendril-mcp diagnose --connection remote
```

Omit `--trust-anchor` when the Stem certificate chains to an operating-system
root. When a private CA is used, install only its public PEM certificate as
`~/.config/tendril/trust-anchors/stem-ca`, owned by the Pollinator account and
readable by that account. The profile accepts a named reference, not an
arbitrary file path. The named CA is appended to system roots; regular chain and
hostname/IP SAN verification still applies. The server certificate's DNS name
or IP SAN must match the endpoint. No Pollinator client certificate is used.

The remote listener is configured on the Stem with
`TENDRIL_REMOTE_LISTEN_ADDR`, `TENDRIL_REMOTE_TLS_CERT`, and
`TENDRIL_REMOTE_TLS_KEY`. All three are required together; all absent disables
the listener and incomplete or invalid settings fail Stem startup. TLS
terminates at the Stem with TLS 1.2 minimum. Its private key is separate from
the access-token signing material.

### Public HTTPS projection

The remote listener is a deliberately narrow Pollinator-only projection within
the same Stem process and shared Core authority. It is not the Botanist
management interface, the Greenhouse interface, the full local/private Stem
mux, a second Stem, or a reverse proxy trust boundary. The public routes are
exactly:

| Route | Credential |
|---|---|
| `GET /health` | None |
| `POST /v1/pollinator/token` | Durable Pollinator root |
| `POST /v1` | Short-lived Stem-signed access token |
| `POST /v1/seeds/grow` | Short-lived Stem-signed access token |
| `POST /v1/seeds/grow/async` | Short-lived Stem-signed access token |
| `GET /v1/seeds/runs/{handle}` | Short-lived Stem-signed access token |
| `POST /v1/phytomers/{sessionId}/continue` | Short-lived Stem-signed access token |
| `GET /v1/phytomers/{sessionId}/watch` | Short-lived Stem-signed access token |

Legacy `/v1/sessions/...` aliases are not public. Botanist configuration, mesh
management, pending-confirmation management, chat, WebSocket, Greenhouse,
ordinary Phytomer CRUD, and other local/private routes are not projected on
this listener. No tunnel is required: the supported public trust topology
terminates Pollinator TLS directly at the Stem.

The durable root is accepted only at `POST /v1/pollinator/token`. Use the
returned short-lived access token for public data and MCP requests. The
Botanist bearer is invalid on the public Pollinator surface, and the durable
root is invalid on ordinary public data and MCP routes. Send the credential in
the `Authorization` header; query-string bearer material has no public
authentication effect. `Forwarded` and `X-Forwarded-*` metadata do not affect
Pollen, authority, listener provenance, limits, or rate identity. Each
governed admission reads the live grant state; already-minted access tokens
still age out cryptographically within their 15-minute cap.

Public MCP accepts protocol initialization, `tools/list`, and `tools/call`. It
does not expose repository-backed `resources/list` or `resources/read`.
`tools/list` contains the primary MCP identifiers for canonical Core
capabilities in `DelegatedCapabilityNames()` plus the `sproutWatch` view. It
does not list compatibility aliases; an alias can resolve only to an already
allowed delegated Core capability. The public MCP projection is not the full
ordinary local MCP/parity surface and does not create a second capability
authority model.

Public `GET /health` is passive in-memory readiness. It does not run active
`healthmon` checks, publish health events, inspect repositories, contact
providers, invoke Docker, create Sprouts or Terrariums, or expose the local
owner UID. The local/private `/health` remains the active operator-health
behavior.

The current public transport defaults are:

| Boundary | Default |
|---|---:|
| Simultaneous accepted TCP connections | `128` |
| `ReadHeaderTimeout` | `5s` |
| `IdleTimeout` | `60s` |
| `MaxHeaderBytes` | `32 KiB` |
| Global `ReadTimeout` | unset |
| Global `WriteTimeout` | unset |
| Ordinary public request body maximum | `4 MiB` |
| Token-mint request body maximum | `16 KiB` |
| Simultaneous public request handling | `64` |
| Simultaneous authenticated admissions | `32` |
| Simultaneous long-lived REST observations | `16` |
| Simultaneous token-mint requests | `8` |
| Global token-mint rate | `4 requests/second` |
| Token-mint burst | `8` |

These are public-boundary defaults. Clients may receive `413` when a request
body exceeds its limit, `429` when the global token-mint bucket is exhausted,
or `503` when a public concurrency bound is unavailable. The mint bucket is
global and has no IP-derived or forwarding-header-derived identity; there is
no generic system-wide rate limiter or global HTTP read/write deadline.
Excess TCP connections beyond the 128-connection cap are closed before TLS and
HTTP handling begins, so that cap does not produce an HTTP `503` response.

The remote root is sent only to `POST /v1/pollinator/token`. Governed data and
MCP requests use the returned access token, capped at 15 minutes. Root
revocation stops the next mint without a Stem restart; current grant removal or
narrowing blocks the next governed admission without a restart. Already-issued
tokens do not override current grants.

Plain HTTP is supported by the restricted client only for a literal loopback IP
and requires the existing separate Unix-owner check. HTTPS uses TLS identity
and does not treat Unix UID as Stem identity. The client refuses non-loopback
plaintext HTTP before reading or sending the root. Forwarded headers cannot
change Pollen, authority, listener provenance, or limits. No SSH, VPN, or tunnel
is required. The standalone Gateway is not the remote Pollinator ingress;
allowed requests still reach the same Stem Core authority and
Sprout/Terrarium execution boundaries.

The profile and credential must be owned by the Pollinator account. The
credential reference `codex` resolves to
`~/.config/tendril/pollinators/codex`, mode `0600`. Use
`tendril-mcp diagnose --connection remote` to verify connection posture and
minting without invoking governed MCP capabilities. Diagnostics do not print
root or access-token values.

The user-level connection metadata is stored in
`~/.config/tendril/connections.yaml`; it contains endpoint and named references,
not secret material.

**Single-user installation**

`tendril mcp` is the supported single-user stdio command. It is a governed
command surface and stdio bridge that retains its current environment-based
forwarding and in-process behavior. It may run in-process as the operator, or
forward to another-user Stem when a credential and reachable Stem are present.

`tendril setup substrate` writes `~/.tendril/substrates.yaml` and prints a
single-user MCP snippet for `tendril mcp`. That helper is not the governed
Pollinator install path.

The generated Substrate is named `default-workspace`. Use it when calling
`sproutGrow` or `sequenceGrow` for code changes. Grants still name the
canonical operation-classes `sprout.grow` and `sequence.grow`.

**Independent REST client**

An independent REST client first verifies the Stem HTTPS certificate and
hostname/IP SAN using system roots and, when configured, its named public trust
anchor. Only then does it read and present its Pollinator root to
`POST /v1/pollinator/token`. It sends the resulting short-lived access token on
governed data routes. Remote requests use the same Stem Core authority and
authorization as MCP within the public route projection above. The root is
never sent on governed data routes.

Detached Seed calls require a caller-provided `idempotencyKey`. Reuse the same
key only with the same Pollen and semantic request; a replay returns the
original Seed handle and Phytomer without replacement work. A changed semantic
request with the same Pollen and key is refused. Continue and observe that same
Phytomer under their separately granted operation classes. The resulting Fruit
is Git-reviewable, and the default branch remains human-controlled.

## Bootstrap the config

Run:

```bash
tendril setup substrate
```

The command prompts for:

- the target Git remote URL
- the environment variable name that holds your PAT

It then writes `~/.tendril/substrates.yaml` and prints an MCP configuration
snippet to stdout.

## Issue a credential

A Pollinator authenticates as a specific Pollen. The credential is durable, and
its only power is minting the short-lived access tokens that carry requests.

On a governed installation the binary is mode 750 under the Stem's own account,
so issuance runs as the Stem and cannot write into your home. Issue to a staging
path, install it where the Pollinator will read it, then destroy the staging copy:

```bash
sudo -u tendril -i tendril pollinator issue --pollen claude --out /tmp/claude.cred
mkdir -p -m 700 ~/.config/tendril/pollinators
sudo install -o "$USER" -g "$USER" -m 600 /tmp/claude.cred ~/.config/tendril/pollinators/claude
shred -u /tmp/claude.cred
```

On a single-user installation, where the binary is on your own PATH, `--out`
writes the final location directly:

```bash
tendril pollinator issue --pollen claude --out ~/.config/tendril/pollinators/claude
```

`--out` prints the path it wrote and never the secret; without it the secret goes
to stdout instead. It refuses an existing file unless you add `--force`.

**Check:** the file is mode 600 and owned by the account that uses it.

```bash
stat -c '%A %U %n' ~/.config/tendril/pollinators/claude
```

Where the binary is reachable from that account, `tendril hardiness` reports the
same fact as a finding alongside the rest of the posture. A finding naming weak
permissions means the file is readable beyond its owner.

To rotate, revoke **before** issuing. `revoke` takes every active credential for
the Pollen, so issuing first would revoke the credential you had just written:

```bash
tendril pollinator list
tendril pollinator revoke --pollen claude
sudo -u tendril -i tendril pollinator issue --pollen claude --out /tmp/claude.cred
```

Requests presenting the revoked credential are denied at once; access tokens
already minted from it age out within their 15-minute cap.

> [!NOTE]
> The restricted `tendril-mcp` path uses only the selected named connection and
> ignores environment target and credential selectors. The full-binary
> `tendril mcp` path retains its current environment-based and in-process
> behavior.

## MCP config

### Governed installations

```json
{
  "mcpServers": {
    "opentendril": {
      "command": "tendril-mcp",
      "args": ["--connection", "remote"]
    }
  }
}
```

### Single-user installations

```json
{
  "mcpServers": {
    "opentendril": {
      "command": "tendril",
      "args": ["mcp"],
      "env": {
        "TENDRIL_POLLEN": "claude"
      }
    }
  }
}
```

If `tendril` is not on your `PATH`, replace `command` with the absolute path to
the binary you installed.

## Claude Desktop

Open the Claude Desktop MCP config file and paste the snippet that matches the
install shape into `mcpServers`.

Typical paths:

- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Linux: `~/.config/Claude/claude_desktop_config.json`

Restart Claude Desktop after saving.

## Cursor

Add the same JSON snippet to Cursor's MCP settings, then restart Cursor so it
reloads the server.

## Gemini

Use the same MCP block in Gemini's MCP config file. The repository's default
setup path is `~/.gemini/config/mcp_config.json`.

## Using the tools

Once connected, send work to the `default-workspace` Substrate using the
primary MCP identifiers. Grants remain dotted canonical operation-classes.

| Grant / Core | Primary MCP tool |
|---|---|
| `sprout.grow` | `sproutGrow` |
| `sequence.grow` | `sequenceGrow` |
| `git.status` | `gitStatus` |

Example `sproutGrow` call:

```json
{
  "transcript": "Update the parser to handle the new field.",
  "substrate": "default-workspace"
}
```

Example `sequenceGrow` call:

```json
{
  "pathOrName": "sequences/code-change.yaml",
  "substrate": "default-workspace"
}
```

On the local/private MCP surface, the eight compatibility aliases (`runSequence`,
`sproutTendril`, `createGenotype`, `viewGenome`, `reduceGenome`, `injectPlasmid`,
`graftSubstrate`, `promotePR`) remain callable. They are deprecated
compatibility behavior, not the recommended interface, and they carry no
independent authority. The public Pollinator projection does not list those
aliases; a public alias can resolve only to an already-allowed delegated Core
capability.

The Substrate entry keeps the credential with the Stem and lets the Ramet manage
the clone, the Terrarium and the push, without ever exposing the secret to the
Pollinator.
