# ROADMAP — `cli-agents-bridge`

> Vista milestone del progetto. Per dettaglio tecnico vedi `PLAN.md` (v3 RATIFIED).
> Aggiornato dal VAL ad ogni completamento milestone.

---

## Status corrente — 2026-05-24

**Fase**: 🟢 **Sprint 2 DONE** (commit `f4d0d44` + `5774cdf`) → 🟡 **Pre-Sprint 3**

**Sprint 0 deliverable** (commit `c142c8d`):
- Day 0 FIX-4 spike → Esito A definitivo (self-marketplace Claude Code 2.1.150)
- Go module baseline + cross-compile (darwin-arm64, linux-amd64, linux-arm64, no cgo)
- Security P0: SC-1 umask 077, SC-3 ownership, SC-4 regex, EnforceDirPerms
- 27 sub-test green con `go test -race`
- CI workflow + docs stub (README/LICENSE/ARCHITECTURE/CHANGELOG/PRIVACY/SECURITY)
- Layout finding documentato

**Sprint 1 deliverable** (commit `57a5db3` refactor + `c38612e` feat):
- Layout Patil-style refactor: `plugins/cli-agents-bridge/` subdir + Makefile `install-plugin` target + sanity end-to-end fresh (`/plugin marketplace add` + install → `/cli-agents-bridge:cab`)
- **BUG-1** heartbeat goroutine: `Manager.StartHeartbeat(ctx, sessionID) <-chan struct{}` con `time.Ticker` + `context.Context` cancellation + done channel idiom
- **BUG-5** LongestPrefixLookup: bestLen tracking + trailing-separator guard (no collision `/foo/barbaz` vs `/foo/bar`)
- **BUG-6** Lock O_EXCL atomic: `os.O_CREATE|O_EXCL|O_WRONLY 0o600` + `syscall.Kill(pid,0)` ESRCH/EPERM stale recovery + `ForceNew` flag + holder PID in error message (semantica `ErrSessionExistsForProject` da documentare Sprint 3 multi-esc-patterns)
- **FIX-7** atomic write: `os.CreateTemp(dir,...)` same-dir + `f.Sync()` + `os.Rename` + EXDEV explicit "config bug not transient"
- 28 sub-test green con `-race` (6 atomic + 8 lock + 11 manager + 3 regression)
- 1441 LOC inserted, max file 341 righe (manager.go), tutti sotto 600
- HeartbeatTickMs 30000 in config + env override `CAB_HEARTBEAT_TICK_MS`
- Velocità: ~1h reale vs stima 1-1.5 giorni (vedi CLAUDE.md LL-6)

**Sprint 2 deliverable** (commit `f4d0d44` feat message + `5774cdf` feat transport):
- **BUG-2** long-poll `ReceiveReply`: preserve non-matching messages in inbox (recovery superior a Patil che li perdeva), ErrTimeout sentinel
- **BUG-7** stderr + exit code 124 timeout (coreutils convention), 1 validation error, 2 unknown subcommand (distinct caller branching)
- Message schema v2 (`schema.go` 111 LOC) + validation gateway (`validate.go` 161 LOC) con `DecodeStrict`/`DecodeLenient` pattern
- Filesystem polling (`poll.go` 109 LOC) con `time.Ticker` + ctx + done chan idiom
- `cab-bridge receive --msg-id=X --max-deadline=N` subcommand funzionante manual smoke
- 27 nuovi sub-test green con -race (14 message + 5 poll + 6 receive + 2 regression BUG-2/7)
- 55+ sub-test totali cumulati Sprint 0+1+2
- Velocità Sprint 2: ~50 min vs stima 3-4h (LL-6 trend continua, atomic.go pre-esistente + idiom Sprint 1)
- Inbox cleanup policy: A (delete post-read) implementato Sprint 2 come da brief. **Migration a B (move-to-processed/) schedulata Sprint 3** parte del BUG-4 cleanup scope-aware work.

**Bug status**: BUG-1, BUG-2, BUG-5, BUG-6, BUG-7 ✅ fixed + tested. Restanti Sprint 3: BUG-3 (routing role), BUG-4 (cleanup scope + inbox migration A→B), BUG-8 (STALE_SECONDS unified check), BUG-9 (connect heartbeat).

**Sblocco Sprint 3**: nessuno. Sprint 3 task = BUG-3/4/8/9 + cmd subcommand suite (register/listen/ask/peers/cleanup/status/inspect/migrate-from-patil) + inbox policy migration A→B.

