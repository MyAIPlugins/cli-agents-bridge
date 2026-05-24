# Manual smoke test — cli-agents-bridge v0.2.0

Pre-release checklist Alan runs (~45 min) before VAL tags `v0.2.0`. Each step has expected output / pass criteria. If any step fails, stop and file an issue.

**Environment**: macOS Darwin 25.x. Two terminal windows ("Window 1" / "Window 2") with their own working directory. Claude Code 2.1.150+ installed.

---

## Pre-flight (1 min)

```bash
# Verify Claude Code + Go
claude --version       # >= 2.1.150
go version             # >= 1.25.0

# Backup existing data
cp -r ~/.claude/cli-agents-bridge ~/.claude/cli-agents-bridge-smoketest-backup-$(date +%Y%m%d) 2>/dev/null || true

# Use isolated dev sandbox to avoid touching production data
export CAB_DATA_DIR=/tmp/cab-smoke-$$
```

All subsequent steps assume `CAB_DATA_DIR` is set.

---

## Setup (5 steps, ~5 min)

### 1. Plugin install via marketplace

In a Claude Code session:

```
/plugin marketplace add /Users/alan/develop/cli-agents-bridge
/plugin install cli-agents-bridge@cli-agents-bridge-marketplace
# scope: user
/cli-agents-bridge:cab
```

**Pass criteria**: slash command invokes binary, prints `cab-bridge 0.2.0 — multi-peer IPC bridge...` help with full subcommand list.

### 2. Register VAL in Window 1

```bash
cd /tmp/cab-smoke-proj-val   # new dir
mkdir -p .
cab-bridge register --role=val --agent-name=VAL-smoke
```

**Pass criteria**: JSON manifest on stdout with `role: "val"`, `agentName: "VAL-smoke"`, `pid: <current shell PID>`, `schemaVersion: 2`. Note the `sessionId` value — call it `VAL_ID`.

### 3. Register ESC in Window 2 (DIFFERENT cwd to test BUG-5)

```bash
cd /tmp/cab-smoke-proj-esc   # DIFFERENT dir
mkdir -p .
cab-bridge register --role=esc --agent-name=ESC-smoke
```

**Pass criteria**: JSON manifest with `role: "esc"`. Note `sessionId` as `ESC_ID`. Different from VAL_ID.

### 4. List peers from any window

```bash
cab-bridge peers
```

**Pass criteria**: tabwriter output with both VAL_ID and ESC_ID, status `ok`, heartbeat age < 60s for both.

### 5. Connect from VAL to ESC

```bash
# In Window 1 (VAL)
cab-bridge connect --session-id=$VAL_ID $ESC_ID
```

**Pass criteria**: JSON report `{"sender": {...val...}, "target": {...esc...}, "status": "connected"}`. VAL's `lastHeartbeat` is now within 5s of `date -u +%FT%T%Z` (BUG-9 wiring).

---

## Happy path (5 steps, ~5 min)

### 6. ask with --content inline

```bash
# In Window 1 (VAL)
cab-bridge ask --session-id=$VAL_ID --to=$ESC_ID --content="hello smoke test"
```

**Pass criteria**: stdout is exactly one message ID `msg-...` (12 hex chars). Capture as `MSG1`.

### 7. ask with --file payload

```bash
echo "this is a big briefing across many lines" > /tmp/cab-brief.md
echo "with quotes \"and\" 'apostrophes' that would break shell parsing inline" >> /tmp/cab-brief.md
cab-bridge ask --session-id=$VAL_ID --to=$ESC_ID --file=/tmp/cab-brief.md
```

**Pass criteria**: new `msg-...` ID. ESC's inbox now has 2 files.

### 8. status from ESC

```bash
# In Window 2 (ESC)
cab-bridge status --session-id=$ESC_ID
```

**Pass criteria**: JSON shows `inboxCount: 2`, `outboxCount: 0`, `processedCount: 0`, `stale: false`.

### 9. ESC listen + drain inbox

```bash
# In Window 2 (ESC), Ctrl-C after first message printed
cab-bridge listen --session-id=$ESC_ID
```

**Pass criteria**: messages emit as JSON one per line; the `--file` payload preserves quotes/apostrophes exactly. After Ctrl-C, `cab-bridge status` shows `inboxCount: 0` and `processedCount: 2`.

### 10. peers --json output parses cleanly

```bash
cab-bridge peers --json | python3 -m json.tool >/dev/null && echo OK
```

**Pass criteria**: prints `OK`. Output is valid JSON parsable by any jq/python.

---

## Edge cases (5 steps, ~10 min)

### 11. force-new collision (BUG-6)

```bash
# Window 3, same cwd as VAL register
cd /tmp/cab-smoke-proj-val
cab-bridge register --role=val --agent-name=VAL-clash
```

