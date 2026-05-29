# ROADMAP — `cli-agents-bridge`

> Vista milestone del progetto. Per dettaglio tecnico vedi `PLAN.md` (v3 RATIFIED).
> Aggiornato dal VAL ad ogni completamento milestone.

---

## Status corrente — 2026-05-29

**Fase**: 🟢 **Sprint 6 DONE** — fixup post-smoke (commit `2404ebe` + `12bc8a4`) → 🟡 **Pre-tag** (ri-smoke PASS, push + tag v0.2.0)

**Sprint 6 — fixup post-smoke** (commit `2404ebe` BUG-A + `12bc8a4` BUG-B/chore, audit VAL PASS + repro empirico):
Lo smoke test manuale (step 1-15) ha scoperto 2 bug che i test unit/integration NON coglievano (loro simulano il long-running con tick accelerato; lo smoke usa il CLI reale one-shot):
- **BUG-A** (architetturale): `register` one-shot scriveva `pid` effimero (morto a fine comando) → collision check BUG-6 non scattava mai + sessioni STALE fuori da `listen`. Fix (a): `Manager.AdoptPID` + `listen.go` lo chiama all'avvio → il processo listen long-running adotta il PID vivo. **Repro empirico VAL**: con listen attivo, register stessa cwd ora FALLISCE collision (era il fallimento smoke step 11). `Touch` lasciata invariata (connect one-shot, solo un vero listen prende possesso).
- **BUG-B** (JSON hygiene): `cleanup.Result`/`migrateReport`/`collectPeers` emettevano slice nil → JSON `null` invece di `[]`. Fix: init `[]T{}` + correzione 3 early-return `ErrNotExist` che sovrascrivevano l'init con nil (catch ESC, brief sottostimava).
- Finding cosmetici: `cab.md` (rimossi refs Sprint 1/3), `smoke-test.md` (`CAB_DATA_DIR=$$`→fisso `/tmp/cab-smoke-shared` + pre-flight `make install-dev`), version `0.2.0-dev`→`0.2.0` in plugin.json/marketplace.json.
- +5 test (158 PASS/0 FAIL -race, conferma indipendente VAL `-count=1`). File <600 (max manager 375).
- **VAL doc (c)**: declassata garanzia BUG-6 a best-effort in SECURITY.md SC-6 + README features + troubleshooting (collision efficace vs listen owner attivo; register-senza-listen → re-register permesso, ID unici, no merge). Corretta anche riga README "Security baseline" (rimosso "ownership check" → SC-3 è deferred v0.2.1).

**Sprint 5 — security hardening pre-tag** (commit `208c4b2`): SC-7 boot check + DataDir abs + migrate integrity. (dettaglio storico sotto)

**Sprint 5 — security hardening pre-tag** (commit `208c4b2`, audit VAL PASS):
Triadic security audit (security-sentinel + VAL rilettura codice Opus 4.8 + ESC doppia-verifica adversarial) ha scoperto gap tra security model dichiarato e implementato. 3 MUST chiusi PRIMA del tag pubblico MIT (decisione Alan):
- **SC-7 boot check** (era dichiarato P1 MVP, era ASSENTE): `common.go::bootstrapDataDir` Lstat-based wired in tutti i 10 subcommand. first-run→mkdir 0700, symlink/non-dir/owner→FATAL, perms→warn+chmod. Keystone TM-5.
- **DataDir assoluto** (FINDING-11): `config.Load` → `filepath.Abs` + warning. Prereq SC-7.
- **migrate integrity** (FINDING-4/NEW-1/NEW-2): `mf.SessionID==dirname` + `ValidateSessionID` + `mf.Validate()` pre-write fail-closed + `LongestPrefixLookup` ritorna `e.Name()` (chiude traversal latente).
- +12 test (153 PASS/0 FAIL -race, conferma indipendente VAL). File <600 (max manager 358).
- **DEFER v0.2.1**: SC-3 ownership wiring via fstat-fd, os.Root symlink-safe, inbox bound, scanForReply idempotente. **ACCEPT no-action**: #5/#9/#10/#13.
- **VAL doc reconciliation (NEW-3)**: SECURITY.md aggiornato — SC-7 ora dichiarato come wired (vero), SC-3 spostato onestamente a P2 deferred (primitivo presente, wiring v0.2.1) con honesty note. Under-claim > over-claim.

**Sprint 4 — release readiness** (commit `9e56b72`): 6 integration scenari + 8 docs production + smoke checklist. (dettaglio storico sotto)

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

