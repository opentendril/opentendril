# Pollinator Integration Guide

OpenTendril is a governed git path and isolation boundary for **Pollinators** —
external requesters, human or Mycorrhizal, that reach in and ask the organism
for work. A Pollinator never receives a raw git token. It talks to this Ramet
over the Model Context Protocol. The Stem resolves the Substrate, authenticates
with the credential it holds, and runs the work inside an isolated Sprout
within a Terrarium. The Substrate entry names the target repository and the
environment variable holding the Personal Access Token value. The Stem clones,
runs and pushes on its own side, while the Sprout doing the work stays sealed
inside its Terrarium.

## Architecture

A governed installation and a single-user installation use different MCP
executables.

**Governed installation**

```text
Pollinator
    -> tendril-mcp
    -> durable Pollinator root
    -> short-lived access token
    -> governed Stem
    -> governed capabilities
```

`tendril-mcp` is the Pollinator-side client. It holds that Pollinator's durable
root, mints short-lived access tokens, and forwards MCP frames to the separately
owned Stem. Authorization and Pollen derivation stay at the Stem. The client
cannot construct a Stem and has no in-process mode. Do not launch the protected
`tendril` binary from a governed Pollinator account.

**Single-user installation**

`tendril mcp` is the supported single-user stdio command. It is a governed
command surface and stdio bridge that may run in-process as the operator, or
forward to another-user Stem when a credential and reachable Stem are present.

`tendril setup substrate` writes `~/.tendril/substrates.yaml` and prints a
single-user MCP snippet for `tendril mcp`. That helper is not the governed
Pollinator install path.

The generated Substrate is named `default-workspace`. Use it when calling
`sproutGrow` or `sequenceGrow` for code changes. Grants still name the
canonical operation-classes `sprout.grow` and `sequence.grow`.

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
> **Two kinds of consumer, one location.** The path above is a convention. What
> reads it depends on which surface the Pollinator speaks.
>
> **MCP credential lookup.** `TENDRIL_POLLINATOR_CREDENTIAL` names the file
> directly and takes precedence; `TENDRIL_MCP_CREDENTIAL` is honoured next.
> With neither set the path defaults to `~/.config/tendril/pollinators/<pollen>`,
> where `<pollen>` comes from `TENDRIL_POLLEN`. A missing file at the default
> path is the ordinary unconfigured case and is not an error; a missing file at
> a path you named explicitly is.
>
> `TENDRIL_POLLEN` selects the default credential file. On a forwarded or
> governed connection, the Stem derives Pollen from the presented credential;
> the variable does not override that authorization identity. On the in-process
> `tendril mcp` path, `TENDRIL_POLLEN` also binds the session Pollen used for
> grant lookup.
>
> **Where your own process is the client — REST.** None of those variables apply.
> Your client reads the file itself, presents the credential to
> `POST /v1/pollinator/token` to obtain an access token, and carries that token on
> data routes. Follow the location above so `hardiness` can audit the file; no Stem
> code path reads it on your behalf.

## MCP config

### Governed installations

```json
{
  "mcpServers": {
    "opentendril": {
      "command": "tendril-mcp",
      "env": {
        "TENDRIL_POLLEN": "claude"
      }
    }
  }
}
```

Name the credential file with `TENDRIL_POLLINATOR_CREDENTIAL` or
`TENDRIL_MCP_CREDENTIAL` when the default path is not the one you want.

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

The eight compatibility aliases (`runSequence`, `sproutTendril`,
`createGenotype`, `viewGenome`, `reduceGenome`, `injectPlasmid`,
`graftSubstrate`, `promotePR`) remain callable. They are deprecated
compatibility behavior, not the recommended interface, and they carry no
independent authority.

The Substrate entry keeps the credential with the Stem and lets the Ramet manage
the clone, the Terrarium and the push, without ever exposing the secret to the
Pollinator.
