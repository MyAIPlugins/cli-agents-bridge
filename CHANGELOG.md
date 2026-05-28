# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Sprint 6 — 2026-05-29 (fixup post-smoke, pre-tag)

#### Fixed
- **BUG-A** (session liveness): `register` is a one-shot command whose PID dies on return, so the BUG-6 collision check (`isProcessAlive`) never fired and sessions appeared STALE outside `listen`. `Manager.AdoptPID` is now called at `listen` startup so the long-running listener writes its own live PID into the manifest. Collision detection and stale detection observe a live owner during an active listen. `Touch` (connect path) deliberately unchanged. Caught by the manual smoke test, invisible to in-process unit tests.
- **BUG-B** (JSON hygiene): `cleanup.Result`, `migrateReport`, and `collectPeers` emitted nil slices serialising as JSON `null` instead of `[]` (breaking `jq '... | length'`). Slices initialised to `[]T{}` and the three `ErrNotExist` early-returns that overwrote the init with nil are fixed.

#### Changed
- `SECURITY.md` SC-6, `README.md` features, `docs/troubleshooting.md`: BUG-6 collision guarantee documented as **best-effort** (reliable against a live `listen` owner; a session with no listener is treated as abandoned and re-`register` is allowed, getting a fresh unique ID — sessions never merge). README "Security baseline" row corrected: removed "ownership check" (SC-3 is deferred v0.2.1).
- `commands/cab.md`: removed stale "Sprint 1 placeholder / Sprint 3" references.
- `tests/smoke-test.md`: pre-flight `CAB_DATA_DIR` fixed value `/tmp/cab-smoke-shared` (not `$$`, which differs per shell and isolates namespaces) + `make build && make install-dev` step + PATH-injection note.
- Plugin version `0.2.0-dev` → `0.2.0` in `plugin.json` + `marketplace.json`.

#### Tests
- +5 (AdoptPID writes live PID; integration: listen adoption enables real register collision; cleanup/peers/migrate empty buckets serialise as `[]`). Full suite 158 PASS / 0 FAIL under `-race`.

### Sprint 5 — 2026-05-28 (security hardening pre-tag)

#### Security
- **SC-7 base-dir integrity check** (`cmd/cab-bridge/common.go::bootstrapDataDir`): closes the declared-but-absent SC-7. Lstat-based, wired into all 10 subcommands. First-run creates dir 0o700; symlink / non-directory / owner-mismatch are FATAL (TM-5); loose perms trigger warn + chmod 0o700. Never auto-repairs a symlink.
- **DataDir absolute resolution** (`internal/config/config.go::Load`): a relative `CAB_DATA_DIR` or `data_dir` is resolved via `filepath.Abs` (+ warning) — prerequisite so SC-7 and every `filepath.Join` target the intended directory.
- **migrate-from-patil integrity** (`migrate.go`, `manager.go`): rejects manifests whose internal `sessionId` diverges from the directory name + re-validates it (SC-4); `migrateOne` calls `mf.Validate()` before writing (fail-closed); `LongestPrefixLookup` returns the directory name instead of the attacker-influenceable `mf.SessionID` (closes latent path-component propagation).

#### Changed
- `SECURITY.md` reconciled with the shipped binary: SC-7 documented as wired (now true); SC-3 (`CheckOwnership`) moved from "implemented P0" to "deferred v0.2.1" — the primitive exists and is tested but is not yet invoked at read call-sites. Primary TM-1/TM-6 defense in v0.2.0 is SC-1 + SC-2 + SC-7. Honesty note added (under-claim > over-claim).

#### Tests
- +12 (5 bootstrapDataDir unit, 2 config absolute-path, 1 LongestPrefixLookup NEW-1 regression, 2 SC-7 integration wiring, 2 migrate integrity). Full suite 153 PASS / 0 FAIL under `-race`.

#### Deferred to v0.2.1
- SC-3 ownership wiring via fstat-on-fd helper (closes Stat-vs-Lstat TOCTOU too), `os.Root` symlink-safe session creation, inbox backpressure bound, `scanForReply` idempotent-on-already-consumed.

### Sprint 4 — 2026-05-25 (release readiness)

#### Added
- 5 end-to-end integration test scenarios (PLAN §7.3): 1V+1E roundtrip, 1V+2E+observer routing enforcement, long-run heartbeat persistence (compressed), cross-project cleanup safety, migrate-from-patil dry-run + idempotent + RC-3 traversal reject + connect BUG-9 cmd-level
- `cab-bridge connect <peer-id>` subcommand — cmd-level wiring of `Manager.Touch` (BUG-9 fix end-to-end): refresh own heartbeat pre-handshake + `routing.ValidateSendPair` enforcement
- 8 production docs (vs Sprint 0 stub): README quickstart + feature parity table + roadmap; PRIVACY GDPR-1..5 with concrete commands; SECURITY threat model + 7 SC controls + disclosure policy; docs/migration-from-patil.md how-to with rollback; docs/multi-esc-patterns.md with hub-and-spoke vs mesh use cases; docs/security-model.md detailed TM-1..6 decomposition + code refs; docs/dev-conventions.md Go style + commit + test patterns; docs/troubleshooting.md FAQ
- tests/smoke-test.md — 15-step manual checklist (~45 min Alan-time) covering 5 setup + 5 happy + 5 edge cases (force-new collision, ESC→ESC forbidden, BUG-2 late reply recovery, BUG-7 stderr+exit124, cleanup global confirm + migrate dry-run)
- schemas/manifest-v2.schema.json + schemas/message-v2.schema.json — JSON Schema 2020-12 references with regex/enum constraints matching internal/security + internal/message validation gateway

