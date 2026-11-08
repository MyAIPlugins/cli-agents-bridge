# Customizing the roles

The skills in `skills/` work as they are: each one needs only the `cab-bridge` binary and the runtime it is written for (Claude Code, Codex, Antigravity, Claude Desktop). Nothing in them requires a plugin, an MCP server or a subagent.

They are also meant as a base. A team that has more tools can give them to the roles that benefit from them. This page says how to do that without losing the protections `make install-skills` gives you.

## Keep your version next to the generic one, not on top of it

`make install-skills` refuses to overwrite an installed skill that was edited by hand. So editing the installed copy works once, and then every update of the repository stops on it.

What works better:

1. Copy the skill you want to adapt into a folder of your own, outside the repository or in a git-ignored folder inside it.
2. Edit it there, and install it from there.
3. When the repository's version changes, compare it with yours and carry over what applies.

Write your additions as additions: a section "Tools in this environment" at the end is easier to keep up to date than changes scattered through the text.

## What is worth adding, by role

| role | addition | when it pays off |
|---|---|---|
| val (orchestrator) | a subagent that runs build, tests and lint in one worktree and returns only the outcome | the gate output is long and would fill the orchestrator's context |
| val, esc | a subagent that fetches the documentation of one library at an exact version | before calling an API not verified in the session, or adding a dependency |
| val | a web research subagent that returns facts with URL and date | decisions that depend on prices, policies or regulations |
| esc, critic | a code navigation tool (language server based) | callers, references, renames on a large codebase |
| browser tester | a browser automation MCP server, started with an isolated profile | measuring a page as an anonymous visitor sees it |
| architect (Claude Desktop) | a knowledge base connector, read-only | the team keeps curated patterns somewhere searchable |

## Rules that keep additions safe

- **One level of subagents.** The member of the team delegates; subagents do not delegate further. In Claude Code, `CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH=1` in the `env` block of `settings.json` enforces it. To cover several things at once, launch several instances in the same message, each on a separate perimeter.
- **Never two instances on the same worktree or on the same browser.** Build and test share output folders; two agents driving one browser steal each other's pages.
- **Pin versions** of every MCP server and tool, and prefer releases older than 14 days.
- **Turn telemetry off** where the tool offers it, and check what a tool sends before pointing it at a client's code: what a tool returns goes to the model provider, and some tools send data of their own.
- **Say what a role can use.** A skill that promises a tool the runtime does not have gets believed. If a role runs on two runtimes, keep the runtime-specific tools in the version for that runtime.
