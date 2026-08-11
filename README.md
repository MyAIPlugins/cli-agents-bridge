# cli-agents-bridge

Robust multi-peer IPC bridge between CLI agent sessions (Claude Code, Codex, Aider, Cline, ...) running in separate VS Code windows.

Fork of [`PatilShreyas/claude-code-session-bridge`](https://github.com/PatilShreyas/claude-code-session-bridge) v0.1.0 (MIT) with 9 confirmed upstream bugs fixed structurally, role-based routing, namespace-isolated storage, security baseline, and a single Go binary distribution.

**Status**: v0.8.0 (shipped). See [CHANGELOG.md](./CHANGELOG.md) and [Releases](https://github.com/MyAIPlugins/cli-agents-bridge/releases) for details.

---

## What it is

A peer-to-peer message bus for AI coding agent sessions on the same machine. Two sessions register and exchange JSON messages via files in `~/.claude/cli-agents-bridge/sessions/<id>/{inbox,outbox}/`; each agent reads its inbox and responds from its live conversation context — no external API calls, no approximation. The common shape is an **orchestrator** and an **executor** (the built-in role names are `val` and `esc`), but **roles are free-form** — register any role name you like (see [Roles](#roles)).

Designed for an orchestrator ↔ executor workflow across separate windows, scaling to 1 orchestrator + N executors. The day-to-day protocol — how an agent waits, what a delivery means, when a message is closed — lives in the **`bridge-workflow`** skill this plugin bundles; see [The working loop](#the-working-loop).

---

## Install

**The bridge is a binary.** The plugin adds the skill and the `/cab` command, but it does **not** ship an executable — install the binary first, or nothing else here will work.

### 1. Get the binary

Download the archive for your OS/arch from the latest [Release](https://github.com/myAIPlugins/cli-agents-bridge/releases/latest):

```
VERSION=0.8.0     # the tag of the release you downloaded
OS=darwin         # darwin | linux
ARCH=arm64        # arm64 | amd64

tar -xzf "cab-bridge_${VERSION}_${OS}_${ARCH}.tar.gz"
mkdir -p ~/.local/bin
install -m 755 cab-bridge ~/.local/bin/
```

`mkdir -p` is not optional: `install` does not create its destination, and a fresh machine often has no `~/.local/bin` — without it the command fails with an error naming a temporary file you have never seen.

`~/.local/bin` must also be on your `PATH`. Verify the download against `checksums.txt`, published alongside the archives.

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

## Features

| Feature | cli-agents-bridge | Patil upstream |
|---|---|---|
| Heartbeat in wait loop | structural (goroutine + Ticker) | bug, never updated |
| Delivery semantics | `next` shows without consuming; `reply` archives one delivery, requeues the rest | delete on consume |
| Multi-peer role routing | hub-and-spoke val↔esc + --allow-mesh | no role field |
| Cross-project cleanup safety | scope=my-session default; `--scope=global` stays inside this project root and `--all-scopes` is opt-in. Retention purge is deliberately data-dir-wide and says so on stderr | global wipe by default |
| Session ID lookup | longest-prefix-match | first-found, non-deterministic |
| Lock on register | O_EXCL + stale recovery + --force-new; unique random IDs that never merge; collision detection vs a live owner | silent reuse, dup IDs sharing one inbox |
| Agent names | guaranteed addressable: a name survives the shell that carries it | n/a |
| Cross-repository addressing | `<name>@<project>`, val → val, with a shell-safe form of the token | n/a |
| Stderr discipline | errors→stderr, exit 124 on timeout | errors→stdout |
| Inbox audit trail | move-to-processed/ + retention | delete on consume |
| Distribution | single static Go binary | bash + jq runtime |
| Storage namespace | `~/.claude/cli-agents-bridge/` | shared with upstream |
| Security baseline | umask 077, perms 700/600, SC-7 base-dir integrity, session-ID regex validation | user defaults |
| Migration | `migrate-from-patil` subcommand | n/a |
| JSON validation | DisallowUnknownFields gateway + lenient runtime read | none |

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
└── sessions/<id>/
    ├── manifest.json             # schema v2 (role, agentName, pid)
    ├── inbox/*.json              # incoming messages (atomic write)
    ├── outbox/*.json
    ├── processed/                # audit trail post-consume
    └── lock                      # O_EXCL PID file
```

Storage namespace is **separate** from Patil upstream (`~/.claude/session-bridge/`) to eliminate cross-destructive cleanup risk.

For threat model + security controls see [SECURITY.md](./SECURITY.md). For GDPR / data flow see [PRIVACY.md](./PRIVACY.md).

---

## Roadmap

**Shipped** (detail in [CHANGELOG.md](./CHANGELOG.md)):

- **v0.2.0**: MVP — 9 upstream bugs fixed structurally, role routing, single binary, marketplace install
- **v0.2.1 → v0.2.4**: auto-gc of orphan sessions; observability + delivery receipts; prebuilt multi-OS binaries (GoReleaser) + public `bridge-workflow` skill; **automatic per-project isolation** (scope derived from the project root)
- **v0.5.x**: zero-config onboarding (`bootstrap`, scope = git repository, `overview`); id-free wake; symbolic references instead of raw message ids
- **v0.7.0**: feedback-driven hardening from real multi-agent runs
- **v0.8.0** (current): **the mailbox model** — five commands with no flags (`join`/`next`/`ask`/`tell`/`reply`); `next` never consumes and only `reply` archives, one delivery at a time, requeueing whatever arrived later; four explicit message states; cross-repository addressing `<name>@<project>` between orchestrators; agent names guaranteed to survive the shell that carries them

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

Forked from [`PatilShreyas/claude-code-session-bridge`](https://github.com/PatilShreyas/claude-code-session-bridge) (MIT) — full credit to Shreyas Patil for the original session-bridge design that this builds on.

---

## License

MIT — see [LICENSE](./LICENSE). Compatible with upstream Patil license.
