# Migration from Patil session-bridge

Step-by-step how-to for migrating active sessions from `PatilShreyas/claude-code-session-bridge` v0.1.0 to `cli-agents-bridge` v0.2.0.

---

## Prerequisites

- cli-agents-bridge v0.2.0 installed (`cab-bridge --version` returns `0.2.0` or newer).
- An existing Patil install with sessions in `~/.claude/session-bridge/`.
- No active Claude Code session currently using Patil bridge (close all listen/ask windows first).

Disk space: the migration writes a full backup of the source tree, so reserve ~2x the current `~/.claude/session-bridge/` size.

---

## Why migrate?

`cli-agents-bridge` resolves 9 bugs confirmed in the Patil source — see [PLAN.md §2](../PLAN.md#2-validazione-bug-upstream). Critically:

- **BUG-4** cross-project cleanup that wipes other projects' sessions on `/bridge stop` — fixed structurally by namespace separation.
- **BUG-3** multi-peer role routing — adds the `role` field missing in Patil v1.
- **BUG-1** heartbeat dead in listen loop — `lastHeartbeat` now updates every 30s.

After migration the source Patil tree is **never modified** — you can keep both plugins installed and uninstall Patil manually after verifying.

---

## Procedure

### 1. Dry-run first

```bash
cab-bridge migrate-from-patil --dry-run
```

Output is a JSON report:

```json
{
  "backupDir": "",
  "dryRun": true,
  "migrated": ["abc123 (dry-run)", "def456 (dry-run)"],
  "skippedExisting": [],
  "skippedInvalid": [],
  "errors": []
}
```

Review:

- `migrated` — sessions that WOULD be migrated.
- `skippedInvalid` — sessions rejected by SC-4 (invalid session ID) or RC-3 (`projectPath` contains `..`). These are NOT migrated and the original is left untouched.
- `errors` — manifest read failures (corrupt JSON). These do not block other sessions.

If `skippedInvalid` flags a session you actually need, manually fix the source manifest then re-run.

### 2. Real migration

```bash
cab-bridge migrate-from-patil
```

Steps performed by the subcommand:

1. **Backup**: `~/.claude/session-bridge/` is copied to `~/.claude/cli-agents-bridge/migration-backup-<YYYY-MM-DD-HHMMSS>/` with file modes re-tightened (`chmod 600` for files, `700` for dirs).
2. **Per-session migration**:
   - Read source `manifest.json`.
   - SC-4 validate `sessionId` (rejected if not `[a-z0-9]{6,32}`).
   - RC-3 reject `projectPath` containing `..` (security audit Sprint 0).
   - Transform schema v1 → v2: `schemaVersion=2`, `role="neutral"` (since v1 had no role), `agentName=projectName`, `pid=0` (no live PID inferable for legacy session).
   - Write target manifest atomically (`AtomicWriteJSON`).
   - Copy `inbox/*.json` and `outbox/*.json` preserving filenames with `chmod 600`.
   - Drop `.migrated` marker file inside target session dir.
3. **Source untouched**.

### 3. Verify

```bash
cab-bridge peers --json
```

Each migrated session shows up with `role: "neutral"` and `pid: 0`. After the next `register` (or your normal workflow) the role can be updated by re-registering with `--force-new --role=val` or `--role=esc`.

### 4. Re-run (idempotent)

```bash
cab-bridge migrate-from-patil
```

Sessions with a `.migrated` marker are skipped:

```json
{
  "skippedExisting": ["abc123", "def456"],
  ...
}
```

Safe to re-run any number of times.

---

## Rollback

The backup folder is your rollback point:

```bash
# Identify the backup folder
ls ~/.claude/cli-agents-bridge/migration-backup-*

# Remove migrated v2 sessions
rm -rf ~/.claude/cli-agents-bridge/sessions/

# Restore Patil tree (it was untouched, but if you accidentally deleted it)
cp -r ~/.claude/cli-agents-bridge/migration-backup-<ts>/sessions \
      ~/.claude/session-bridge/sessions
```

---

## Edge cases

- **Corrupted manifest** (invalid JSON): listed in `errors[]`, not migrated. Original is preserved in source tree.
- **Empty source tree** (`~/.claude/session-bridge/` missing): the subcommand reports "nothing to do" and exits 0. No-op.
- **Source on a different filesystem than target** (rare, e.g. NFS source / local target): atomic write within target is still same-fs because we use `os.CreateTemp(filepath.Dir(target), ...)`. Source-to-target copy is a plain `io.Copy`, not rename — no EXDEV risk.
- **Partial migration interrupted** (Ctrl-C mid-run): sessions written before interruption have their `.migrated` marker; a re-run skips them and picks up where it stopped. The backup folder is fully written before any session migration begins, so it is always complete or absent.

---

## Source override (for testing)

```bash
cab-bridge migrate-from-patil --patil-dir=/tmp/fake-patil/ --dry-run
```

Useful for testing migration against a staged tree without touching real `~/.claude/session-bridge/`. Used by the integration test suite.
