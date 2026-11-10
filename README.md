# cli-agents-bridge

Robust multi-peer IPC bridge between CLI agent sessions (Claude Code, Codex, Aider, Cline, ...) running in separate terminals or editor windows on the same machine.

A single Go binary with role-based routing, a mailbox where nothing is consumed by accident, an isolated storage namespace and a security baseline. The basic idea came from [`PatilShreyas/claude-code-session-bridge`](https://github.com/PatilShreyas/claude-code-session-bridge) (see [Authors & credits](#authors--credits)).

**Status**: v0.11.0 (shipped). See [CHANGELOG.md](./CHANGELOG.md) and [Releases](https://github.com/MyAIPlugins/cli-agents-bridge/releases) for details.

---

## What it is

A peer-to-peer message bus for AI coding agent sessions on the same machine. Two sessions register and exchange JSON messages via files in `~/.claude/cli-agents-bridge/sessions/<id>/{inbox,outbox}/`; each agent reads its inbox and responds from its live conversation context — no external API calls, no approximation. The common shape is an **orchestrator** and an **executor** (the built-in role names are `val` and `esc`), but **roles are free-form** — register any role name you like (see [Roles](#roles)).

Designed for an orchestrator ↔ executor workflow across separate windows, scaling to 1 orchestrator + N executors. The day-to-day protocol — how an agent waits, what a delivery means, when a message is closed — lives in the **`bridge-workflow`** skill this plugin bundles; see [The working loop](#the-working-loop).

---

## Install

**The bridge is a binary.** The plugin adds the skill and the `/cab` command, but it does **not** ship an executable — install the binary first, or nothing else here will work.

### 1. Get the binary

Download the archive for your OS/arch from the latest [Release](https://github.com/myAIPlugins/cli-agents-bridge/releases/latest).

#### macOS and Linux

```
VERSION=PUT_THE_VERSION_HERE   # the version WITHOUT the leading v (the tag carries it, the archive name does not)
OS=darwin         # darwin | linux
ARCH=arm64        # arm64 | amd64

tar -xzf "cab-bridge_${VERSION}_${OS}_${ARCH}.tar.gz"
mkdir -p ~/.local/bin
install -m 755 cab-bridge ~/.local/bin/
```

`mkdir -p` is not optional: `install` does not create its destination, and a fresh machine often has no `~/.local/bin` — without it the command fails with an error naming a temporary file you have never seen.

`~/.local/bin` must also be on your `PATH`.


#### Windows

The Windows archive is a **zip**, and everything below is built into PowerShell — nothing to install
first.

```powershell
$Version = 'PUT_THE_VERSION_HERE'   # the version WITHOUT the leading v (the tag carries it, the archive name does not)
$Zip     = "cab-bridge_${Version}_windows_amd64.zip"

# Same check, same limit as above: this proves the file arrived intact, not that it is genuine.
Get-FileHash $Zip -Algorithm SHA256

Expand-Archive $Zip -DestinationPath .\cab-bridge -Force
New-Item -ItemType Directory -Force "$env:LOCALAPPDATA\Programs\cab-bridge" | Out-Null
Copy-Item .\cab-bridge\cab-bridge.exe "$env:LOCALAPPDATA\Programs\cab-bridge\" -Force
```

Then put that directory on your **user** PATH — and the first line matters more than the second:

```powershell
# Read the CURRENT user PATH and append. Do NOT write a bare value here: `setx PATH "...new..."`
# replaces everything the user already had, and that loss is silent and permanent.
$dir = "$env:LOCALAPPDATA\Programs\cab-bridge"
$current = [Environment]::GetEnvironmentVariable('Path', 'User')
if ($current -notlike "*$dir*") {
  [Environment]::SetEnvironmentVariable('Path', "$current;$dir", 'User')
}
```

**Open a new terminal** — a running one keeps the environment it started with, so the command will
look missing in the window you just typed that in. Then:

```powershell
cab-bridge version
```

Only `amd64` is published for Windows. There is no `arm64` build: Go would produce one, but nobody
here has a machine to run it on, and an untested binary is worse than an absent one — build from
source if you need it.

Verify the download against `checksums.txt`, published alongside the archives. Be clear about what that proves: the archive reached you **intact** — no truncated download, no corrupted mirror. It does **not** prove the release is **genuine**, because `checksums.txt` ships from the same place as the archive, so a tampered release would carry matching sums. There is no signature yet.

Or build from source (Go 1.25+):

```
make build          # -> bin/cab-bridge (version from `git describe`)
make install-dev    # symlink it into ~/.local/bin
```

### 2. Check it runs

```
cab-bridge version
```

This must print a version before you go further. If it prints `command not found`, `~/.local/bin` is not on your `PATH` — fix that first.

### 3. Add the plugin (optional, recommended)

From a Claude Code session:

```
/plugin marketplace add myAIPlugins/cli-agents-bridge
/plugin install cli-agents-bridge@cli-agents-bridge-marketplace
```

This installs the `bridge-workflow` skill and the `/cli-agents-bridge:cab` command. Both call the binary you installed in step 1 — the plugin alone is not enough.

### 4. Install the agent skills (optional, from a clone, macOS and Linux)

`skills/` holds the role skills for each runtime: the VAL and critic skills for Claude Code and Codex, the security reviewer (`security-expert`) and the team's rubrics (`team-rubrics`) that both load, the Antigravity ones, and `arc-bridge` for Claude Desktop. A skill that Claude Code and Codex both load lives once, in `skills/shared/<name>/`, and is installed in both; the same name in `skills/shared/` and in `skills/claude/` or `skills/codex/` is refused before anything is copied. From a clone of this repository:

```
make skills-check      # what differs between the repository and your installed copies
make install-skills    # install them into ~/.claude/skills, ~/.agents/skills (Codex), ~/.gemini/config/skills
```

`install-skills` touches only the skills the repository has. It refuses to overwrite a copy that was edited by hand unless you pass `FORCE=1`, and it never deletes files you added or writes through a symbolic link. A copy that is only behind the repository is updated without `FORCE`. Claude Desktop takes its skill as a zip: `install-skills` builds `bin/arc-bridge.zip`, and you upload it under Customize > Skills.

Codex loads a user's skills from `~/.agents/skills` and, for now, also reads the older `~/.codex/skills`, so a skill found in both is listed twice. If a skill of this repository is also in `~/.codex/skills`, as a folder of its own, a link to one, or a link to nothing, `skills-check` reports it and `install-skills` copies nothing, even with `FORCE=1`, and names each path to clear by hand; nothing there is moved or deleted for you. The same folder reached through a link is not a second copy. Both commands need a `sh` whose `test` has a working `-ef`, as dash, bash, ksh and zsh do, and stop before reading anything if it does not. On Windows, where these commands do not run, copy each folder by hand into the one your runtime loads skills from (for Codex, `.agents/skills` in your home, which we have not tested on Windows).

The skills need nothing but the binary. To give the roles more tools (subagents, MCP servers, a knowledge base) without losing those protections, see [docs/customizing-roles.md](./docs/customizing-roles.md).

---

## Quickstart

Two agents, two windows, two different working directories in the same git repository.

**Window 1** — the orchestrator:

```
cab-bridge join --role=val --agent-name=VAL-main
```

`join` registers (idempotently) and prints who else is here. Then wait for mail:

```
cab-bridge next
```

`next` blocks until something arrives, prints it, and exits. Run it again to keep listening.

**Window 2** — the executor, from a different directory in the same repo:

```
cab-bridge join --role=esc --agent-name=ESC-main
cab-bridge next
```

**Window 1** — ask something:

```
cab-bridge ask ESC-main "implement feature X"
```

Window 2's `next` returns with the message. The executor works, then answers:

```
cab-bridge reply "done — see the diff on branch feat/x"
```

Long messages come from stdin instead of an argument:

```
cab-bridge tell VAL-main < report.md
```

### Cleanup

```
cab-bridge cleanup                     # own session only (default)
cab-bridge cleanup --scope=global      # every stale session in THIS project root
cab-bridge cleanup --scope=global --all-scopes   # every project sharing the data dir
```

The two `--scope=global` forms **ask for confirmation** and exit non-zero if you decline — add `--force` to run them unattended, and read what they name before you do.

Any cleanup also applies the retention policy, which spans the **whole** data dir by design: session removal is scoped, retention is not.

---

## The working loop

Five commands, no flags: `join` once, then `next` forever, with `ask` / `tell` / `reply` to talk.

The rules that make it reliable are not in this README on purpose — they are in the **`bridge-workflow`** skill, which ships with the plugin and is the single source for them:

- **`next` never consumes.** Being woken and consuming are separate acts, so no process can eat another agent's mail.
- **Only `reply` archives**, and it closes one delivery — anything that arrived later goes back in the queue and is handed to you again, marked `redelivered`.
- **Four explicit states** — `UNREAD`, `NOTIFIED`, `REQUEUED`, `ARCHIVED` — and exactly one command moves a file.
- **Recipients are agent names**; `<name>@<project>` reaches another repository.

If you are an agent reading this: load that skill rather than inferring the protocol from here.

---

## Roles

`join --role=<name>` and `register --role=<name>` accept **any** role. Built-ins: `val` (orchestrator), `esc` (executor), `critic` (reviewer), `observer` (read-only), `neutral` — plus `architect`, reserved for Claude Desktop over MCP. Routing is permissive: custom roles (`planner`, `coder`, `peer`, ...) are accepted as-is. Two structural rules:

- `observer` cannot send (read-only sink) — no flag overrides this.
- `esc → esc` is rejected by default (route through the orchestrator); pass `--allow-mesh` to override.

Two equal agents with no hierarchy can both register `--role=peer` — `peer ↔ peer` is allowed out of the box.

A pair in the same repo isolates itself **automatically**: the scope is derived from the git repository root, so an orchestrator at the root and an executor in a linked worktree pair up with no flags, and `peers` shows only the current project. Different repositories do not see each other by default — `peers --all-scopes` lists them and `<name>@<project>` addresses them (val → val only). For special cases (two pairs in one repo) use `--team=<name>`, or a separate `CAB_DATA_DIR` to isolate storage entirely.

---

## Claude Desktop

Claude Desktop has no shell on your machine: in Cowork the agent runs in Anthropic's cloud and reaches the Mac only through tool calls, each capped at 60 seconds. It joins the bridge through `cab-bridge mcp`, a stdio MCP server that Desktop starts on the host. **macOS and Linux only**: on Windows `cab-bridge mcp` refuses to start, because the exclusive identity lock it needs is not available there.

**Install once, for every repository.** Add one entry under `mcpServers` in `claude_desktop_config.json`, with the absolute path of the binary, then quit Desktop (Cmd-Q) and reopen it:

```json
"cab-bridge": {
  "command": "/absolute/path/to/cab-bridge",
  "args": ["mcp"]
}
```

The entry names no project. Desktop starts the server with cwd `/`; which project it can reach is decided by the val of that project, from its terminal:

```
cab-bridge desktop open     Open this project to Claude Desktop, for mcp_grant_ttl_minutes (480 by default)
cab-bridge desktop status   Is it open, until when, and has a Desktop session joined
cab-bridge desktop close    Close it now
```

Desktop joins with its `join` tool (without arguments it lists the projects open to it), appears in `peers` as role `architect`, and can talk **only to a val** of that project. Its role and rules are in the `arc-bridge` skill (`skills/claude-desktop/arc-bridge/`): zip that folder and upload it in Desktop under Customize > Skills. A message from anyone else, from another project, or after the bridge is closed is held back: Desktop is told that something is withheld and why, never what it says.

**Seven tools**: `join`, `next`, `ack`, `read`, `ask`, `tell`, `reply`. As in the CLI, `next` never consumes: `ack` archives a tell or a response, `reply` closes an ask. `next` waits at most `mcp_next_max_wait_seconds` (40 by default) so that a call ends inside Cowork's 60-second cap. A message too large for one result (`mcp_max_result_bytes`, 24 KiB) arrives as a pointer line, and `read` returns it in pieces.

**One surface, many conversations.** Desktop starts one server per surface (chat, Cowork), and every conversation of that surface shares it: the joined project, the `architect` identity and the inbox are the same in all of them. An `ack` in one conversation removes the message from the others, and a message already shown in one arrives in another as a pointer, with a preview and `read`.

**Settings.** The `mcp_*` keys of `config/default.json` can go in `config.json` or, as `CAB_MCP_*` variables, in the `env` of the Desktop entry. A `cab-bridge` older than this feature rejects unknown keys in `config.json` and then fails **every** command, so while an older binary is still on the machine, set them only in the Desktop entry.

What travels to Anthropic and what stays on the host: [PRIVACY.md](./PRIVACY.md) and [SECURITY.md](./SECURITY.md).

---

## Features

| Feature | How |
|---|---|
| Heartbeat in wait loop | structural (goroutine + Ticker) |
| Delivery semantics | `next` shows without consuming; `reply` archives one delivery, requeues the rest |
| Multi-peer role routing | hub-and-spoke val↔esc + --allow-mesh |
| Cross-project cleanup safety | scope=my-session default; `--scope=global` stays inside this project root and `--all-scopes` is opt-in. Retention purge is deliberately data-dir-wide and says so on stderr |
| Session ID lookup | longest-prefix-match |
| Lock on register | a kernel lock (`flock`, `LockFileEx`) the OS releases when the holder dies; --force-new; unique random IDs that never merge; collision detection vs a live owner |
| Agent names | guaranteed addressable: a name survives the shell that carries it |
| Cross-repository addressing | `<name>@<project>`, val → val, with a shell-safe form of the token |
| Stderr discipline | errors→stderr, exit 124 on timeout |
| Inbox audit trail | move-to-processed/ + retention |
| Distribution | single static Go binary, no runtime dependencies |
| Storage namespace | `~/.claude/cli-agents-bridge/`, separate from the session-bridge plugin's `~/.claude/session-bridge/` |
| Security baseline | umask 077, perms 700/600, SC-7 base-dir integrity, session-ID regex validation |
| Migration | `migrate-from-patil` imports the sessions of the session-bridge plugin |
| JSON validation | DisallowUnknownFields gateway + lenient runtime read |

---

## Subcommands

The working loop:

```
cab-bridge join --role=<role>   Once, at the start: register (idempotent) and see who is here
cab-bridge next                 Then forever: deliver whatever arrived, waiting until it does
cab-bridge ask <agent> ["msg"]  Ask something — stays open until they reply
cab-bridge tell <agent> ["msg"] Inform — no reply expected
cab-bridge reply ["msg"]        Answer whoever asked; closes the asks that came in together
```

With no message argument the text is read from stdin: `tell VAL-x < report.md`

Service — inspection and maintenance, never in the loop:

```
cab-bridge read <msg-id>        Re-read a message, including an archived one
cab-bridge sent                 What I sent and what state it is in for the recipient
cab-bridge peers                Who exists (table, or --json / --all-scopes)
cab-bridge overview             Me, my peers and my inbox at a glance
cab-bridge whoami               This session's identity
cab-bridge state <value>        Declare what I am doing: idle | working | done | orchestrating
cab-bridge status               Own session counters
cab-bridge inbox --list|--tidy  Inspect without consuming, or archive what is handled
cab-bridge register             Lower-level registration (join is the way in)
cab-bridge connect <peer-id>    Refresh own heartbeat + check a peer is reachable
cab-bridge notify-watch -- <hook>  Run <hook> when mail arrives, for peers with no native push
cab-bridge desktop open|close|status  Open this project to Claude Desktop, close it, or say if it is open (a val runs it)
cab-bridge mcp                  The MCP server Claude Desktop starts (see "Claude Desktop"); not run by hand
cab-bridge findings add|stale   The team's findings registry, .handover/findings.tsv: add one row (JSON on stdin), list the open ones
cab-bridge cleanup              Remove own session, or --scope=global
cab-bridge inspect <id>         Print a session manifest
cab-bridge migrate-from-patil   Migrate ~/.claude/session-bridge/ sessions to the v2 namespace
cab-bridge version              Show version
```

Each subcommand prints its own `--help`.

---

## Architecture

Single Go binary, filesystem-based IPC. No external runtime dependencies (no jq, no Python).

```
~/.claude/cli-agents-bridge/
├── config.json                   # optional CAB_* env vars override
└── layout-2/sessions/<id>/       # this version shares no file with older ones
    ├── manifest.json             # schema v2 (role, agentName, pid)
    ├── inbox/*.json              # incoming messages (atomic write)
    ├── outbox/*.json
    ├── processed/                # audit trail post-consume
    └── lock                      # empty file, held with a kernel lock
```

A command waits for a busy session lock for `session_lock_wait_ms` (env `CAB_SESSION_LOCK_WAIT_MS`, default 5000), then fails with an error that says so.

Storage namespace is **separate** from the session-bridge plugin's directory (`~/.claude/session-bridge/`), so neither can clean up the other's sessions.

For threat model + security controls see [SECURITY.md](./SECURITY.md). For GDPR / data flow see [PRIVACY.md](./PRIVACY.md).

---

## Roadmap

**Shipped** (detail in [CHANGELOG.md](./CHANGELOG.md)):

- **v0.2.0**: MVP — 9 upstream bugs fixed structurally, role routing, single binary, marketplace install
- **v0.2.1 → v0.2.4**: auto-gc of orphan sessions; observability + delivery receipts; prebuilt multi-OS binaries (GoReleaser) + public `bridge-workflow` skill; **automatic per-project isolation** (scope derived from the project root)
- **v0.5.x**: zero-config onboarding (`bootstrap`, scope = git repository, `overview`); id-free wake; symbolic references instead of raw message ids
- **v0.7.0**: feedback-driven hardening from real multi-agent runs
- **v0.8.0**: **the mailbox model** — five commands with no flags (`join`/`next`/`ask`/`tell`/`reply`); `next` never consumes and only `reply` archives, one delivery at a time, requeueing whatever arrived later; four explicit message states; cross-repository addressing `<name>@<project>` between orchestrators
- **v0.9.0**: **the token surface** — an agent-name grammar (one shell word, rejected otherwise, derived reversibly from a directory name); every path and address the bridge prints is rendered as one shell argument by the tool (`peers` `SCOPE` column, `next`'s `fromAddressShellArg`, every remediation) instead of asking the reader to quote it; `register --resume` resumes by identity, not by name; lock errors state facts and no remedy; this README rewritten around the binary and validated black-box; Makefile and CI made as strict as the local gate
- **v0.10.0**: **three platforms** — Windows joins macOS and Linux, with a fifth release archive (`cab-bridge_<version>_windows_amd64.zip`) and a PowerShell install path that was executed rather than written; `windows/arm64` is excluded on purpose, because nobody here can run that artifact. The gate runs on `windows-latest` beside `ubuntu-latest`, and `vet` + `staticcheck` run **once per GOOS** — five seconds that closed a hole as old as the repository, since build constraints hide per-OS files from the analyser. Three product defects came out of that gate: a qualified address that could not resolve on Windows (F-135), a rename that was not atomic there (F-133), and a cross-device branch whose comment promised what the code never did (F-137). Releases are now created as **drafts**: a tag prepares a release instead of publishing one
- **v0.11.0** (current): **Claude Desktop joins the bridge** — from Cowork, through `cab-bridge mcp`, opened per project by the val with `cab-bridge desktop open` and judged message by message (see [Claude Desktop](#claude-desktop)). The agent skills are in English, one copy in `skills/`, installed with `make install-skills`. `next`'s JSON `bytes` now counts the text, not the file.

**Next**: attestation of the installed copy against the versioned source, and a black-box check that a fresh agent following this README ends up with a working bridge.

**Gated** (only if the need is measured): a Unix-socket daemon (only if filesystem-polling latency exceeds ~200ms in real long runs **and** concurrent peers exceed 3); v1.0 (Anthropic marketplace, opt-in encryption, multi-machine via Tailscale) after sustained real use.

Design rationale in [PLAN.md](./PLAN.md).

---

## Contributing

See [docs/dev-conventions.md](./docs/dev-conventions.md) for Go style, commit format, and test patterns.

---

## Authors & credits

- **Idea, direction & review** — [Alan Curtis](https://www.alancurtisagency.com), AC Agency
- **Implementation** — Claude Opus 4.7, 4.8 & 5 (Anthropic)

Built as a triadic VAL/ESC workflow: Alan drove the vision and gated every plan and commit; Claude designed and wrote the code. *The idea is Alan's, the code is Claude's.*

The basic idea, two Claude Code sessions talking through files, came from Shreyas Patil's [`PatilShreyas/claude-code-session-bridge`](https://github.com/PatilShreyas/claude-code-session-bridge) (MIT). cli-agents-bridge is a new codebase, written in Go, with its own design.

---

## License

MIT, see [LICENSE](./LICENSE).
