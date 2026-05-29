# cli-agents-bridge

Robust multi-peer IPC bridge between CLI agent sessions (Claude Code, Codex, Aider, Cline, ...) running in separate VS Code windows.

Fork of [`PatilShreyas/claude-code-session-bridge`](https://github.com/PatilShreyas/claude-code-session-bridge) v0.1.0 (MIT) with 9 confirmed upstream bugs fixed structurally, role-based routing, namespace-isolated storage, security baseline, and a single Go binary distribution.

**Status**: v0.2.0 release candidate. See [CHANGELOG.md](./CHANGELOG.md) for full details.

---

## What it is

A peer-to-peer message bus for AI coding agent sessions on the same machine. One Claude Code window registers as VAL (planner/orchestrator), another as ESC (executor); they exchange JSON messages via files in `~/.claude/cli-agents-bridge/sessions/<id>/{inbox,outbox}/`. Each agent polls its inbox and responds from its live conversation context — no external API calls, no approximation.

Designed for the workflow triadic VAL ↔ ESC cross-VS-Code, with scaling to 1 VAL + N ESC.

---

## Quickstart (5 minutes)

### Install

From a Claude Code session:

```
/plugin marketplace add myAIPlugins/cli-agents-bridge
/plugin install cli-agents-bridge@cli-agents-bridge-marketplace
```

Verify:

```
/cli-agents-bridge:cab
# → cab-bridge --help output
```

### Register two peers

**Window 1 (VAL)**:

```
cab-bridge register --role=val --agent-name=VAL-main
# → JSON manifest with sessionId
```

**Window 2 (ESC)** in a different working directory (BUG-5 longest-prefix-match requires distinct paths):

```
cab-bridge register --role=esc --agent-name=ESC-main
```

### Exchange messages

**VAL sends a query**:

```
cab-bridge ask --to=<ESC-id> --content="implement feature X"
# → msg-abc123def456 (capture for receive)
```

**ESC listens** (long-poll, prints each message as JSON):

```
cab-bridge listen
```

**VAL waits for the reply**:

```
cab-bridge receive --msg-id=msg-abc123def456 --max-deadline=1800
# → JSON message body on stdout, exit 0
# → exit 124 if no reply within deadline
```

### Cleanup

```
cab-bridge cleanup                # own session only (default)
cab-bridge cleanup --scope=global # cross-project (interactive confirm)
```

---

## Features

| Feature | cli-agents-bridge | Patil upstream |
|---|---|---|
| Heartbeat in listen loop | structural (goroutine + Ticker) | bug, never updated |
| Receive timeout semantics | long-poll, late reply recoverable | strict-< loop, late reply lost |
| Multi-peer role routing | hub-and-spoke val↔esc + --allow-mesh | no role field |
| Cross-project cleanup safety | scope=my-session default | global wipe by default |
| Session ID lookup | longest-prefix-match | first-found, non-deterministic |
| Lock on register | O_EXCL + stale recovery + --force-new; unique random IDs that never merge; collision detection vs a live `listen` owner (best-effort, see troubleshooting) | silent reuse, dup IDs sharing one inbox |
| Stderr discipline | errors→stderr, exit 124 on timeout | errors→stdout |
| Inbox audit trail | move-to-processed/ + retention | delete on consume |
| Distribution | single static Go binary | bash + jq runtime |
| Storage namespace | `~/.claude/cli-agents-bridge/` | shared with upstream |
| Security baseline | umask 077, perms 700/600, SC-7 base-dir integrity, session-ID regex validation | user defaults |
| Migration | `migrate-from-patil` subcommand | n/a |
| JSON validation | DisallowUnknownFields gateway + lenient runtime read | none |

---

## Subcommands

```
cab-bridge register             Register a new session for the current project
cab-bridge listen               Poll inbox emitting messages as JSON
cab-bridge ask                  Send a message to a peer
cab-bridge connect <peer-id>    Refresh own heartbeat + validate peer reachable
cab-bridge receive              Long-poll wait for a reply
cab-bridge peers                List known peers (table or --json)
cab-bridge cleanup              Cleanup own session (or --scope=global)
cab-bridge status               Show own session status
cab-bridge inspect <id>         Print session manifest JSON
cab-bridge migrate-from-patil   Migrate upstream Patil sessions to v2 namespace
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

Storage namespace is **separate** from Patil upstream (`~/.claude/session-bridge/`) to eliminate cross-distructive cleanup risk.

For threat model + security controls see [SECURITY.md](./SECURITY.md). For GDPR / data flow see [PRIVACY.md](./PRIVACY.md).

---

## Roadmap

- **v0.2.0** (current): MVP feature-complete, 9 BUG fixes, single binary, marketplace install
- **v0.3.0**: notifications (osascript/notify-send), transcript log, thread view, retry built-in
- **v0.4.0**: Unix socket daemon (gated by empirical latency >200ms ∧ peer count >3)
- **v1.0.0**: Anthropic marketplace, encryption opt-in, multi-machine via Tailscale

Full plan in [PLAN.md](./PLAN.md).

---

## Contributing

See [docs/dev-conventions.md](./docs/dev-conventions.md) for Go style, commit format, and test patterns.

---

## Authors & credits

- **Idea, direction & review** — [Alan Curtis](https://www.alancurtisagency.com), AC Agency
- **Implementation** — Claude Opus 4.7 & 4.8 (Anthropic)

Built as a triadic VAL/ESC workflow: Alan drove the vision and gated every plan and commit; Claude designed and wrote the code. *The idea is Alan's, the code is Claude's.*

Forked from [`PatilShreyas/claude-code-session-bridge`](https://github.com/PatilShreyas/claude-code-session-bridge) (MIT) — full credit to Shreyas Patil for the original session-bridge design that this builds on.

---

## License

MIT — see [LICENSE](./LICENSE). Compatible with upstream Patil license.
