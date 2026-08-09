# Security — cli-agents-bridge

Threat model, implemented controls, reporting policy, and known limitations for cli-agents-bridge (current through **v0.8.0**).

---

## Threat model

### In-scope

Single-user macOS / Linux workstation. Local-only IPC. The threats we defend against:

- **TM-1 — Malware on the same machine, different UID** reads `inbox/outbox` content (briefings, code, decisions). Vector: world-readable file modes.
- **TM-2 — Path traversal** via session-ID values used as path components. Vector: a corrupted or hand-crafted manifest with `sessionId = "../../etc/passwd"`.
- **TM-3 — TOCTOU on lock/manifest** in multi-process scenarios where N ESCs share the data dir. Vector: rename/check race.
- **TM-4 — Cross-session destructive cleanup** where one session wipes another (the original Patil BUG-4). Vector: shared sessions root + global glob. **Scope of the defence, stated precisely:** it covers *sessions* — removal is confined to the caller's project root, `--all-scopes` is opt-in, and live sessions are never touched. It does **not** cover the **retention purge**, which spans the entire data dir on purpose: `RetentionDays` is a data-minimisation policy (GDPR-1), and a retention window that only applied to whoever happened to run `cleanup` would leave another team's archived data past its declared lifetime forever. That reach is intended, it is announced on stderr with the day count and how many archived sessions were in it, and it is disabled by `retention_days = 0`.
- **TM-5 — Symlink attack on data dir creation** where an attacker plants `~/.claude/cli-agents-bridge` as a symlink to `/etc/`. Vector: weak initial dir creation.
- **TM-6 — Cross-session impersonation** where a session writes a manifest claiming to be another peer. Vector: missing ownership check on read. **Covered since v0.8.0 by SC-3** — before then the primary defence was the 0700 directory alone, which makes planting such a file impossible going forward but does not remove one planted while the perms were loose.

### Out-of-scope

Explicitly NOT defended against, with rationale:

- **Remote attacker**: zero network surface — no sockets opened. If the binary ever gets network features (v0.4+ Tailscale), threat model expands.
- **Malware running as the same UID**: Unix single-user model limit. The only mitigation would be OS-level sandboxing (macOS `sandbox-exec`, Linux seccomp), out of scope for a developer tool.
- **Supply chain on the plugin itself**: the marketplace install path is curated by the user. Verify the GitHub repo before adding.
- **Privilege escalation**: cli-agents-bridge never invokes anything as root, never uses setuid, never writes outside `~/.claude/cli-agents-bridge/` (when running as non-root).
- **Encryption-at-rest** vs same-disk attacker: theatre against FileVault / LUKS, which is the right layer.
- **Multi-tenant shared machine**: explicit non-goal of the design.

---

## Implemented security controls

### P0 — required since v0.2.0, active

- **SC-1 umask 077**: `syscall.Umask(0o077)` set in `cmd/cab-bridge/main.go init()` before any file/dir creation. Every file created by the binary is 0o600, every directory 0o700.
- **SC-2 dir perms 700**: `internal/session/manager.go::Register` enforces `os.MkdirAll(sessionDir, 0o700)` plus explicit `os.Chmod` for pre-existing dirs. Same enforcement in `internal/transport/fs/process.go::MoveToProcessed` for `processed/` and in cleanup archive paths.
- **SC-4 session-ID regex**: `internal/security/perms.go::ValidateSessionID` enforces `^[a-z0-9]{6,32}$`. Applied on every field that becomes a path component (`sessionId`, `from`, `to`, `inReplyTo`) at the message validation gateway and on every session-resolution path (`resolveSessionID`, `receive.go`, `migrate-from-patil`).
- **SC-5 atomic write**: `internal/transport/fs/atomic.go::AtomicWriteBytes` uses `os.CreateTemp(filepath.Dir(target), ...)` (same-filesystem guarantee) + `f.Sync()` + `os.Rename`. EXDEV surfaces as explicit error (no silent copy-fallback).

### P1 — active

- **SC-6 PID lock O_EXCL**: `internal/session/lock.go::AcquireLock` uses `os.OpenFile(lockPath, O_CREATE|O_EXCL|O_WRONLY, 0o600)`. Stale recovery via `syscall.Kill(pid, 0)`: `ESRCH` → remove + retry once; `EPERM`/`nil` → treat as live (ErrLockHeld). Re-entrant acquire from same PID returns a no-op release.
  - **Ownership model & BUG-6 guarantee scope** (Sprint 6 BUG-A): session liveness is tied to a long-running `cab-bridge listen`, which adopts the manifest PID at startup (`Manager.AdoptPID`). Collision detection (`ErrSessionExistsForProject`) is therefore **best-effort**: it reliably blocks a duplicate `register` for a project *whose session is owned by an active listener*, but a session with no live listener is treated as abandoned and re-`register` is permitted (it gets a fresh unique ID — sessions never merge, unlike Patil). This is intentional, not a security boundary: the lock prevents accidental concurrent ownership, not a determined same-UID actor (out of scope). See `docs/troubleshooting.md`.
