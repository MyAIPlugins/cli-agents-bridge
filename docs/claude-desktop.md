# Claude Desktop and the bridge

**macOS and Linux only.** On Windows `cab-bridge mcp` refuses to start: the exclusive identity lock it needs records its holder's process id inside the locked file, and the Windows lock does not let another process read it. Turning it on there is a design change, not a setting.

Claude Desktop has no shell on your machine: in Cowork the agent runs in Anthropic's cloud and reaches your computer only through tool calls, each capped at 60 seconds. It joins the bridge through `cab-bridge mcp`, a stdio MCP server that Desktop starts on the host.

## Install once, for every repository

Add one entry under `mcpServers` in `claude_desktop_config.json`, with the absolute path of the binary, then quit Desktop (Cmd-Q on macOS) and reopen it:

```json
"cab-bridge": {
  "command": "/absolute/path/to/cab-bridge",
  "args": ["mcp"]
}
```

The entry names no project. Desktop starts the server with the root directory as its working directory; which project it can reach is decided by the orchestrator (`val`) of that project, from its terminal:

```
cab-bridge desktop open     Open this project to Claude Desktop, for mcp_grant_ttl_minutes (480 by default)
cab-bridge desktop status   Is it open, until when, and has a Desktop session joined
cab-bridge desktop close    Close it now
```

## How Desktop takes part

Desktop joins with its `join` tool (without arguments it lists the projects open to it), appears in `peers` as role `architect`, and can talk **only to a val** of that project. Its role and rules are in the `arc-bridge` skill (`skills/claude-desktop/arc-bridge/`): zip that folder and upload it in Desktop under Customize > Skills. A message from anyone else, from another project, or after the bridge is closed is held back: Desktop is told that something is withheld and why, never what it says.

**Seven tools**: `join`, `next`, `ack`, `read`, `ask`, `tell`, `reply`. As in the command line, `next` never consumes: `ack` archives a tell or a response, `reply` closes an ask. `next` waits at most `mcp_next_max_wait_seconds` (40 by default) so that a call ends inside Cowork's 60-second cap. A message too large for one result (`mcp_max_result_bytes`, 24 KiB) arrives as a pointer line, and `read` returns it in pieces.

**One surface, many conversations.** Desktop starts one server per surface (chat, Cowork), and every conversation of that surface shares it: the joined project, the `architect` identity and the inbox are the same in all of them. An `ack` in one conversation removes the message from the others, and a message already shown in one arrives in another as a pointer, with a preview and `read`.

## Settings

The `mcp_*` keys of `config/default.json` can go in `config.json` or, as `CAB_MCP_*` variables, in the `env` of the Desktop entry. A `cab-bridge` older than this feature rejects unknown keys in `config.json` and then fails **every** command, so while an older binary is still on the machine, set them only in the Desktop entry.

## What leaves your machine

What travels to Anthropic and what stays on the host: [PRIVACY.md](../PRIVACY.md) and [SECURITY.md](../SECURITY.md).
