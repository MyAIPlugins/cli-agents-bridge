# Privacy Notice

> Stub Sprint 0. Full GDPR checklist in [PLAN.md §9.6](./PLAN.md#96-gdpr--eu-compliance-checklist-local-only-data).

## TL;DR

**No data leaves the local machine.** `cli-agents-bridge` is a local IPC tool. All messages, manifests, and state are stored exclusively in `~/.claude/cli-agents-bridge/` on the user's filesystem with permissions 600/700.

## Data flow

| Data | Storage | Retention | Erasure |
|---|---|---|---|
| Session manifest (sessionId, role, agentName, projectPath, pid, timestamps) | `~/.claude/cli-agents-bridge/sessions/<id>/manifest.json` | Until cleanup (default 7d retention) | `cab purge --session <id>` |
| Inbox/outbox messages (JSON payload with content) | `~/.claude/cli-agents-bridge/sessions/<id>/{inbox,outbox}/*.json` | Until processed + retention window | `cab purge --session <id>` o `cab cleanup --retention=0` |
| Config | `~/.claude/cli-agents-bridge/config.json` | Persistent | Manual `rm` |
| Migration backup (one-time) | `~/.claude/cli-agents-bridge/migration-backup-<date>/` | Indefinito (utente decide) | Manual `rm -rf` |

## GDPR controls

- **Data minimization**: default `BRIDGE_RETENTION_DAYS=7`, configurable
- **Right to erasure**: `cab purge --session <id>` rm -rf safeguarded
- **Data localization**: zero data transfer over network. Zero telemetry. Zero analytics.
- **Logging minimal opt-in**: persistent transcripts default OFF, opt-in via config flag (v0.3+ candidate)
- **Transparent processing**: this document + SECURITY.md describe all data handling

## Caveat

Messages are stored in **plaintext** JSON files. Do not send credentials, secrets, or sensitive PII through the bridge. The file permissions (600) protect against other-UID readers but not against malware running as the same user (intrinsic Unix single-user model limit).
