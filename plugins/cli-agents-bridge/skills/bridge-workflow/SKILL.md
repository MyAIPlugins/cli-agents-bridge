---
name: bridge-workflow
description: How to coordinate two (or more) CLI agent sessions with the cab-bridge binary — register/listen/ask/receive, the PID/heartbeat model, instant wake with listen --wait-one, delivery receipts (auto-ack), team isolation, and self-send visibility. Use when one agent session needs to hand work to, or exchange messages with, another agent session on the same machine via cab-bridge.
---

# cab-bridge — coordinating two agent sessions

`cab-bridge` is a single Go binary (on `$PATH` when this plugin is installed) that lets two CLI agent sessions on the same machine exchange JSON messages through files under `~/.claude/cli-agents-bridge/sessions/<id>/{inbox,outbox,processed}/`. No network, no API calls: each agent reads its inbox and replies from its own live context.

A common shape is **one orchestrator + one executor** (the default roles are named `val` and `esc`, but the roles are free-form — see "Roles" below). Nothing here is specific to those names.

## Mental model (read this first — it prevents the #1 confusion)

A "session" is a manifest on disk. Its `pid` and liveness work like this:

- `cab-bridge register` is **one-shot**: it writes the manifest, records the register command's PID, then **exits — so that PID is already dead**. Right after `register`, a session has a dead PID. This is normal.
- `cab-bridge listen` is **long-running**: on start it **adopts** the session (writes its OWN live PID into the manifest) and refreshes `lastHeartbeat` periodically. A session is "alive" only while a `listen` holds it.
- **Consequence**: outside `listen`, a session goes `stale` after `StaleSeconds` (default 300s). That is NOT a bug. An orchestrator that doesn't sit in `listen` will look stale — it doesn't block anything; messages still land in its inbox.
- Orphan sweep: a session whose PID is dead AND whose heartbeat is older than `AutoGCHours` (default 24h) is removed by the auto-gc that runs at `register` startup.

## Setup constraints

- **Distinct working directories**: the two sessions should start from DIFFERENT cwds (session lookup is longest-prefix-by-cwd; same cwd is ambiguous). The cwd does NOT limit file access — only the bridge's "which session am I" lookup. Passing `--session-id` explicitly avoids the ambiguity entirely.
- **Same data dir = the pair's channel**: both sessions must use the same `CAB_DATA_DIR` (default `~/.claude/cli-agents-bridge/`). Use the LITERAL same value in both (never a shell `$$`).
- **Multiple pairs at once → isolate by team**: if several pairs share one data dir, `peers` shows everyone and you can confuse whose session is whose. Two options: a separate `CAB_DATA_DIR` per pair, or — simpler — register with `--team=<name>` and filter with `peers --team=<name>`.

## The two patterns

### Orchestrator (does NOT sit in listen)

```bash
cab-bridge register --role=val --agent-name=orchestrator-1   # -> sessionId
cab-bridge peers                                             # discover the other session's id
cab-bridge ask --session-id=<self> --to=<peer> --file=/tmp/brief.md   # -> msg-id (use --file for long payloads)
# wait for the reply without hand-polling:
cab-bridge receive --session-id=<self> --msg-id=<msg-id> --max-deadline=300
```

`receive` is a WAKE signal, not a guaranteed delivery: treat it as "something happened", then verify the real state by reading the inbox files on disk (and, if the task produces commits, `git log`). A reply that lands after the deadline stays in the inbox and is picked up on the next read.

### Executor (sits in listen to receive)

```bash
cab-bridge register --role=esc --agent-name=executor-1       # -> sessionId
cab-bridge listen --wait-one --session-id=<self>             # see wake note below
# on a work task: leave listen, implement, then reply:
cab-bridge ask --session-id=<self> --to=<orchestrator> --type=response --in-reply-to=<brief-id> --file=/tmp/reply.md
```

## Instant wake — use `listen --wait-one`

A `listen` running in the background notifies the agent only when the command EXITS, not on each message. With a long blocking window, an urgent message sits unseen until the window times out.

