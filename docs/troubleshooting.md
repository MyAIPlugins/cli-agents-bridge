# Troubleshooting — cli-agents-bridge

Common issues and their fixes.

---

## "list-peers shows my session as stale, but it's actively listening"

**Symptom**: `cab-bridge peers` displays `STALE` in the rightmost column even though the session is in an active `listen` loop.

**Why this happens**: a session is "stale" when `time.Now() - manifest.lastHeartbeat > cfg.StaleSeconds` (default 300s = 5 min). `listen` updates `lastHeartbeat` every `cfg.HeartbeatTickMs` (default 30s) via a background goroutine — so a healthy listen should never appear stale.

**Diagnostic**:

1. `cab-bridge inspect <id>` — read the manifest directly. Note `lastHeartbeat`.
2. Compare to `date -u`. If the delta is < 60s, the heartbeat IS running but `cab-bridge peers` may have a stale `cfg.StaleSeconds`.
3. `echo $CAB_STALE_SECONDS` — if set to a very low value (e.g. `5`), the threshold is too tight.

**Fix**:

- Use the default by unsetting the env var: `unset CAB_STALE_SECONDS`.
- If you intentionally want a tighter threshold for `peers`, also tighten the heartbeat tick: `CAB_HEARTBEAT_TICK_MS=2000` will refresh every 2s.

---

## "cab-bridge register fails with 'session already exists for project'"

**Symptom**:

```
Error: project "/repo/analysis" already has active session abc123 (pid 4567), use --force-new to override
```

**Why this happens**: BUG-6 protection (Sprint 1) — `cli-agents-bridge` refuses to silently create a duplicate session for the same `projectPath`. Patil v0.1 reused `.claude/bridge-session` silently and ended with two sessions sharing one inbox.

**Fix**:

1. Check whether a previous Claude Code window is still alive: `ps -p 4567` (replace with the PID from the error).
2. If alive: reuse the existing session — connect to it from the new window via `cab-bridge connect --session-id=abc123 <other-peer-id>` or close the old window first.
3. If dead (`ps` shows nothing): the lock is stale. Either:
   - `cab-bridge register --force-new ...` to override.
   - Or `rm ~/.claude/cli-agents-bridge/sessions/abc123/lock` then `register` normally.

---

## "How do I migrate from Patil's session-bridge?"

See [docs/migration-from-patil.md](./migration-from-patil.md) for the full procedure. TL;DR:

```bash
cab-bridge migrate-from-patil --dry-run   # preview
cab-bridge migrate-from-patil             # do it
cab-bridge migrate-from-patil             # idempotent re-run
```

---

## "Which subcommand do I use for X?"

| Goal | Command |
|---|---|
| Start a session | `cab-bridge register --role=val\|esc --agent-name=NAME` |
| See who's around | `cab-bridge peers` |
| Send a message | `cab-bridge ask --to=ID --content="..."` |
| Send a message with file payload | `cab-bridge ask --to=ID --file=path.md` |
| Wait for a reply | `cab-bridge receive --msg-id=msg-... --max-deadline=N` |
| Listen for incoming continuously | `cab-bridge listen` |
| Validate a peer is reachable | `cab-bridge connect <peer-id>` |
| See my status (inbox/outbox counts) | `cab-bridge status` |
| Inspect any session's manifest | `cab-bridge inspect <session-id>` |
| Clean up my session | `cab-bridge cleanup` |
| Clean up all stale sessions | `cab-bridge cleanup --scope=global` |

Each subcommand has its own `--help`.

---

## "Receive timed out (exit 124) but the reply DID arrive — where did it go?"

**Symptom**: `cab-bridge receive --msg-id=msg-X --max-deadline=10` exits 124. Later you check `cab-bridge status` and `inboxCount` is 1.

**Why this happens**: BUG-2 fix (Sprint 2) — late replies are NOT silently dropped. They stay in `inbox/` until consumed.

**Fix**: re-run `receive` (or call it with a longer deadline):

```bash
cab-bridge receive --msg-id=msg-X --max-deadline=60
```

The pending reply is found and consumed. This is the structural difference vs Patil's bridge-receive.sh, which would have lost the reply permanently.

---

## "I get 'esc→esc routing forbidden by default' — why?"

**Symptom**:

```
Error: esc→esc routing forbidden by default (use --allow-mesh to override): from="esc" to="esc"
```

**Why this happens**: BUG-3 fix (Sprint 3) — `routing.ValidateSendPair` rejects ESC-to-ESC by default to prevent the routing chaos Alan reported empirically (ESC-A messaging ESC-B under the misconception it was VAL).

**Fix**: if you genuinely want mesh communication, pass `--allow-mesh`:

```bash
cab-bridge ask --to=<other-esc-id> --content="..." --allow-mesh
```

See [docs/multi-esc-patterns.md](./multi-esc-patterns.md) for when this is the right call.

---

## "Plugin install fails with 'source type your Claude Code version does not support'"

**Symptom**: `/plugin install cli-agents-bridge@cli-agents-bridge-marketplace` fails immediately.

**Why this happens**: Claude Code 2.1.150 requires the Patil-style layout (plugin in a subdir under `plugins/<name>/`, marketplace.json at the root). cli-agents-bridge ships exactly this layout (see Sprint 1 `refactor(layout)` commit), so if you see this error it likely means:

1. The repo you added is a fork that broke the layout.
2. Your Claude Code is older than 2.1.150 and missing features.

**Fix**:

1. Verify Claude Code version: `claude --version` (should be ≥ 2.1.150).
2. Reinstall via the official marketplace path:

```
/plugin marketplace remove cli-agents-bridge-marketplace
/plugin marketplace add myAIPlugins/cli-agents-bridge
/plugin install cli-agents-bridge@cli-agents-bridge-marketplace
```

---

## "Where are my data stored?"

`~/.claude/cli-agents-bridge/`. See [PRIVACY.md](../PRIVACY.md) for the full inventory.

To wipe everything: `rm -rf ~/.claude/cli-agents-bridge/`.

---

## Reporting bugs

If your issue isn't covered here:

1. Capture the failing command + exit code.
2. Run `cab-bridge inspect <id> --json` for the relevant session.
3. Check `~/.claude/cli-agents-bridge/sessions/<id>/manifest.json` for unusual values.
4. Open an issue at the repo with the above + your Claude Code + cab-bridge versions.
