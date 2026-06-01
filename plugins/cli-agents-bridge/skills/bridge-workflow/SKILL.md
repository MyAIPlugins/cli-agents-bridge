---
name: bridge-workflow
description: How to coordinate two (or more) CLI agent sessions with the cab-bridge binary — register/listen/ask/receive, the PID/heartbeat model, instant wake with listen --wait-one (and --until-deadline), zero-config onboarding (bootstrap), id-less wake (receive --any), at-a-glance state (overview), scope = git repository (same-repo worktrees pair automatically), delivery receipts (auto-ack), agent state (working/done/orchestrating), automatic per-project isolation, inbox inspection (inbox --list/--tidy), idempotent reconnect after a compact (register --resume), team isolation, and self-send visibility. Use when one agent session needs to hand work to, or exchange messages with, another agent session on the same machine via cab-bridge.
---

# cab-bridge — coordinating two agent sessions

`cab-bridge` is a single Go binary (on `$PATH` when this plugin is installed) that lets two CLI agent sessions on the same machine exchange JSON messages through files under `~/.claude/cli-agents-bridge/sessions/<id>/{inbox,outbox,processed}/`. No network, no API calls: each agent reads its inbox and replies from its own live context.

A common shape is **one orchestrator + one executor** (the default roles are named `val` and `esc`, but the roles are free-form — see "Roles" below). Nothing here is specific to those names.

## Mental model (read this first — it prevents the #1 confusion)

A "session" is a manifest on disk. Its `pid` and liveness work like this:

- `cab-bridge register` is **one-shot**: it writes the manifest, records the register command's PID, then **exits — so that PID is already dead**. Right after `register`, a session has a dead PID. This is normal.
- `cab-bridge listen` is **long-running**: on start it **adopts** the session (writes its OWN live PID into the manifest) and refreshes `lastHeartbeat` periodically. A session is "alive" only while a `listen` holds it.
- **Consequence**: outside `listen`, a session goes `stale` after `StaleSeconds` (default 300s). That is NOT a bug. An orchestrator that doesn't sit in `listen` will look stale — it doesn't block anything; messages still land in its inbox. **Since v0.4 (F-23a) an orchestrator can declare `cab-bridge state orchestrating` once and is then heartbeat-exempt — it no longer shows stale (see "Task state" below).**
- Orphan sweep: a session whose PID is dead AND whose heartbeat is older than `AutoGCHours` (default 24h) is removed by the auto-gc that runs at `register` startup.

## Setup constraints

- **Distinct working directories**: the two sessions should start from DIFFERENT cwds (session lookup is longest-prefix-by-cwd; same cwd is ambiguous). The cwd does NOT limit file access — only the bridge's "which session am I" lookup. Passing `--session-id` explicitly avoids the ambiguity entirely.
- **Automatic per-project isolation (v0.4, F-17) — the common case needs no config**: the v0.4 binary derives a `scope` from the project root (the `.git` marker, walking up from cwd; `$HOME` excluded; fallback = cwd) at `register`, and `peers` shows only the sessions of the current project by default. So a pair in the SAME repo isolates itself — you need NEITHER `CAB_DATA_DIR` NOR `--team`. `peers --all-scopes` for the global view; `whoami` shows the `scope`. **(v0.5, F-41)** the scope is the git REPOSITORY (git-common-root): a linked `git worktree` resolves to its main repo, so a VAL at the repo root and an ESC in a worktree of the SAME repo share one scope and pair in plain `peers` — no flags. Different git repos keep distinct scopes (isolated).
- **Manual isolation (special cases / pre-v0.4)**: both sessions sharing the same `CAB_DATA_DIR` (default `~/.claude/cli-agents-bridge/`, LITERAL same value, never a shell `$$`) is the physical channel. Use it for peers on DIFFERENT git repos that must share one channel, or for two pairs in one repo (since v0.5/F-41, worktrees of the SAME repo already share a scope — nothing needed). `--team=<name>` + `peers --team=<name>` is a logical filter WITHIN one data dir — do not mix the two axes (a session without the team is hidden by `peers --team`).

## Zero-config onboarding — `bootstrap` (v0.5, F-40)