**Sprint 3 deliverable** (commit `0e9f39a` feat backend + `f81599d` feat cmd + `224e438` feat migrate):
- **BUG-3** `internal/routing/role.go`: `ValidateSendPair` hub-and-spoke val-centric + `--allow-mesh` override + observer-cannot-send STRUTTURALE (no flag override)
- **BUG-4** `internal/cleanup/scope.go`: scope=my-session default + scope=global TTY confirm + ErrConfirmRequired exit 3 non-tty + pre-delete archive + retention sweep
- **BUG-8** strutturale: `config.StaleSeconds` unica fonte verità per peers + cleanup
- **BUG-9** `Manager.Touch`: single-shot heartbeat refresh per connect-peer path
- **Inbox policy A→B migration**: `poll.go` refactor `os.Remove` → `MoveToProcessed(processedDir)` con RFC3339 timestamp prefix
- **CMD suite 8 subcommand**: register/listen/ask/peers/cleanup/status/inspect/migrate-from-patil + `cmd/cab-bridge/common.go::exitFromErr` centralized err→exit mapping
- **migrate-from-patil subcommand Go**: backup + dry-run + idempotent + `--patil-dir` test injection + SC-4 path validation RC-3
- 29 nuovi sub-test green con -race (15 routing + 6 cleanup + 4 process + 4 regression BUG-3/4/8/9)
- ~90+ sub-test totali cumulati Sprint 0+1+2+3
- 9/9 regression test BUG-1..BUG-9 green
- Velocità Sprint 3: ~1h45 vs stima 5-7h (LL-6 trend continua, pattern Sprint 1-2 riutilizzati al massimo)

**Bug status**: ✅ **9/9 FIXED** + regression test green. MVP feature-complete.

