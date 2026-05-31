# Privacy Notice — cli-agents-bridge

`cli-agents-bridge` is a **local-only single-user** IPC tool. This document describes what data is stored on disk, where, for how long, and how to remove it.

**TL;DR**: No data leaves your machine. Zero telemetry, zero analytics, zero network calls. All state is plaintext JSON in `~/.claude/cli-agents-bridge/` with Unix permissions 600 (file) / 700 (directory).

---

## What gets stored

| Data | Location | Created by | Removed by |
|---|---|---|---|
| Session manifest | `~/.claude/cli-agents-bridge/sessions/<id>/manifest.json` | `register` | `cleanup` (own/global) |
| Incoming messages | `~/.claude/cli-agents-bridge/sessions/<id>/inbox/*.json` | peer `ask` | `listen` (move to `processed/`) |
| Outgoing messages | `~/.claude/cli-agents-bridge/sessions/<id>/outbox/*.json` | n/a in v0.2 (reserved) | `cleanup` |
| Consumed messages | `~/.claude/cli-agents-bridge/sessions/<id>/processed/*.json` | `listen` post-consume | `cleanup` (move to `archive/`) |
| Archived messages | `~/.claude/cli-agents-bridge/archive/<YYYY-MM-DD>/<id>/*.json` | `cleanup` pre-delete | retention sweep (`RetentionDays`, default 7) |
| PID lock | `~/.claude/cli-agents-bridge/sessions/<id>/lock` | `register` | session exit / `cleanup` |
| Config | `~/.claude/cli-agents-bridge/config.json` | user (optional) | manual `rm` |
| Migration backup | `~/.claude/cli-agents-bridge/migration-backup-<ts>/` | `migrate-from-patil` | manual `rm` (user retains audit copy) |

---

## GDPR controls

### GDPR-1 Data minimization

`config.RetentionDays` (default `7`) caps how long archived messages live on disk. After that, retention sweep at every `cleanup` run removes archive folders older than the window.

Override via `CAB_RETENTION_DAYS=N` env var, or in `~/.claude/cli-agents-bridge/config.json`:

```json
{"retention_days": 14}
```

### GDPR-2 Right to erasure

Remove a single session and all its data:

```bash
cab-bridge cleanup --session-id=<id>
```

Remove every stale session (interactive confirmation required):

```bash
cab-bridge cleanup --scope=global
```

Remove everything cli-agents-bridge has ever stored:

```bash
rm -rf ~/.claude/cli-agents-bridge/
```

### GDPR-3 Data localization

No data leaves your machine. The binary makes zero network connections. Verifiable with `lsof -p <pid>` or `tcpdump` while a session is active — no sockets opened beyond the standard process tooling.

### GDPR-4 Logging is opt-in

Persistent transcripts (proposal feature for v0.3.0) will be **off by default**. The only logs in v0.2.0 are stderr warnings during runtime (printed to terminal, never persisted).

### GDPR-5 Trasparency

This document plus [SECURITY.md](./SECURITY.md) describe the complete data lifecycle. No DPA is required for a single-user developer tool.

---

## Caveat — plaintext storage

Messages are stored in **plaintext JSON files**. They are protected against other-UID readers by file permissions (`chmod 600`), but a malware running as the same user as cli-agents-bridge can read them. This is the inherent Unix single-user model limit — only OS-level sandboxing (e.g. `sandbox-exec` on macOS) can mitigate.

**Recommendation**: do not send credentials, secrets, or sensitive PII through the bridge. Use it for code, briefings, and design context as intended.

For encryption-at-rest (a v1.0+ candidate), see PLAN.md §5 v1.0.

---

## Contact

Privacy questions: firstcontact@alancurtisagency.com