**Pass criteria**: exit 1, stderr contains `session already exists for project ... pid ... use --force-new`.

```bash
cab-bridge register --role=val --agent-name=VAL-forced --force-new
```

**Pass criteria**: succeeds, new sessionId.

### 12. ESC→ESC routing forbidden by default (BUG-3)

Register a second ESC:

```bash
cd /tmp/cab-smoke-proj-esc2
mkdir -p .
cab-bridge register --role=esc --agent-name=ESC-other
# Note ESC2_ID
cab-bridge ask --session-id=$ESC_ID --to=$ESC2_ID --content="should fail"
```

**Pass criteria**: exit 2 (or 1), stderr `esc→esc routing forbidden by default (use --allow-mesh to override)`.

```bash
cab-bridge ask --session-id=$ESC_ID --to=$ESC2_ID --content="explicit mesh" --allow-mesh
```

**Pass criteria**: succeeds, returns msg ID.

### 13. receive timeout exit 124 + stderr (BUG-7)

```bash
cab-bridge receive --session-id=$VAL_ID --msg-id=msg-nonexistent --max-deadline=2
echo "exit=$?"
```

**Pass criteria**: exit 124, error message on stderr (no stdout pollution). Verify:

```bash
cab-bridge receive --session-id=$VAL_ID --msg-id=msg-nonexistent --max-deadline=2 2>/dev/null
echo "exit=$?"
```

Same exit 124 but no error printed (stderr was suppressed). If error appears here, BUG-7 has regressed.

### 14. late reply recovery (BUG-2 non-loss)

In Window 1 (VAL):

```bash
cab-bridge ask --session-id=$VAL_ID --to=$ESC_ID --content="please reply slow"
# Capture msg ID as MSG_SLOW
cab-bridge receive --session-id=$VAL_ID --msg-id=$MSG_SLOW --max-deadline=3
# exit 124 expected
```

In Window 2 (ESC), WHILE Window 1 is still in receive:

```bash
# Wait for the receive in Window 1 to time out (~3s)
# Then manually craft a reply
cab-bridge listen --session-id=$ESC_ID
# Capture the query, then in another shell:
cab-bridge ask --session-id=$ESC_ID --to=$VAL_ID --content="late reply" --in-reply-to=$MSG_SLOW
```

In Window 1 (VAL), re-run with longer deadline:

```bash
cab-bridge receive --session-id=$VAL_ID --msg-id=$MSG_SLOW --max-deadline=60
```

**Pass criteria**: receive returns the JSON reply on stdout, exit 0. The reply was NOT lost despite the first timeout.

### 15. cleanup --scope=global confirm + migrate-from-patil dry-run

Confirm prompt:

```bash
cab-bridge cleanup --scope=global
# Prompt: Confirm global cleanup ... [y/N]:
# Type N — must abort
```

**Pass criteria**: exit ≠ 0, stderr `cleanup global: aborted by user`. No sessions removed.

Non-TTY (no force):

```bash
echo "" | cab-bridge cleanup --scope=global
echo "exit=$?"
```

**Pass criteria**: exit 3, stderr `global cleanup requires explicit confirmation (non-tty: pass --force)`.

migrate dry-run:

```bash
mkdir -p /tmp/cab-fake-patil/sessions/abc123
cat > /tmp/cab-fake-patil/sessions/abc123/manifest.json <<EOF
{"sessionId":"abc123","schemaVersion":1,"projectName":"legacy","projectPath":"/tmp/legacy","startedAt":"2026-01-01T00:00:00Z","lastHeartbeat":"2026-01-01T00:00:00Z","status":"active"}
EOF
mkdir -p /tmp/cab-fake-patil/sessions/abc123/inbox /tmp/cab-fake-patil/sessions/abc123/outbox
cab-bridge migrate-from-patil --patil-dir=/tmp/cab-fake-patil --dry-run
```

**Pass criteria**: JSON report with `dryRun: true`, `migrated: ["abc123 (dry-run)"]`. No target writes.

---

## Cleanup smoke environment

```bash
# Remove sandbox + uninstall plugin
unset CAB_DATA_DIR
# In Claude Code session:
# /plugin uninstall cli-agents-bridge@cli-agents-bridge-marketplace
# /plugin marketplace remove cli-agents-bridge-marketplace
```

---

## Pass/fail decision

- **All 15 steps pass** → VAL gates `v0.2.0` tag + release publication.
- **Any step fails** → file issue, do NOT tag. Sprint 5 fixup before release.

Sign-off:

```
Smoke test executed by: Alan
Date:                   YYYY-MM-DD
Claude Code version:    _____
cab-bridge version:     _____
Result:                 PASS / FAIL
Notes:                  _____
```
