# Design & Plan — cli-agents-bridge

Design rationale and roadmap for `cli-agents-bridge`, a Go fork of [`PatilShreyas/claude-code-session-bridge`](https://github.com/PatilShreyas/claude-code-session-bridge) v0.1.0 (MIT). For usage see [README](./README.md); for the threat model see [SECURITY.md](./SECURITY.md).

> This is a public design summary. The original ratified planning document (with full decision audit trail, in Italian) is kept internally and is not published.

---

## What it is, and why a rewrite

A peer-to-peer message bus for AI coding-agent sessions on the same machine. Sessions register and exchange JSON messages through files under `~/.claude/cli-agents-bridge/sessions/<id>/{inbox,outbox,processed}/` — no network, no API calls. The common shape is an **orchestrator + executor** (default roles `val`/`esc`, but roles are free-form).

The upstream plugin is bash + jq. Auditing the source surfaced **9 confirmed bugs** rooted in a single structural gap (below) that point fixes can't close. The fork is a **Go rewrite** chosen for:

- **Single static binary** (`CGO_ENABLED=0`, cross-compiled darwin/linux × amd64/arm64) — no jq/Python/Node runtime.
- **Type safety + the Go 1 compatibility promise** — maintainable years later, recompilable as-is.
- **Native concurrency** — heartbeat as a goroutine with `context.Context` cancellation, instead of an external scheduler that upstream assumes but never wires.
- **Namespace isolation** (`~/.claude/cli-agents-bridge/`, separate from upstream `~/.claude/session-bridge/`) — eliminates the cross-destructive cleanup risk of sharing a directory with a plugin that has the unfixed cleanup bug.

---

## Upstream bugs fixed

All confirmed against `PatilShreyas/claude-code-session-bridge` @ `8d0816b` (tag `v0.1.0`).

| ID | Severity | Issue | Fix |
|---|---|---|---|
| BUG-1 | critical | Heartbeat never updated in the listen loop | Heartbeat goroutine (`time.Ticker` + atomic manifest update) |
| BUG-2 | critical | `receive` hard timeout loses a late reply | Long-poll to `--max-deadline`; non-matching messages preserved in inbox |
| BUG-3 | critical | Multi-peer routing with no role field | Role-based `ValidateSendPair` (hub-and-spoke + `--allow-mesh`) |
| BUG-4 | critical | `cleanup` is global/cross-project by default | `--scope=my-session` default; `--scope=global` needs confirmation |
| BUG-5 | critical | Session lookup parent-fallback is non-deterministic | Longest-prefix-match by cwd |
| BUG-6 | medium | Session-ID collision per cwd, silent inbox sharing | `O_EXCL` PID lock + stale recovery + `--force-new`; unique IDs |
| BUG-7 | high | `receive` writes errors to stdout (pollutes capture) | Errors to stderr, exit 124 on timeout |
| BUG-8 | high | `STALE_SECONDS` inconsistent (peers 300s vs cleanup 1800s) | Single config source of truth |
| BUG-9 | high | `connect` doesn't refresh the sender's heartbeat | Single-shot heartbeat refresh on connect |

**Root cause**: no command in the operating loop (listen/receive/connect) refreshes the heartbeat — the architecture presumes external heartbeat scheduling that no command performs. The fix is a lifecycle abstraction (a session manager owning the heartbeat goroutine), not point patches.

---

## Architecture

Filesystem-polling JSON transport. No daemon in the shipped versions (a Unix-socket daemon is gated — see Roadmap).

```
~/.claude/cli-agents-bridge/
├── config.json                  # optional; CAB_* env vars override
└── sessions/<id>/
    ├── manifest.json            # schema v2
    ├── inbox/*.json             # incoming (atomic write: temp same-dir + fsync + rename)
    ├── outbox/*.json            # copy of own sends (self-send visibility)
    ├── processed/               # audit trail post-consume
    └── lock                     # O_EXCL PID file
```

The plugin tree lives under `plugins/cli-agents-bridge/` (marketplace install requires a subdir layout; source `.` is not supported). The binary is `bin/cab-bridge`, auto-added to `PATH` by the plugin system; version is injected from the git tag (`git describe` / GoReleaser).

### Go packages (`internal/`)

| Package | Responsibility |
|---|---|
| `config` | Load `config/default.json` + `~/.claude/cli-agents-bridge/config.json` + `CAB_*` env |
| `session` | Register, longest-prefix lookup, heartbeat goroutine, `O_EXCL` lock + stale recovery, agent `state`, idempotent reconnect |
| `transport/fs` | Atomic write, polling (`time.Ticker` + `context.Context`), long-poll receive, drain-once |
| `message` | Schema v2 marshal + a strict validation gateway (`DisallowUnknownFields`) and a lenient runtime reader (forward-compatible) |
| `routing` | Role-based send rules (hub-and-spoke; `observer` cannot send; `esc→esc` needs `--allow-mesh`) |
| `security` | umask 077, perms 700/600, ownership check, session-ID regex, base-dir integrity |
| `cleanup` | Scope-aware cleanup, pre-delete archive, retention sweep, PID-aware staleness |

### Schemas (v2)

Manifest: `sessionId`, `schemaVersion`, `projectName`, `projectPath`, `agentName`, `role`, `pid`, `startedAt`, `lastHeartbeat`, `status`, `capabilities`, plus additive fields introduced later (`teamId`, `scope`, `state`, `lastConsumedMsgId`). Message: `id`, `schemaVersion`, `from`/`fromRole`/`fromAgentName`, `to`/`toRole`, `type`, `timestamp`, `status`, `content`, `inReplyTo` (`*string` for explicit null), `metadata`. Schema growth is additive — the lenient runtime reader ignores unknown fields, so a newer peer never breaks an older one; the strict gateway is used only for write/audit validation.

---

## Security baseline

**In scope** (single-user macOS/Linux): a different-UID local process reading inbox/outbox; path traversal via session-ID injection; TOCTOU on lock/manifest; cross-session destructive cleanup; symlink attack on dir creation; manifest spoofing. **Out of scope** (stated): remote attacker (no network surface), same-UID malware (Unix single-user limit), supply chain, encryption (single-disk single-user vs FileVault), multi-tenant machines.

| Control | What |
|---|---|
| SC-1 | `syscall.Umask(0o077)` in `init()` before any file/dir creation |
| SC-2 | `MkdirAll(…, 0o700)` + explicit chmod on existing dirs |
| SC-3 | Ownership check helper (`Stat.Uid == Getuid()`) — primitive present, runtime wiring tracked for a later version |
| SC-4 | Session-ID regex `^[a-z0-9]{6,32}$` on every path-component field |
| SC-5 | `0o600` writes + atomic write (temp same-filesystem + `f.Sync()` + `Rename`, EXDEV explicit) |
| SC-6 | Lock `O_CREATE\|O_EXCL\|O_WRONLY 0o600` + `kill -0` stale recovery |
| SC-7 | Boot check: base dir is not a symlink, perms 700, owner == `Getuid()` |

GDPR (local-only data): data minimization via retention sweep, right-to-erasure via cleanup, data localization (nothing leaves the machine), documented in [PRIVACY.md](./PRIVACY.md). Full detail and honest deferral notes in [SECURITY.md](./SECURITY.md).

---

## Roadmap

### Shipped

- **v0.2.0** — MVP: 9 upstream bugs fixed structurally, role routing, longest-prefix lookup, atomic writes, security baseline, single binary, marketplace install.
- **v0.2.1** — auto-gc of orphan sessions; cleanup data-loss fix.
- **v0.2.2** — observability + instant wake: `ack` type + automatic delivery receipt on consume; `listen --wait-one` (wake-on-arrival); `teamId` + `whoami`; outbox + `sent`.
- **v0.2.3** — prebuilt multi-OS binaries (GoReleaser on tag) + version injection; public `bridge-workflow` skill.
- **v0.2.4** — automatic per-project isolation: `register` derives a `scope` from the project root (`.git`); `peers` filters by it (`--all-scopes` for the global view).
- **v0.4.0** — reliable wake/delivery cycle + tooling: `receive` archives a matched reply to `processed/` (recoverable from home); `listen --wait-one` exits 0 with a timeout payload instead of 124; `listen --until-deadline`; `inbox --list`/`--tidy`; agent `state` (`working`/`done`/`orchestrating`, with `orchestrating` heartbeat-exempt); `register --resume` (idempotent reconnect-or-register after a compact/restart).

These versions were built and validated through the bridge itself (dogfooding), with an independent `go test -race -count=1` gate per change plus real-use field validation.

### Next

- **Conversation cursor** — attach the last-read peer message-id to each send so the bridge can flag message crossings (both sides acting on a stale message); the single highest-impact item against conversational rework. Subsumes a message-level read-receipt.
- **Inbox ergonomics** — `inbox --list` type/unread filters; an id-less `receive --any` ("wake on the next message", since with late replies the exact id is often unknown).
- **Quality** — wire the SC-3 ownership check onto the live path; bump CI actions to Node 24.

### Gated (only if the need is measured)

- **Unix-socket daemon** — only if filesystem-polling latency exceeds ~200ms in real long runs **and** concurrent peers exceed 3. Otherwise it is not built.
- **v1.0** — Anthropic marketplace submission, opt-in encryption (with a real use case first), multi-machine via Tailscale (justified empirically first), after sustained real use.

---

## Distribution

Self-marketplace on GitHub (primary): `/plugin marketplace add MyAIPlugins/cli-agents-bridge` + install. Prebuilt binaries are published to GitHub Releases for standalone PATH use. The binary works both when invoked by the plugin manager and from a manual `$PATH` install. Repository: <https://github.com/MyAIPlugins/cli-agents-bridge>.
