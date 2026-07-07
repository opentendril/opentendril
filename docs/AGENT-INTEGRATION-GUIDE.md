# Agent Integration Guide

OpenTendril is a secure Git proxy and sandbox for coding agents. The agent does
not receive a raw Git token. Instead, it talks to the OpenTendril MCP server,
and the Stem resolves the repository, authenticates with the host PAT, and runs
work inside an isolated Sprout container.

## Architecture

1. The agent connects to OpenTendril over MCP.
2. OpenTendril loads `~/.tendril/substrates.yaml`.
3. The substrate entry names the target repo and the environment variable that
   contains the PAT value.
4. OpenTendril clones, runs, and pushes on the host side while the agent stays
   inside the sandbox boundary.

The generated substrate is named `default-agent-workspace`. Use that substrate
when calling `sproutTendril` or `runSequence` for code changes.

## Bootstrap the config

Run:

```bash
tendril setup agent
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

Once connected, send work to the `default-agent-workspace` substrate.

Example `sproutTendril` call:

```json
{
  "transcript": "Update the parser to handle the new field.",
  "substrate": "default-agent-workspace"
}
```

Example `runSequence` call:

```json
{
  "sequence": "sequences/code-change.yaml",
  "substrate": "default-agent-workspace"
}
```

The substrate entry keeps auth on the host and lets OpenTendril manage the Git
clone, execution container, and push flow without exposing the secret to the
agent.