**Sprint 4 deliverable** (commit `9e56b72` release-coherent single audit point):
- 6 integration test scenari (5 PLAN §7.3 + 1 connect BUG-9 end-to-end)
- 8 docs production grade (README + PRIVACY + SECURITY + 5 docs/*) — 1089 LOC totali, no stub
- `tests/smoke-test.md` 275 LOC, 15 step Alan-executable con pass criteria specifici + sign-off block
- `cmd/cab-bridge/connect.go` 109 LOC — BUG-9 cmd-level wiring end-to-end
- `schemas/{manifest,message}-v2.schema.json` JSON Schema draft 2020-12 con regex/enum constraints
- Version bump `cmd/cab-bridge/main.go` const version = `0.2.0`
- CHANGELOG.md [0.2.0] release entry con cumulative bug coverage
- Single release-coherent commit (vs 1-3 raggruppati permessi) — review-friendly single audit point
- Velocità Sprint 4: ~1h30 vs stima 3-4h

**Sblocco tag v0.2.0**:
1. ⏳ Alan esegue `tests/smoke-test.md` (~45 min, 2 finestre VS Code reali)
2. ⏳ Alan firma sign-off block PASS o segnala FAIL
3. ⏳ Se PASS → VAL aggiorna ROADMAP a "M3 DONE" + memory + crea tag v0.2.0 + (opzionale) push remote + GitHub Release con notes da CHANGELOG.md [0.2.0]
4. Se FAIL → Sprint 5 fixup, no tag

**Remote setup completato 2026-05-24**: `git@github.com:MyAIPlugins/cli-agents-bridge.git` (public, MIT, descrizione + homepage set). Push iniziale `origin/main` = commit `4f8f42f` (12 commit storia completa Sprint 0-4 visibili).

**Tag v0.2.0 NON ancora creato** — disciplina protocol: tag dopo smoke test PASS, mai prima.

---

## Milestone overview

| Milestone | Status | Target | Note |
|---|---|---|---|
| **M0** Planning ratification | ✅ DONE | 2026-05-24 | PLAN v3 RATIFIED (synthesis ESC v2 + ultraplan + VAL gate) |
| **M1** Sprint 0 — Day 0 spike + Go baseline | ✅ DONE | 2026-05-24 | commit `c142c8d`, Esito A definitivo, security P0 + Go module + CI cross-compile |
| **M2a** Sprint 1 — Layout refactor + BUG-1/5/6 | ✅ DONE | 2026-05-24 | commit `57a5db3` + `c38612e`. Layout Patil-style, heartbeat goroutine, longest-prefix, lock O_EXCL. 28 sub-test green |
| **M2b** Sprint 2 — BUG-2/7 + message v2 schema | ✅ DONE | 2026-05-24 | commit `f4d0d44` + `5774cdf`. Long-poll receive, stderr+exit 124, DecodeStrict/Lenient gateway. 27 nuovi sub-test |
| **M2c** Sprint 3 — BUG-3/4/8/9 + cmd suite + policy A→B + migrate | ✅ DONE | 2026-05-24 | commit `0e9f39a` + `f81599d` + `224e438`. 9/9 BUG fixed, MVP feature-complete. 29 nuovi sub-test |
| **M3a** Sprint 4 — Release readiness | ✅ DONE | 2026-05-24 | commit `9e56b72`. 6 integration scenari + 8 docs production + smoke checklist. v0.2.0 RC ready |
| **M3b** Sprint 5 — Security hardening pre-tag | ✅ DONE | 2026-05-28 | commit `208c4b2`. Triadic audit → 3 MUST (SC-7 boot check, DataDir abs, migrate integrity). 153 test green. SECURITY.md riconciliato (NEW-3) |
| **M3c** Smoke test (step 1-15) | ✅ DONE | 2026-05-29 | Alan step 1-12 + VAL step 13-15. 13/15 PASS → scoperti BUG-A (register one-shot PID) + BUG-B (JSON null) + 3 finding doc/test |
| **M3d** Sprint 6 — fixup post-smoke | ✅ DONE | 2026-05-29 | commit `2404ebe` + `12bc8a4`. BUG-A (listen adopts PID, repro PASS) + BUG-B ([] non null) + cab.md/smoke/version. 158 test green |
| **M3e** Push + ri-smoke + tag v0.2.0 | ✅ DONE 🚀 | 2026-05-29 | Pushed + tag `v0.2.0` + GitHub Release live. ri-smoke (step 11 collision + 15 JSON) PASS via repro VAL |
| **M4** v0.2.1 — auto-gc + AUDIT-1 fix (dogfooding) | ✅ SHIPPED 🚀 | 2026-05-29 | tag `v0.2.1` + GitHub Release live. Auto-gc orphan sessions + data-loss fix (§1.6) + F-A/F-B. Primo task interamente via cli-agents-bridge (dogfooding). 158+ test green |
| **M5** v0.2.2+ — distribution + deferred hardening + dogfooding findings | 🔮 NEXT | TBD | GoReleaser multi-OS binaries (release source-first) + SC-3 ownership wiring + os.Root + inbox bound + scanForReply idempotente + `cab ask --wait`. **Skill cab-bridge-awareness creata + raffinata** (3 gap chiusi: CAB_MAX_BLOCKING_SECONDS per listen, listen-in-bg per ESC, recovery post-reset). Da distribuire col plugin. |

### Dogfooding findings — ac-flusso-perfetto (2026-05-29, build videogame 4/6 fasi via cab-bridge)

Il primo uso "serio" del fork (VAL-flusso ↔ ESC-flusso, ~ore di build reale) ha prodotto:

- **F-1 (listen finestra corta) — il più costoso lato ESC**: `listen` in background ri-loopa ogni MaxBlockingSeconds=540 (9min, tarato per foreground harness Patil). Durante attese lunghe di VAL (tuning ~1h ≈ 7 cicli a vuoto). ESC NON conosceva il workaround → costo reale. **Fix prodotto**: default più alto per listen-in-background o flag `--background` (il vincolo 10min harness non si applica al bg). **Fix skill**: ✅ fatto (CAB_MAX_BLOCKING_SECONDS=1800 documentato nel pattern ESC).
- **F-2 (receive return inaffidabile) — lato VAL**: `receive` in bg funziona come WAKE ma (a) se ESC risponde dopo `--max-deadline` la reply tardiva non viene agganciata (resta in inbox), (b) `--in-reply-to` non aggancia su mis-tag/post-reset. Pattern affidabile emerso: trattare receive come sveglia + verificare stato reale via inbox-su-disco + git log. **Fix skill**: ✅ fatto (nota "receive è sveglia non consegna"). **Fix prodotto candidato**: `cab ask --wait` (ask+receive integrato che ripesca late-reply) + receive che al timeout segnala "reply potrebbe arrivare tardi, ricontrolla inbox".
- **F-3 (no resilienza a reset) — entrambi**: reboot/reset azzera i processi, le sessioni non si ri-attaccano (nuovo id, peer non lo sa → tramite umano). **Fix skill**: ✅ fatto (sezione recovery post-reset). **Fix prodotto candidato**: re-attach by agent-name (register che ri-annuncia stessa agent-name ai peer) — v0.3.
- **F-4 (no threading history) — minore**: `peers` non espone l'ultimo msg-id scambiato; per il threading inReplyTo si risale ai msg-id dai file output. **Fix prodotto candidato**: `cab history --session-id`.
- **Confermato OK**: `ask --file` (payload lunghi, zero quoting hell) + `listen`/`receive` in run_in_background (wake event-driven) — i due pilastri del workflow reggono. La skill cab-bridge-awareness è "in larga parte bastata" (ESC) — il modello PID/heartbeat ha evitato la confusione VAL-stale.
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