A fresh agent pairs in ONE command, with no id to type or transcribe:
```bash
cab-bridge bootstrap --role=esc   # register + discover peer + adaptive name + enter listen
cab-bridge bootstrap --role=val   # register + set state=orchestrating + exit (a val does not listen)
```
`bootstrap` discovers an already-registered peer in its scope (in-process — no piped output to parse), derives its own name adaptively (inherits the peer's suffix: a peer `VAL-x` → `ESC-x`, converging in either order; fallback `<ROLE>-<scope-basename>`), and registers idempotently (`--resume`). For `role=esc` it hands off to `listen --wait-one`, so the executor's session id is managed internally and never transcribed — the safest onboarding for a fresh agent. Pass `--agent-name=<name>` to override the derived name. Re-running the same command is the id-free re-listen loop.

## The two patterns

### Orchestrator (does NOT sit in listen)

```bash
cab-bridge register --role=val --agent-name=orchestrator-1   # -> sessionId
cab-bridge peers                                             # discover the other session's id
cab-bridge ask --session-id=<self> --to=<peer> --file=/tmp/brief.md   # -> msg-id (use --file for long payloads)
# wait for the reply without hand-polling:
cab-bridge receive --session-id=<self> --msg-id=<msg-id> --max-deadline=300
# or wake on ANY next non-ack message, with NO id to wait on (v0.5, F-36):
cab-bridge receive --any --max-deadline=300        # times out exit 0 with {"status":"timeout"}; acks never wake it
```

`receive` is a WAKE signal, not a guaranteed delivery: treat it as "something happened", then verify the real state by reading the inbox files on disk (and, if the task produces commits, `git log`). A reply that lands after the deadline stays in the inbox and is picked up on the next read. **Since v0.4 (F-30), when `receive` DOES match it archives the reply to your OWN `processed/` dir** (symmetric with `listen`) — so even if a background caller misses the stdout, you recover the consumed reply with `inbox --list` from your own session, instead of digging through the sender's outbox. **`receive --any` (v0.5, F-36)** wakes on the first non-ack message with no `--in-reply-to` to match — the robust id-free wake when the orchestrator has nothing specific to await; `--msg-id` is for awaiting ONE specific reply (and needs the executor to tag `--in-reply-to` exactly, else its hit-rate is low, F-2).

### Executor (sits in listen to receive)

```bash
cab-bridge register --role=esc --agent-name=executor-1       # -> sessionId
cab-bridge listen --wait-one --session-id=<self>             # see wake note below
# on a work task: leave listen, implement, then reply:
cab-bridge ask --session-id=<self> --to=<orchestrator> --type=response --in-reply-to=<brief-id> --file=/tmp/reply.md
```

## Instant wake — use `listen --wait-one`

A `listen` running in the background notifies the agent only when the command EXITS, not on each message. With a long blocking window, an urgent message sits unseen until the window times out.

**Preferred**: `cab-bridge listen --wait-one` exits (code 0) as soon as the first non-empty batch arrives — so a background caller is woken the instant a message lands. Process it, then re-launch `listen --wait-one`. It delivers the whole batch present at that sweep (lossless — no message is consumed-but-unseen). **On an empty-window timeout it exits 0 with a `{"status":"timeout","messages":[]}` payload (v0.4, F-24) — not a failure** — so a background harness doesn't read "command failed" every idle cycle; the caller tells a timeout from a delivered batch by the `status` field. (The default non-`--wait-one` `listen` keeps exit 124 for bash until-loops.)

For a long standby window without re-looping every ~9 min, set it explicitly: **`listen --until-deadline=2h`** (v0.4, F-26 — more discoverable than the `CAB_MAX_BLOCKING_SECONDS` env; precedence: flag > env > 540s default).

Keep an executor in an ACTIVE listen between tasks: an agent that finished its turn and is no longer listening will NOT be woken by a new message until something re-engages it.

## Delivery receipts (auto-ack) + task-state observability

- When a `listen` consumes a `query`, the binary automatically sends a `type=ack` receipt back to the sender (`inReplyTo` set to the original id). The orchestrator gets an `sent → ack → done` state machine for free. Only `query` triggers an auto-ack (so a receipt never begets a receipt). Suppress it with `listen --no-auto-ack`.
- `peers` and `status` expose `inboxCount` (pending, un-consumed messages) and `lastConsumedMsgId` — so you can tell an idle session from one actively draining its inbox, without relying on heartbeat (which only proves the listen process is alive, not that work is happening).
- **Agent state (v0.4, F-23a)**: `cab-bridge state <idle|working|done|orchestrating>` sets the session's state — the flag goes BEFORE the value: `cab-bridge state --session-id=<id> working`. `peers` (a `STATE` column), `status`, and `whoami` show it, so an orchestrator sees a peer move `working → done` natively, with less manual ACK discipline. `orchestrating` makes a session **heartbeat-exempt** (never stale) — for an orchestrator that does not sit in `listen`. State is setter-only; read it via `whoami`/`status`/`peers`.

## Knowing who/where you are — `cab whoami`

`cab-bridge whoami` prints the current session's identity: sessionId, agent name, role, team, the FULL `projectPath` (not just the basename), and the current `dataDir`. The `dataDir` line is the quickest way to catch the classic mistake of registering in the wrong data dir (a forgotten `CAB_DATA_DIR`).

## At-a-glance state — `overview` (v0.5, F-42)

`cab-bridge overview` prints, in ONE call and with NO `--session-id`, your whole world: who you are (id, scope, state), your paired peer (the complementary role in your scope), and your pending inbox — human-readable by default (`--json` for scripting). It collapses the `peers` + `whoami` + inbox-listing dance into one scannable view, and is worktree-aware (it resolves "you" from the cwd).

## Seeing your own sends — `cab sent`

Every message you send is also copied into your own `outbox/`. `cab-bridge sent` lists what you sent (msg-id, to, type, timestamp, in-reply-to) — so you can verify your own outbound traffic from your own data, not by inspecting the recipient's inbox. `status` reports `outboxCount`.

## Roles

Default roles are `val` (orchestrator), `esc` (executor), plus `architect`, `observer`, `neutral`. **Roles are free-form**: you can register any custom role (e.g. `--role=planner`, `--role=coder`, `--role=peer`) and routing accepts it. Only two structural rules apply:

- `observer` cannot send (read-only sink) — no flag overrides this.
- `esc → esc` is rejected by default (route through the orchestrator); pass `--allow-mesh` for advanced mesh scenarios.

So two equal agents with no hierarchy can just use a custom role (e.g. both `--role=peer`) — `peer ↔ peer` is allowed out of the box.

## Recovery after a reboot / reset

A reboot/restart/**compact** leaves the manifests on disk with dead PIDs. **Since v0.4 (F-27) recovery is one deterministic line**:

```bash
cab-bridge register --resume --agent-name=<same-name> --role=<same-role>
```

`--resume` = reconnect-or-register: it resumes the existing session matching your identity (agent-name + role + scope + team) — **same sessionId, same inbox/processed/outbox, same state** — or registers fresh if none matches. So you keep your old id (the peer keeps writing to the same place — no re-announce), and skip manual `whoami`+`peers` reconciliation. Liveness is the manifest PID (`IsProcessAlive`): a live owner (an active `listen`) is never stolen — if every identity match is live, the command errors (use `--force-new` for a deliberate second instance). A legacy (pre-F-17) session resumed this way has its `scope` backfilled. *(Pre-v0.4: re-`register` for a NEW id + re-announce to the peer + `cleanup --scope=global --force`.)*

## Cleaning up dead sessions

Closing a window/session does not delete its session — it lingers as an orphan until the auto-gc threshold. A reliable hook would be unreliable (no shutdown hook fires on force-quit/crash); the robust pattern is reconcile-on-start, not cleanup-on-close. To clear dead sessions now: `cab-bridge cleanup --scope=global --force` (removes sessions stale beyond `StaleSeconds` — via the shared `IsStale`, so a session in state `orchestrating` is exempt; a live `listen` is preserved). For a single one: `cab-bridge cleanup --session-id=<id>`.

## Inspecting the inbox — `inbox --list` / `--tidy` (v0.4, F-22)

`cab-bridge inbox --session-id=<id> --list [--json]` lists `inbox/` (pending) and `processed/` (consumed) messages WITHOUT consuming them — id, from, type, timestamp, one-line preview, with a `box` field distinguishing the two. It replaces a fragile `ls inbox/*.json` and is how you recover a reply that `listen`/`receive` already archived to `processed/` (completes F-30). `cab-bridge inbox --session-id=<id> --tidy` archives every well-formed `inbox/` message to `processed/` (lossless sweep) — the explicit "I handled what `--list` showed" hygiene action. `--list` and `--tidy` are mutually exclusive.

## Command quick reference

```
cab-bridge bootstrap --role=<val|esc> [--agent-name=<name>] [--until-deadline=<dur>]   # zero-config onboarding (v0.5)
cab-bridge register --role=<val|esc|architect|observer|neutral|custom> --agent-name=<name> [--team=<name>] [--resume] [--force-new]
cab-bridge listen   --session-id=<id> [--wait-one] [--until-deadline=<dur, e.g. 2h>] [--no-auto-ack]
cab-bridge ask      --session-id=<id> --to=<peer> [--content=... | --file=path] [--type=query|response|notify|ack] [--in-reply-to=msg-...] [--allow-mesh]
cab-bridge receive  --session-id=<id> (--msg-id=<msg-...> | --any) --max-deadline=<sec>   # --any: id-less wake (v0.5)
cab-bridge state    --session-id=<id> <idle|working|done|orchestrating>     # flag BEFORE the value
cab-bridge inbox    --session-id=<id> (--list [--json] | --tidy)
cab-bridge peers    [--json] [--team=<name>] [--all-scopes] [--include-stale]
cab-bridge status   --session-id=<id>
cab-bridge whoami   [--session-id=<id>] [--json]
cab-bridge overview [--json]                                   # me + peer + inbox in one call, no id (v0.5)
cab-bridge sent     [--session-id=<id>] [--json]
cab-bridge connect  --session-id=<id> <peer>
cab-bridge cleanup  [--scope=my-session|global] [--session-id=<id>] [--force]
cab-bridge inspect  <id>
cab-bridge version
```

Exit codes: 0 ok, 1 validation, 2 routing-forbidden, 3 cleanup-confirm-required, 124 timeout. `listen --wait-one` exits 0 on delivery AND on an empty-window timeout (the latter with a `{"status":"timeout","messages":[]}` payload, F-24); the default `listen` exits 124 on timeout.
