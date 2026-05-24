# Multi-ESC patterns

Workflow patterns for `1 VAL + N ESC` topology. Empirically validated during ac-agents and chatterence-bi-template sprints.

---

## Default: hub-and-spoke VAL-centric

```
              ┌───────┐
              │  VAL  │
              └───┬───┘
       ┌──────────┼──────────┐
       │          │          │
   ┌───▼───┐  ┌───▼───┐  ┌───▼───┐
   │ ESC-A │  │ ESC-B │  │ ESC-C │
   └───────┘  └───────┘  └───────┘
```

`routing.ValidateSendPair` enforces this by default:

- VAL ↔ ESC: OK (canonical workflow)
- VAL ↔ VAL: OK (multi-VAL planning sessions)
- VAL ↔ architect: OK (triadic pattern with architect role)
- ESC ↔ ESC: **REJECTED** with `ErrEscToEscForbidden`

Rationale: empirical evidence from p1-wp-translator (2026-05-24) — a free mesh topology led to ESC-A messaging ESC-B under the misconception it was VAL. Hub-and-spoke makes routing intent explicit by structure.

Example setup:

```bash
# Window 1: VAL
cab-bridge register --role=val --agent-name=VAL-main --project-path=/repo

# Window 2: ESC-A
cab-bridge register --role=esc --agent-name=ESC-analysis --project-path=/repo/analysis

# Window 3: ESC-B
cab-bridge register --role=esc --agent-name=ESC-src --project-path=/repo/src
```

Note the distinct `--project-path` for each ESC — `BUG-5` longest-prefix-match relies on path differentiation. Two `register` calls with the same `--project-path` would fail with `ErrSessionExistsForProject` (BUG-6 protection) unless `--force-new`.

---

## Opt-in: mesh (esc ↔ esc with `--allow-mesh`)

Override the default for specific use cases where two ESCs genuinely need to coordinate without going through VAL.

```bash
# From ESC-A to ESC-B (otherwise blocked)
cab-bridge ask --to=<ESC-B-id> --content="..." --allow-mesh
```

### When to use `--allow-mesh`

- **Peer review**: ESC-A finishes its task, asks ESC-B "review my output before I commit". VAL doesn't need to mediate.
- **Parallel exploration**: ESC-A and ESC-B independently try two approaches; they exchange progress to decide which converges faster.
- **Blue/green**: ESC-A runs old code, ESC-B runs new code; they coordinate to compare outputs.

### When NOT to use

- Anything you'd describe as "delegation" — that's VAL's job.
- Anything where you want VAL to know what happened — VAL is not in the loop with mesh messages.
- More than two ESCs talking pair-wise — quickly becomes the chaos `BUG-3` was meant to prevent. Three-or-more peer coordination should route through VAL.

---

## Observer role (read-only sink)

A session registered as `--role=observer` can RECEIVE messages but CANNOT SEND. This is enforced structurally — no flag, including `--allow-mesh`, relaxes it.

```bash
cab-bridge register --role=observer --agent-name=metrics-collector
```

Use cases:

- **Audit / metrics collection**: third-party process that ingests bridge traffic without modifying it.
- **Architect read-only mode**: an architect agent that should see VAL ↔ ESC traffic but not interject.

Implementation: VAL can `ask --to=<observer-id>` to broadcast; the observer never reciprocates.

---

## ErrSessionExistsForProject — "live session per project"

Sprint 1 finding (BUG-6 fix): `cab-bridge register` refuses a second registration on the same `projectPath` if a live session already exists there.

```
Error: project "/repo/analysis" already has active session abc123 (pid 4567), use --force-new to override
```

Semantics:

- "Live" = `manifest.pid` corresponds to a process responding to `kill -0`. A crashed session leaves an orphan PID; the next register's stale recovery removes the lock and proceeds.
- This is NOT "one session per project ever" — multiple sessions can coexist for the same project at different times. The constraint is "no two LIVE sessions for the same project simultaneously".
- Override with `--force-new` when you genuinely want two parallel sessions (rare; typically indicates a workflow that should split `projectPath` into subdirs).

---

## Cleanup discipline

In a multi-ESC setup, default `cab-bridge cleanup` is **scope=my-session** — touches only the caller's own session.

Use `--scope=global` (with interactive confirmation) only when you're sure no other window has an active session.

The structural BUG-4 fix means a typo here is bounded: the worst case is `cab-bridge cleanup --scope=global --force` from a single window — which removes only sessions whose heartbeat is older than `StaleSeconds` (default 300s = 5min). Active peers are protected by their heartbeat goroutine (BUG-1 fix).

---

## Real-world examples from validation

| Scenario | Pattern | Notes |
|---|---|---|
| Single VAL + single ESC, sequential tasks | hub default | the 90% case |
| VAL coordinating 2 ESCs on parallel modules | hub default + ESC-to-ESC peer review with `--allow-mesh` | empirically observed valuable |
| Architect + VAL + ESC triadic | architect role for planning, val for orchestration, esc for execution | `routing.ValidateSendPair` allows val↔architect by default |
| Metrics agent observing live traffic | observer role | structurally read-only |