- **SC-7 base dir integrity check at boot**: `cmd/cab-bridge/common.go::bootstrapDataDir` runs on every subcommand (via `loadConfigOrFail`, plus an explicit call in `receive.go`) before any session file is touched. It `os.Lstat`-s the base dir and: creates it 0o700 on first run; FATAL on symlink (TM-5, never auto-repaired); FATAL on non-directory; FATAL on owner mismatch; WARN + chmod 0o700 on loose perms. Operates on the absolute `DataDir` resolved by `config.Load` (`filepath.Abs`), so the check and every `filepath.Join` target the intended directory.

- **SC-3 ownership check (active since v0.8.0)**: every read inside our data dir verifies that the file belongs to the current UID, via `fstat` on the **descriptor used for the read** (`internal/transport/fs/atomic.go::ReadJSON` opens once, stats that fd, and reads from it) — so there is no TOCTOU window between the check and the use.
  - **Covered**: manifests (`LoadManifest`), messages in `inbox/`/`outbox/`/`processed/`, session state (`wake-cursor.json`, `listener.json`, `reply-txn.json`, the lock file, `notify-watch/<name>.json`), and `config.json`. In short: **everything inside our data dir**.
  - **Not covered**: the Patil data dir (`~/.claude/session-bridge/`), read only by `migrate-from-patil`. It is a perimeter we did not create, with permissions we do not set; refusing to read it would mean a migration that cannot read its own source.
  - **On mismatch**: hard failure for the caller's own manifest and for messages being consumed — those decide who you are and what you do. During *enumeration* of other sessions (`peers`, name resolution) the offending file is **skipped with a warning naming the path**, because a diagnostic command that dies on the anomaly it exists to surface is useless exactly when needed. Unreadable/corrupt JSON keeps its previous silent skip: a broken file is a mess, not a claim about identity. A missing file returns `os.ErrNotExist`, never `ErrOwnershipMismatch` — several callers treat absence as a normal race.
  - **Skipped for root**, consistently with SC-7.
  - **Cost, measured** (`peers` over 4 sessions, 20 runs): 0.138s with the check vs 0.125s without — **~0.6 ms per invocation**. It is one `fstat` on an already-open descriptor, next to a read and a JSON parse that happened anyway.
  - **What it is actually for.** With the base dir at 0700 and owned by us (SC-1/SC-2/SC-7), another user *cannot create* a file in there — writing into a directory requires permission on the directory, so a foreign manifest is impossible, not merely unlikely. The real window is different, and SC-7 documents it by existing: when the base dir is found with loose perms, SC-7 warns, tightens to 0700 and **proceeds** — it shuts the door but does not remove whoever already walked in. A manifest planted during that window survives and is read; it carries an `agentName` (which intercepts messages resolved by name in scope) and a `projectPath` (which participates in `LongestPrefixLookup`, i.e. in deciding who *you* are). SC-3 is the remediation of that window, not a generic anti-spoofing layer.

### P2 — still deferred at v0.8.0

_SC-3 moved to the active list above at v0.8.0; only SC-8 remains deferred._

- **SC-8 PII detection**: explicitly NOT implemented. Regex on content for "looks like credit card / email" is false-positive prone and adds runtime cost without addressing the actual threat (same-UID malware reading plaintext). PRIVACY.md warns users not to send secrets.

> **Honesty note (through v0.8.0)**: this document describes controls as actually wired in the shipped binary, verified against the code at each release rather than assumed. SC-3 sat under "deferred" from v0.2.0 to v0.7 — seven releases — precisely because the primitive existed and nothing called it; it moved to the active list only when the call-sites landed. SC-8 stays deferred for the same reason. We would rather under-claim than assert a control that is not on the live code path.

---

## Reporting vulnerabilities

If you find a security issue:

1. **Email**: firstcontact@alancurtisagency.com
2. **Subject**: `[security] cli-agents-bridge: <short description>`
3. Include:
   - Affected version (`cab-bridge --version`)
   - Reproduction steps
   - Impact assessment
4. **Disclosure timeline**: 90-day responsible disclosure. We aim to ship a fix within 30 days and credit reporters in CHANGELOG.md unless anonymity is requested.

Avoid filing public GitHub issues for security topics until coordinated disclosure.

---

## Known limitations

- **NFS-mounted home dir**: `CheckOwnership` reads `stat().Uid`; NFS may return synthetic UIDs that do not match local `Getuid()`. Documented limitation, no MVP fix. Workaround: use a local data dir via `CAB_DATA_DIR=/var/tmp/cab-$USER/`.
- **Same-UID malware**: see Threat model out-of-scope.
- **Path-traversal via reading attacker-controlled JSON**: only `sessionId` and message IDs are SC-4 validated as path components. The `projectPath` field of a Patil v1 manifest could carry `..` — `migrate-from-patil` explicitly rejects such manifests (RC-3). New v2 manifests are written by us under our control.

---

## See also

- [docs/security-model.md](./docs/security-model.md) — detailed threat decomposition with attack paths
- [PLAN.md §9](./PLAN.md#9-security-baseline-fix-5) — design rationale
- [PRIVACY.md](./PRIVACY.md) — data flow + GDPR