#### Changed
- `cmd/cab-bridge/main.go` version bump 0.2.0-dev → **0.2.0** (release-ready)

## [0.2.0] — 2026-05-25

First public release of cli-agents-bridge — fork of `PatilShreyas/claude-code-session-bridge` v0.1.0 with 9 confirmed bugs fixed structurally, role-based routing, namespace-isolated storage, security baseline P0/P1, single Go static binary distribution via self-marketplace GitHub.

Cumulative bug coverage (BUG-1..BUG-9 all FIXED with regression tests):
- BUG-1 heartbeat dead in listen loop → Manager.StartHeartbeat goroutine
- BUG-2 receive timeout secco → ReceiveReply long-poll + late reply recoverable
- BUG-3 multi-peer routing senza role → routing.ValidateSendPair hub-and-spoke
- BUG-4 cleanup globale cross-project → namespace separato + scope-aware cleanup
- BUG-5 get-session-id parent fallback → LongestPrefixLookup with bestLen tracking
- BUG-6 session ID collision per cwd → O_EXCL lock + ErrSessionExistsForProject
- BUG-7 errore stdout invece stderr → exitFromErr stderr + exit 124 timeout
- BUG-8 STALE_SECONDS inconsistente → cfg.StaleSeconds unica fonte
- BUG-9 connect-peer no heartbeat → Manager.Touch + connect subcommand

Test stats: 100+ sub-tests across 8 packages, all pass with `-race` detector. 5 integration scenarios end-to-end + 9 regression tests (one per BUG).

See [PLAN.md](./PLAN.md) for full design rationale, [SECURITY.md](./SECURITY.md) for threat model, [PRIVACY.md](./PRIVACY.md) for GDPR data flow.

### Sprint 3 — 2026-05-24 (MVP feature-complete)

#### Fixed
- **BUG-3** Multi-peer routing senza role disambiguation — `internal/routing/role.go::ValidateSendPair` hub-and-spoke val-centric + `--allow-mesh` override + observer-cannot-send STRUTTURALE (no flag override)
- **BUG-4** Cleanup globale cross-project — `internal/cleanup/scope.go` con scope=my-session default + scope=global TTY confirm + ErrConfirmRequired exit 3 non-tty + pre-delete archive `archive/<date>/<id>/` + retention sweep GDPR-1
- **BUG-8** STALE_SECONDS inconsistente — `config.StaleSeconds` unica fonte verità per peers cmd + cleanup library (zero hardcoded)
- **BUG-9** `connect-peer.sh` heartbeat sender — `Manager.Touch(sessionID)` single-shot heartbeat refresh

#### Added
- Inbox policy migration A→B: `poll.go` refactor `os.Remove` → `MoveToProcessed(processedDir)` con RFC3339 timestamp prefix (audit trail + recovery semantics + foundation transcript v0.3)
- 8 CMD subcommand suite: `register`, `listen`, `ask`, `peers`, `cleanup`, `status`, `inspect` (--json default per BUG R8 jq removal mitigation), `migrate-from-patil`
- `cmd/cab-bridge/common.go::exitFromErr` centralized error→exit mapping (124 timeout / 3 confirm-required / 1 validation / 0 success)
- `migrate-from-patil` subcommand Go: backup pre-migration + dry-run + idempotent (.migrated marker) + `--patil-dir` test injection + SC-4 path validation RC-3

### Sprint 2 — 2026-05-24

#### Fixed
- **BUG-2** `bridge-receive.sh` timeout secco — `ReceiveReply` long-poll fino a `--max-deadline` + preserva non-matching messages in inbox (recovery superior a Patil)
- **BUG-7** error su stdout — stderr-only + ErrTimeout sentinel → exit code 124 coreutils convention

#### Added
- Message schema v2 + `DecodeStrict` (gateway DisallowUnknownFields) / `DecodeLenient` (runtime read forward-compat schema additive) pattern
- Filesystem polling con `time.Ticker` + context cancellation + done channel idiom
- `cab-bridge receive --msg-id=X --max-deadline=N` subcommand
- `inReplyTo *string` pointer per Go-idiomatic JSON null semantics

### Sprint 1 — 2026-05-24

#### Fixed
- **BUG-1** Heartbeat dead in listen loop — `Manager.StartHeartbeat(ctx, sessionID) <-chan struct{}` con `time.Ticker` + done channel idiom
- **BUG-5** `get-session-id.sh` parent fallback — `Manager.LongestPrefixLookup` con `bestLen` + trailing-separator guard
- **BUG-6** Session ID collision per cwd — Lock O_EXCL atomic + `kill -0` ESRCH/EPERM stale recovery + ForceNew flag + holder PID in error message

#### Added
- Layout Patil-style refactor: `plugins/cli-agents-bridge/` subdir + Makefile `install-plugin` target
- Atomic write helper `internal/transport/fs/atomic.go`: same-dir mktemp + `f.Sync()` + Rename + EXDEV explicit "config bug not transient"

### Sprint 0 — 2026-05-24

#### Added
- Initial Go module baseline + security primitives P0 (umask 077, perms 700/600, ownership check, session ID regex validation)
- Day 0 FIX-4 spike: empirical verification of distribution path → Esito A definitivo self-marketplace
- Repo structure: cmd/, internal/, commands/, schemas/, tests/, config/, docs/
- CI matrix: cross-compile darwin-arm64 + linux-amd64 + linux-arm64 (no cgo)
- Docs stub: README, LICENSE MIT, ARCHITECTURE, CHANGELOG, PRIVACY, SECURITY

## [0.2.0] — TBD (Sprint 4 release)

Production release: integration test 5 scenari + docs production-ready + smoke test Alan + tag.
