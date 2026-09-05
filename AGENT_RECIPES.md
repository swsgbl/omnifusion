# Agent and CLI Recipes

Last updated: 2026-09-05

OmniFusion exposes one local gateway for Agent and coding-CLI workflows. The examples below use the supported `ofd connect` path, which reads the current port and gateway token from local configuration instead of asking you to copy a token into a public tutorial.

## Prepare once

```bash
ofd key add openrouter     # or another provider; input is hidden and encrypted on disk
ofd serve                  # listens on 127.0.0.1:20130 by default
ofd status                 # check the local gateway and configured providers
```

Keep the gateway terminal open, or use the desktop app. Every recipe below assumes that gateway is running.

## Claude Code

Claude Code uses the gateway's native Anthropic inbound protocol.

```bash
ofd connect claude --print   # preview the exact local config before writing
ofd connect claude           # merge env vars into Claude Code config and restart Claude
ofd disconnect claude        # remove only the two OmniFusion entries
```

For an MCP-only integration:

```bash
claude mcp add ofd -- ofd mcp
```

## Codex

Codex uses the gateway's Responses-compatible inbound protocol.

```bash
ofd connect codex --print    # preview config.toml changes
ofd connect codex            # add the provider block and OMNIFUSION_API_KEY
ofd disconnect codex         # remove the OmniFusion block and provider selection
```

`ofd connect` deliberately preserves unrelated Codex configuration. Review the `--print` output if you maintain multiple model providers.

## Gemini CLI

Gemini CLI uses the gateway's native Gemini inbound protocol through its dotenv override.

```bash
ofd connect gemini --print
ofd connect gemini
ofd disconnect gemini
```

Some Gemini CLI modes may use their own connection settings; the gateway remains available as a standard local OpenAI-compatible endpoint when that happens.

## OpenCode

```bash
ofd connect opencode --print
ofd connect opencode
ofd disconnect opencode
```

If OpenCode is configured through `OPENCODE_CONFIG` or `opencode.jsonc`, automatic JSON merging is intentionally refused. Use the manual snippet printed by `--print` so comments and existing configuration are not destroyed.

## Scope an MCP client

The MCP server can expose health, usage, route, and compression observations. Derive a scoped token when a client does not need full gateway access:

```bash
ofd mcp-token --scopes health,route
ofd mcp --gateway-url http://127.0.0.1:20130
```

Do not paste generated tokens into issues, screenshots, or public posts. Tokens are for the local client configuration only.

## Safety boundaries

- The default listener is loopback-only (`127.0.0.1`).
- Upstream provider keys are encrypted at rest; do not put them in `config.yaml`.
- `ofd disconnect <client>` creates a backup before connect/disconnect writes and removes only OmniFusion-owned fields where possible.
- Public tutorials should show commands, never real keys or tokens.
