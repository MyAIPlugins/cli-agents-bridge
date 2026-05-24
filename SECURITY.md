# Security Policy

> Stub Sprint 0. Full threat model and controls live in [PLAN.md §9](./PLAN.md#9-security-baseline-fix-5).

## Threat model (in-scope vs out-of-scope)

**In-scope** (single-user macOS/Linux, local-only):
- TM-1: malware locale *UID diverso* legge inbox/outbox
- TM-2: path traversal via session ID injection
- TM-3: TOCTOU su lock/manifest in scenario multi-ESC
- TM-4: cleanup cross-session distruttivo (risolto via namespace separato §3.4)
- TM-5: symlink attack su directory creation
- TM-6: cross-session impersonation (manifest spoofing)

**Out-of-scope MVP**:
- Attaccante remoto (zero network surface)
- Malware *stesso UID* (limite intrinseco modello Unix single-user, richiede sandbox OS-level)
- Supply chain attack sul plugin (fuori baseline)
- Privilege escalation (single-user workflow)
- Encryption end-to-end (single-user single-disk → FileVault disk-level)
- Multi-tenant shared machine

## Security controls implementati (v0.2.0 MVP)

### P0 (obbligatori)
- **SC-1** `umask 077` in `cmd/cab-bridge/main.go` init
- **SC-2** dir permissions 700 enforced on creation
- **SC-3** ownership check pre-read/write (UID match `Getuid()`)
- **SC-4** session ID regex validation `^[a-z0-9]{6,32}$` (path traversal prevention)
- **SC-5** atomic write same-filesystem + `fdatasync` + rename (no `EXDEV` cross-fs)

### P1 (importanti)
- **SC-6** lock file PID safe (`O_EXCL` + `kill -0` stale recovery)
- **SC-7** base dir integrity check at boot (no symlink, perms 700, owner=Getuid)

## Reporting vulnerabilities

Open a GitHub issue (private security advisory if available) describing:
- Reproduction steps
- Affected version
- Impact assessment

Contact: advertalis@gmail.com
