# MCP Client Configuration Examples

This document provides example configurations for the Model Context Protocol (MCP) client, used by Pollinators to interact with an OpenTendril Stem. The `mcp-config.json` file in this directory contains a typical configuration for a governed installation.

## Governed Installations

For governed OpenTendril installations, the Pollinator-side client (`tendril-mcp`) is used to communicate with the Stem. The `mcp-config.json` file in this directory is configured for this scenario:

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

Name the credential file with `TENDRIL_POLLINATOR_CREDENTIAL` or `TENDRIL_MCP_CREDENTIAL` when the default path is not the one you want.

## Single-User Installations

For single-user installations, the `tendril mcp` command is used directly. An example configuration would look like this:

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

If `tendril` is not on your `PATH`, replace `command` with the absolute path to the binary you installed.

## Using the Tools

Once connected, send work to the `default-workspace` Substrate using the primary MCP identifiers. Grants still name the canonical operation-classes.

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

The eight compatibility aliases (`runSequence`, `sproutTendril`, `createGenotype`, `viewGenome`, `reduceGenome`, `injectPlasmid`, `graftSubstrate`, `promotePR`) remain callable. They are deprecated compatibility behavior, not the recommended interface, and they carry no independent authority.