**Preferred**: `cab-bridge listen --wait-one` exits (code 0) as soon as the first non-empty batch arrives — so a background caller is woken the instant a message lands. Process it, then re-launch `listen --wait-one`. It delivers the whole batch present at that sweep (lossless — no message is consumed-but-unseen). On an empty inbox it still honors the blocking timeout and exits 124, so the caller re-launches exactly as for the default listen.

Keep an executor in an ACTIVE listen between tasks: an agent that finished its turn and is no longer listening will NOT be woken by a new message until something re-engages it.

## Delivery receipts (auto-ack) + task-state observability

- When a `listen` consumes a `query`, the binary automatically sends a `type=ack` receipt back to the sender (`inReplyTo` set to the original id). The orchestrator gets an `sent → ack → done` state machine for free. Only `query` triggers an auto-ack (so a receipt never begets a receipt). Suppress it with `listen --no-auto-ack`.
- `peers` and `status` expose `inboxCount` (pending, un-consumed messages) and `lastConsumedMsgId` — so you can tell an idle session from one actively draining its inbox, without relying on heartbeat (which only proves the listen process is alive, not that work is happening).

## Knowing who/where you are — `cab whoami`

`cab-bridge whoami` prints the current session's identity: sessionId, agent name, role, team, the FULL `projectPath` (not just the basename), and the current `dataDir`. The `dataDir` line is the quickest way to catch the classic mistake of registering in the wrong data dir (a forgotten `CAB_DATA_DIR`).

## Seeing your own sends — `cab sent`

Every message you send is also copied into your own `outbox/`. `cab-bridge sent` lists what you sent (msg-id, to, type, timestamp, in-reply-to) — so you can verify your own outbound traffic from your own data, not by inspecting the recipient's inbox. `status` reports `outboxCount`.

## Roles

Default roles are `val` (orchestrator), `esc` (executor), plus `architect`, `observer`, `neutral`. **Roles are free-form**: you can register any custom role (e.g. `--role=planner`, `--role=coder`, `--role=peer`) and routing accepts it. Only two structural rules apply:

- `observer` cannot send (read-only sink) — no flag overrides this.
- `esc → esc` is rejected by default (route through the orchestrator); pass `--allow-mesh` for advanced mesh scenarios.

So two equal agents with no hierarchy can just use a custom role (e.g. both `--role=peer`) — `peer ↔ peer` is allowed out of the box.

## Recovery after a reboot / reset

A reboot leaves the manifests on disk with dead PIDs (stale/orphan); sessions do NOT re-attach themselves. Re-`register` (you get a NEW sessionId — the old one is dead), re-announce the new id to the peer, and sweep orphans with `cab-bridge cleanup --scope=global --force`.

## Cleaning up dead sessions

Closing a window/session does not delete its session — it lingers as an orphan until the auto-gc threshold. A reliable hook would be unreliable (no shutdown hook fires on force-quit/crash); the robust pattern is reconcile-on-start, not cleanup-on-close. To clear dead sessions now: `cab-bridge cleanup --scope=global --force` (removes sessions stale beyond `StaleSeconds`; a live `listen` is preserved). For a single one: `cab-bridge cleanup --session-id=<id>`.

## Command quick reference

```
cab-bridge register --role=<val|esc|architect|observer|neutral|custom> --agent-name=<name> [--team=<name>] [--force-new]
cab-bridge listen   --session-id=<id> [--wait-one] [--no-auto-ack]
cab-bridge ask      --session-id=<id> --to=<peer> [--content=... | --file=path] [--type=query|response|notify|ack] [--in-reply-to=msg-...] [--allow-mesh]
cab-bridge receive  --session-id=<id> --msg-id=<msg-...> --max-deadline=<sec>
cab-bridge peers    [--json] [--team=<name>] [--include-stale]
cab-bridge status   --session-id=<id>
cab-bridge whoami   [--session-id=<id>] [--json]
cab-bridge sent     [--session-id=<id>] [--json]
cab-bridge connect  --session-id=<id> <peer>
cab-bridge cleanup  [--scope=my-session|global] [--session-id=<id>] [--force]
cab-bridge inspect  <id>
cab-bridge version
```

Exit codes: 0 ok, 1 validation, 2 routing-forbidden, 3 cleanup-confirm-required, 124 timeout (also: `listen --wait-one` exits 0 on delivery, 124 on empty-inbox timeout).