---

## Milestone overview

| Milestone | Status | Target | Note |
|---|---|---|---|
| **M0** Planning ratification | ✅ DONE | 2026-05-24 | PLAN v3 RATIFIED (synthesis ESC v2 + ultraplan + VAL gate) |
| **M1** Sprint 0 — Day 0 spike + Go baseline | ✅ DONE | 2026-05-24 | commit `c142c8d`, Esito A definitivo, security P0 + Go module + CI cross-compile |
| **M2a** Sprint 1 — Layout refactor + BUG-1/5/6 | ✅ DONE | 2026-05-24 | commit `57a5db3` + `c38612e`. Layout Patil-style, heartbeat goroutine, longest-prefix, lock O_EXCL. 28 sub-test green |
| **M2b** Sprint 2 — BUG-2/7 + message v2 schema | ✅ DONE | 2026-05-24 | commit `f4d0d44` + `5774cdf`. Long-poll receive, stderr+exit 124, DecodeStrict/Lenient gateway. 27 nuovi sub-test |
| **M2c** Sprint 3 — BUG-3/4/8/9 + cmd suite + policy A→B | ⏳ NEXT | +1-2 giorni | Routing role, cleanup scoped, config check, connect heartbeat + 8 cmd subcommand restanti + inbox cleanup policy migration |
| **M3** Smoke test Alan + release v0.2.0 | 🔒 BLOCKED on M2 | +1 giorno post-M2 | ~45 min Alan-time + docs (README/PRIVACY/SECURITY) |
| **M4** v0.3.0 — quality of life | 🔮 FUTURE | 1-2 settimane post-M3 | notification, transcript, retry, background-listen (gated da validation reale) |
| **M5** v0.4.0 — daemon Unix socket | 🔮 FUTURE GATED | 1-2 settimane post-M4 | GATE: G1 latency >200ms ∧ G2 peer >3. Se non si verifica → daemon NON si fa |
| **M6** v1.0.0 — production-ready | 🔮 FUTURE | 3-6 mesi | Marketplace Anthropic submission, brew tap, encryption opt-in, multi-machine |

---

## Decisioni architetturali chiuse (riferimento)

| ID | Decisione | Risolto |
|---|---|---|
| 3.1 Tech stack | **Go from day 1** (single static binary cross-compile) | 2026-05-24 |
| 3.2 Scope MVP v0.2.0 | 8 deliverable + Day 0 spike + 9 regression test | 2026-05-24 |
| 3.3 Naming | `cli-agents-bridge` (vendor-agnostic, kebab-case) | 2026-05-24 |
| 3.4 Backward compat | Namespace separato `~/.claude/cli-agents-bridge/` | 2026-05-24 |
| 3.5 Distribuzione | Self-marketplace GitHub **primary** + pure-PATH **fallback** (Day 0 spike decide) | 2026-05-24 |

---

## Metriche successo v0.2.0 (verifica post-release)

Soglie misurabili definite in PLAN.md §10. Verifica a 1 settimana di uso reale Alan.

- M1: 0 falsi positivi "stale"
- M2: 0 incident cleanup cross-project
- M3: 0 response perse per timeout
- M4: 0 ESC→ESC routing accidentale
- M5: latency round-trip <5s (baseline Patil ~8s)
- M6: setup nuovi peer <60s

Failure criteria + escalation path documentati in PLAN.md §10.

---

## Iterazioni del piano (audit trail)

- `iterations/PLAN_v1_ESC.md` — ESC v1 (pre-Go pivot, naming=claude-bridge obsoleto). Marker `[OBSOLETO]` inline.
- `iterations/PLAN_v2_ESC.md` — ESC v2 (post 7 FIX VAL, Go from day 1, schema trimmed)
- `PLAN.md` — **v3 RATIFIED**, synthesis ESC v2 + ultraplan + VAL critical review (13 micro-fix)

---

## Update protocol

VAL aggiorna ROADMAP.md quando:
- Una milestone passa da ⏳/🔒 a ✅
- Una metrica successo viene misurata (post-release)
- Una decisione architetturale aperta viene chiusa
- Una decisione chiusa deve essere riaperta (eccezionale, segnalare causa)

ESECUTORE NON tocca ROADMAP.md (è docs, responsabilità VAL).
