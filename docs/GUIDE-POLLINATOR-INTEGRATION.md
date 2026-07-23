# Pollinator Integration Guide

OpenTendril is a governed git path and isolation boundary for **Pollinators** —
external requesters, human or Mycorrhizal, that reach in and ask the organism
for work. A Pollinator never receives a raw git token. It talks to this Ramet's
Model Context Protocol server, and the Stem resolves the Substrate,
authenticates with the credential it holds, and runs the work inside an isolated
Sprout within a Terrarium.

## Architecture

1. The Pollinator connects to the Ramet over the Model Context Protocol.
2. The Stem loads `~/.tendril/substrates.yaml`.
3. The Substrate entry names the target repository and the environment variable
   holding the Personal Access Token value.
4. The Stem clones, runs and pushes on its own side, while the Sprout doing the
   work stays sealed inside its Terrarium.

The generated Substrate is named `default-workspace`. Use it when calling
`sproutTendril` or `runSequence` for code changes.

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

## MCP config snippet

The setup command emits a JSON block like this:

```json
{
  "mcpServers": {
    "opentendril": {
      "command": "tendril",
      "args": ["serve", "mcp", "stdio"]
    }
  }
}
```

If `tendril` is not on your `PATH`, replace `command` with the absolute path to
the binary you installed.

## Claude Desktop

Open the Claude Desktop MCP config file and paste the snippet into
`mcpServers`.

Typical paths:

- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Linux: `~/.config/Claude/claude_desktop_config.json`

Restart Claude Desktop after saving.

## Cursor

Add the same JSON snippet to Cursor's MCP settings. Point its MCP server entry
at `tendril serve mcp stdio`, then restart Cursor so it reloads the server.

## Gemini

Use the same MCP block in Gemini's MCP config file. The repository's default
setup path is `~/.gemini/config/mcp_config.json`.

## Using the tools

Once connected, send work to the `default-workspace` Substrate.

Example `sproutTendril` call:

```json
{
  "transcript": "Update the parser to handle the new field.",
  "substrate": "default-workspace"
}
```

Example `runSequence` call:

```json
{
  "sequence": "sequences/code-change.yaml",
  "substrate": "default-workspace"
}
```

The Substrate entry keeps the credential with the Stem and lets the Ramet manage
the clone, the Terrarium and the push, without ever exposing the secret to the
Pollinator.
