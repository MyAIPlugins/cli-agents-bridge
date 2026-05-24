# Security model — detailed threat decomposition

Companion to [SECURITY.md](../SECURITY.md). Each in-scope threat is decomposed into the concrete attack path, the implemented control, and the residual risk after mitigation.

---

## TM-1 — Same-machine, different-UID malware reads inbox/outbox

**Attack path**: a process running as user `mallory` on the same workstation finds `~/alice/.claude/cli-agents-bridge/sessions/<id>/inbox/` world-readable and `cat`s the JSON files containing alice's briefings (which may quote proprietary code or business context).

**Control**:

- **SC-1** umask 077 set in `init()` before any file creation
- **SC-2** every `mkdir` uses 0o700 + explicit `os.Chmod` for pre-existing dirs
- **SC-5** every file write produces 0o600 via the atomic write helper

**Residual risk**: mallory could still observe the existence/timing of `inbox/*.json` files via `ls` on a world-executable parent path. SC-2 protects `~/.claude/cli-agents-bridge/` at 0o700, so `ls` on it from another UID fails with EACCES — neither contents nor file names are leaked.

---

## TM-2 — Path traversal via session-ID injection

**Attack path**: a corrupted manifest, a hand-crafted `--session-id` flag, or a JSON message with `inReplyTo: "../../etc/passwd"` would cause downstream code to construct a path that escapes the data dir.

**Control**:

- **SC-4** `security.ValidateSessionID(id)` with regex `^[a-z0-9]{6,32}$` rejects anything containing path separators, traversal sequences, mixed case, or NUL bytes.
- Applied at every entry where untrusted text becomes a path component: subcommand `--session-id` flags, JSON field decoding (`from`, `to`, `inReplyTo`), `inspect <id>` positional.

**Residual risk**: a callsite that bypasses `ValidateSessionID` (e.g. future code constructing a path from `projectPath` without validation). Lint/code-review discipline + `internal/security/perms.go` as the only validation surface (callsites greppable).

---

## TM-3 — TOCTOU on lock and manifest

**Attack path**: two `register` calls race on the same `lockPath`. Without `O_EXCL` both could observe "no lock exists" then both create it, ending with one stale + one live.

**Control**:

- **SC-6** `os.OpenFile(lockPath, O_CREATE|O_EXCL|O_WRONLY, 0o600)`. Atomic create-or-fail. The OS guarantees exactly one caller succeeds.
- Stale recovery: read PID, `syscall.Kill(pid, 0)`. ESRCH → remove + retry once (`tryCreate` is called recursively but only retries one time, preventing infinite loops). EPERM/nil → live, return ErrLockHeld.

**Residual risk**: NFS-mounted data dir where the `O_EXCL` semantics depend on the server. Documented in PLAN.md and SECURITY.md as out-of-scope (NFS is not a recommended deployment).

---

## TM-4 — Cross-session destructive cleanup

**Attack path**: a `cleanup` invocation from project A wipes session state belonging to project B (Patil BUG-4, empirically observed 2026-05-24 with chatterence-bi-template wiping ac-agents).

**Control**:

- **§3.4 namespace separation**: cli-agents-bridge uses `~/.claude/cli-agents-bridge/` while Patil uses `~/.claude/session-bridge/`. The two cannot interfere via shared sessions root.
- **cleanup scope semantics**: `cab-bridge cleanup` defaults to `--scope=my-session`. The `--scope=global` path requires interactive TTY confirmation (or `--force` in scripted contexts) and operates only on sessions whose `lastHeartbeat` is older than `cfg.StaleSeconds` — live peers protected by BUG-1 heartbeat goroutine.

**Residual risk**: a user explicitly running `cab-bridge cleanup --scope=global --force` from a script with no peers running could wipe state they hadn't expected. Mitigated by retention sweep archiving processed messages before delete (`archive/<date>/<sid>/`).

---

## TM-5 — Symlink attack on data dir creation

**Attack path**: an attacker plants `~/.claude/cli-agents-bridge` as a symlink to `/etc/` (or another sensitive target) before the user runs cab-bridge. The first `MkdirAll` would silently traverse the symlink, with subsequent writes landing in the attacker's chosen path.

**Control**:

- **SC-7** boot-time check: `os.Lstat(baseDir)` rejects symlinks at the data root. Perms verified at 0o700; auto-fix if open (with stderr warning).
- **SC-2** all subdir creations specify mode 0o700 from the start — even if the root check is bypassed, downstream `Stat_t.Mode` reveals tampering.

**Residual risk**: attacker controls a parent directory of `~/.claude/`. If `~/.claude/` itself is symlinked, SC-7 catches it only when we reach the `cli-agents-bridge` subdir. Users with co-tenant access to `$HOME` have larger problems than this tool can address.

---

## TM-6 — Cross-session impersonation via manifest spoofing

**Attack path**: a malicious process writes a manifest at `~/.claude/cli-agents-bridge/sessions/<spoofed-id>/manifest.json` with `role: "val"` and high-trust fields, then sends messages claiming to be VAL.

**Control**:

- **SC-3 ownership check**: before reading any manifest belonging to another session, `CheckOwnership(manifestPath)` rejects files not owned by `os.Getuid()`.
- **SC-1/SC-2/SC-5** combine to make the attacker unable to plant such a file in the first place (other-UID malware cannot write under our 0o700 dir).

**Residual risk**: same-UID attacker. See TM-1 same-UID residual.

---

## Out-of-scope rationale

### Same-UID malware (general)

The Unix permission model gives same-UID processes identical access. Defending against this requires OS-level sandboxing (`sandbox-exec`, seccomp, AppArmor). For a developer tool the user runs interactively, this protection layer belongs to the OS, not the application.

### Encryption-at-rest

Two scenarios:

1. **Disk seizure / laptop theft**: the right answer is full-disk encryption (FileVault on macOS, LUKS on Linux). Applying app-level encryption on top is redundant and creates key-management burden the user must shoulder.
2. **Same-UID malware reading plaintext**: encryption helps only if the key isn't accessible to the same UID. Storing the key in a Keychain item gated by Touch ID would do it, but introduces platform coupling we deferred to v1.0.

### Network attacker

The binary makes no network calls. `lsof -p <pid>` while a session is active shows only stdio + the file handles for `~/.claude/cli-agents-bridge/`. The v0.4 daemon work would add a Unix domain socket; even then, socket lives at `/tmp/cab-$UID/cab.sock` with 0o600 perms.

### Supply-chain on the binary itself

Out of scope for the security baseline of v0.2.0. Users install via `/plugin marketplace add myAIPlugins/cli-agents-bridge` — verifying the GitHub org's authenticity is the same trust step as for any third-party plugin. The marketplace registers a versioned commit SHA, providing some tampering detection.

---

## Code references (control surface)

| Control | File | Symbol |
|---|---|---|
| SC-1 | `cmd/cab-bridge/main.go` | `init()` umask call |
| SC-2 | `internal/session/manager.go` | `Manager.Register` mkdir+chmod |
| SC-3 | `internal/security/perms.go` | `CheckOwnership` |
| SC-4 | `internal/security/perms.go` | `ValidateSessionID` |
| SC-5 | `internal/transport/fs/atomic.go` | `AtomicWriteBytes` |
| SC-6 | `internal/session/lock.go` | `AcquireLock` + `tryCreate` |
| SC-7 | (Sprint 4 wiring — currently library-only via `EnforceDirPerms`) | `internal/security/perms.go` |
