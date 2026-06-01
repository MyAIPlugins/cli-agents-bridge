# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.5.0] — 2026-06-01

*AI-friendly under stress* (LL-13/LL-14): reduce the surface of opaque ids/artifacts an agent must handle by hand — zero-config onboarding, id-less wake, content/state without digging, and guards against the crossings/duplicates a degraded agent produces. Built end-to-end via the cab-bridge dogfooding workflow (VAL↔ESC over the bridge itself, hands-free via the first-arriver-listens pattern); independent VAL gate `go test -race -count=1 ./...` green + a real smoke per feature; **validated in real use** (real-estate field session, 5 prod deploys: `overview` "the single most useful upgrade", `state` "gold", the bridge "made itself forgotten").

### Added
- **F-40 — `cab-bridge bootstrap --role=<val|esc>`** (zero-config pairing): a one-shot bootstrap that discovers an already-registered peer in the project scope **in-process** (`collectPeers`, no pipe → F-16 by design), derives its own name adaptively (inherits the peer's suffix — `VAL-x` ↔ `ESC-x` — or falls back to `<ROLE>-<scope-basename>`, converging in either order), registers idempotently (`--resume`, F-27), and then: for `val` sets `state=orchestrating` + exits, for `esc` hands off to `listen --wait-one`. Eliminates the manual register+peers+naming dance — the riskiest phase for a fresh agent.
- **F-41 — scope is the git REPOSITORY (git-common-root), not the physical checkout**: a linked `git worktree` resolves to its main repo, so a VAL at the repo root and an ESC in a worktree of the SAME repo share one scope and pair in plain `peers` — no flags. `resolveScope` symlink-canonicalizes (`EvalSymlinks`) so a repo reached through a symlink (e.g. macOS `/tmp` → `/private/tmp`) pairs too. Different git repos keep distinct scopes (isolated). `FindProjectRoot` stays a lexical walk; canonicalization happens downstream in its sole caller.
- **F-42 — `cab-bridge overview`**: me + paired peer + pending inbox in ONE call, no `--session-id`, human-readable (`--json` for scripting), worktree-aware. Collapses the `peers` + `whoami` + inbox-listing dance into one scannable view.
- **F-36 — `cab-bridge receive --any`**: id-less wake — blocks until the first batch of any non-ack message arrives (no `--in-reply-to` to fabricate), drains and archives it, emits NDJSON; an empty-window timeout exits 0 with `{"status":"timeout"}`. `type=ack` never wakes it (left in inbox as the F-12 delivery signal). The robust wake for an orchestrator with nothing specific to await. Mutually exclusive with `--msg-id`.
- **F-48 — `cab-bridge read <msg-id>` + `--emit=content`**: `read` prints a message's content body (`--json` for the full message) located by id in `inbox/` or `processed/` WITHOUT consuming it — matching on the decoded id so it finds archived messages despite their timestamp-prefixed filename. Replaces the `find … | python3` dance for a body the `inbox --list` preview (80 runes) truncates. `--emit=content` on `listen`/`receive` emits the body only (zero-parsing wakes; default `json` unchanged, timeout payload preserved). New exported `message.ValidateMessageID`.
- **F-43 — `ask` duplicate guard**: warns on stderr (and sends anyway) when an identical `(to, type, content)` message was sent to the same peer within `DedupWindowSeconds` (default 10, env `CAB_DEDUP_WINDOW_SECONDS`, ≤0 disables); `--skip-duplicate` skips the resend and prints the ORIGINAL id (caller idempotency). Defends against a degraded agent re-invoking `ask` before the first send's stdout returned (string-equality match, no hash, at outbox scale). The guard lives only in `runAsk` — the shared `sendMessage`/auto-ack is unaffected.
- **F-34 — unread-peer warning before `ask`**: warns on stderr (never blocks — sends anyway) when the recipient sent a still-unread non-ack message AFTER our last message to them — the dominant cause of VAL/ESC crossings (replying on a stale snapshot). Zero new state (inbox + outbox already present); the warning cites the id so the caller can `cab-bridge read <id>` first. Symmetric — fires for any sender.

### Changed
- **Skills realigned to v0.5**: the public `bridge-workflow` and the companion `cab-bridge-awareness` now cover `bootstrap`, scope=git-repo (worktree pairing), `overview`, `receive --any`, `read`/`--emit`, `state working/done` in the executor loop, and first-arriver-listens. The id-rule is refined: capturing an id from command output and reusing it is robust — only **inventing** one from memory is the hazard.

## [0.4.0] — 2026-05-31

Reliable wake/delivery cycle + inbox tooling + agent-state observability + idempotent reconnect. Built end-to-end via the cab-bridge dogfooding workflow and **validated in real use** (chatterence-bi field test: F-21/F-22/F-23a/F-26 confirmed on the job — "the bridge is now pleasant to use for an orchestrator, not just reliable"). Independent VAL gate `go test -race -count=1 ./...` green at each step + per-fix real smoke. Distilled from 8 independent dogfooding agent-voices (VPS + game + BI security review). Follow-up findings (F-34 conversation cursor, F-35 inbox filters, F-36 receive --any, F-23b read-receipt) deferred to a later version.

### Fixed
- **F-30 — `receive` no longer deletes the consumed reply, it archives it**: `scanForReply` did `os.Remove` on a match (asymmetric to `listen`/`DrainInboxOnce` which `MoveToProcessed`). With a background `receive` whose caller missed stdout, the reply was then gone from the inbox and survived only in the SENDER's outbox → recovery from someone else's path. Now it is moved to the receiver's own `processed/` dir, so recovery is from home. At-most-once preserved across concurrent callers (a lost archive race — `ErrNotExist` — keeps scanning instead of handing the reply out twice; EXDEV/permission returns the message anyway since the caller is blocking on exactly it). `send.go` untouched — the bug was consume-side, not delivery.

### Changed
- **F-24 — `listen --wait-one` exits 0 with a timeout payload instead of 124**: an empty-window timeout in `--wait-one` returned `ErrTimeout` → exit 124, which a run-in-background harness surfaced as "command failed" every idle cycle. It now emits `{"status":"timeout","messages":[]}` on stdout and exits 0; the caller tells a timeout from a delivered batch by the `status` field. The default `PollInbox` path keeps exit 124 (a bash until-loop relies on it).

### Added
- **F-26 — `listen --until-deadline=<duration>`**: explicit standby window (e.g. `2h`, `30m`) for the run-in-background executor pattern, more discoverable than `CAB_MAX_BLOCKING_SECONDS`. Precedence: `--until-deadline` flag > `CAB_MAX_BLOCKING_SECONDS` env > 540s default. Invalid/non-positive value is a hard error naming the flag.
- **F-22 — `inbox` subcommand**: `inbox --session-id=<id> --list [--json]` lists `inbox/` (pending) and `processed/` (consumed) messages WITHOUT consuming them (id, from, type, timestamp, one-line preview) — completes F-30 (an archived reply is listable from home, replacing the fragile `ls inbox/*.json`). `inbox --tidy` archives every well-formed `inbox/` message to `processed/` (lossless sweep via `MoveToProcessed`, the explicit "I handled what `--list` showed" hygiene action; malformed/`.tmp` files left for forensics). `--list` and `--tidy` are mutually exclusive.
- **F-23a — agent task-state observability**: new `state` field in the session manifest (`idle`/`working`/`done`/`orchestrating`), set with `cab-bridge state <value>`, shown in `peers` (new `STATE` column), `status`, `whoami`. **`orchestrating` is heartbeat-exempt** — an orchestrator (a VAL not in `listen`) no longer shows `stale` while working a gate ("orchestrator looks dead" fix). `IsStale` is now the single source of truth for staleness shared by `peers`/`status`/cleanup `globalSweep` (the three were divergent before). Schema additive (legacy sessions have empty `state` and behave exactly as before); the setter validates the enum, reads stay lenient (a newer peer's future state value never breaks our display). The 24h PID-dead auto-gc still reclaims a truly-abandoned orchestrator. Message-level read-receipt (F-23b: `delivered_at`/`read_at`) deferred to a later sprint.
- **F-27 — `register --resume` (reconnect-or-register)**: an idempotent bootstrap that resumes an existing session matching the identity `(agent-name, role, scope, team)` — reusing its sessionId, `inbox/`/`processed/`/`outbox/`, and `state` — instead of creating a new one, so a post-compact/restart agent recovers its identity in one line without knowing the old id. Liveness is the manifest PID via `IsProcessAlive` (the BUG-6 / auto-gc convention — NOT the lock, which `listen` does not hold): a live owner (a session with an active `listen`) is never stolen; if every identity match is live the command errors with `use --force-new` for a deliberate second instance. A legacy (pre-F-17) session resumed by a v0.4 binary has its `scope` backfilled (auto-migration to F-17). No identity match → registers fresh.

## [0.2.4] — 2026-05-30

Automatic per-project isolation. Built via the bridge dogfooding workflow.

### Added
- **Auto-scope by project root (F-17)**: `register` derives a `scope` from the project root — the `.git` marker walking up from cwd (a dir for a clone, a FILE for a git worktree), with `$HOME` excluded so a dotfiles repo never collapses scopes, and fallback = cwd for marker-less projects. A VAL at the repo root and an ESC in a subfolder resolve to the same scope and see each other with zero config. `whoami` now shows `scope`.

### Changed
- **`peers` default view is now scope-filtered** (was global): it shows only sessions whose scope matches the current cwd's project root, so multiple projects sharing one data dir no longer see each other's (often orphan) sessions. `peers --all-scopes` for the global listing; `--team` and `--all-scopes` bypass the scope filter. Sessions hidden by the filter are reported on stderr (no silent truncation). Manual `CAB_DATA_DIR`/`--team` isolation remains for special cases — F-17 makes the common case automatic.

## [0.2.3] — 2026-05-30

Prebuilt multi-OS binaries + public adoption. Built via the bridge dogfooding workflow.

### Added
- **GoReleaser** (`.goreleaser.yml` + `.github/workflows/release.yml`): prebuilt static binaries for darwin/linux × amd64/arm64 published to GitHub Releases on tag push (`CGO_ENABLED=0`, `-trimpath`, `checksums.txt`). Closes the source-first gap.
- **Version injection**: `main.version` is injected from the git tag (GoReleaser) / `git describe` (Makefile) — single source of truth is the tag; no more hand-bumping the binary version.
- **Public `bridge-workflow` skill** bundled with the plugin (`/cli-agents-bridge:bridge-workflow`): role-agnostic operating guide (PID/heartbeat model, `listen --wait-one`, team isolation, auto-ack, `cab sent`).

### Changed
- README role-agnostic (val/esc as example roles; documents free-form custom roles, `peer` for flat pairs, team isolation) + prebuilt-binary install path. `darwin-amd64` added to the cross-compile matrix (Makefile + ci.yml + goreleaser).

## [0.2.2] — 2026-05-30

Observability, instant wake, team isolation. Built end-to-end through the bridge dogfooding itself (a VAL↔ESC pair over `cab-bridge`).

### Added
- **F-12 task-state observability**: `ack` message type + automatic delivery receipt when `listen` consumes a query (allow-list `{query}`, loop-safe) + `inboxCount`/`lastConsumedMsgId` exposed in `peers`/`status`.
- **F-10 instant wake**: `listen --wait-one` exits (code 0) on the first non-empty batch (lossless drain-once) — wake-on-arrival for run-in-background callers.
- **F-5 team isolation**: `teamId` manifest field + `register --team=<name>` + `peers --team=<name>` filter + new `cab whoami` (full identity incl. full projectPath + dataDir).
- **F-9 self-send visibility**: each send is copied to the sender's outbox + new `cab sent`.

### Notes
- Security baseline SC-1..SC-7 unchanged; SECURITY.md keeps SC-3 honestly deferred (primitive present, runtime wiring not yet on the live path).

## [0.2.1] — 2026-05-29

First feature release after v0.2.0. Adds automatic orphan-session GC and closes a data-loss gap in session cleanup. Built end-to-end via the cli-agents-bridge dogfooding workflow (VAL↔ESC over the fork itself).

### Added
- **Auto-GC of orphan sessions** (`internal/cleanup/gc.go::GCOrphans`, F10): a session is removed only when **certainly orphaned** — owning PID dead (`session.IsProcessAlive`==false) **AND** `lastHeartbeat` older than `AutoGCHours`. The double condition (LL-10) is load-bearing: a just-`register`-ed session already has a dead PID (one-shot), so only a stale heartbeat distinguishes "abandoned" from "born seconds ago"; a session inside `listen` keeps a live PID (AdoptPID) and is never swept. Hooked into `register` (before creating the new session), opt-out via `AutoGCHours=0`. Every removal is logged to stderr (`auto-gc removed orphan session X (pid N dead, idle Hh)`) — no silent cleanup.
- Config `AutoGCHours` (json `auto_gc_hours`, env `CAB_AUTO_GC_HOURS`, default **24**).

### Fixed
- **Data-loss on session removal** (AUDIT-1, closes upstream pain §1.6): `archiveAndRemoveSession` previously archived only `processed/`, so `inbox/` + `outbox/` pending messages were deleted unarchived by `RemoveAll`. Now archives all three (`inbox`/`outbox`/`processed`) into `archive/<date>/<id>/<subdir>/` before removal. Applies to **all** cleanup paths (auto-gc, `--scope=my-session`, `--scope=global`). Verified: an orphan with an unread inbox message has it archived, not lost.
- **`default.json` not copyable to `config.json`** (F-A): the file carried a `"_comment"` key but the loader used `DisallowUnknownFields`, so copying it (as the comment itself suggested) failed with "unknown field". Added a no-op `Comment` field (`json:"_comment,omitempty"`) so the documented path works.
- **Config path ignored `CAB_DATA_DIR`** (F-B): `config.json` user-file path is now resolved from the env-overridden DataDir.

### Notes
- `peers` rejecting `--session-id` is intentional (it is a global command, not session-scoped) — kept as-is per LL-7. Backlog: a clearer error message + per-subcommand flag docs.

## [0.2.0] — 2026-05-29

First public release of cli-agents-bridge — fork of `PatilShreyas/claude-code-session-bridge` v0.1.0. The 9 confirmed upstream bugs fixed structurally (BUG-1..BUG-9), plus role-based routing, namespace-isolated storage, a security baseline (SC-1/2/4/5/6/7), and a single static Go binary distributed via self-marketplace GitHub.

Hardened pre-release across two gate passes: a triadic security audit (Sprint 5: SC-7 boot check, absolute DataDir, migrate integrity) and a manual smoke test (Sprint 6: BUG-A session-liveness PID model, BUG-B JSON hygiene). 158 tests pass under `-race`. SECURITY.md describes only controls actually on the live code path (SC-3 ownership wiring is honestly deferred to v0.2.1).

Sprint-by-sprint detail below (newest first).

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
