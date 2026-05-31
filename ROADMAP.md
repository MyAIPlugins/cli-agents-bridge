# ROADMAP — `cli-agents-bridge`

> Vista milestone del progetto. Per dettaglio tecnico vedi `PLAN.md` (v3 RATIFIED).
> Aggiornato dal VAL ad ogni completamento milestone.

---

## Status corrente — 2026-05-31

**Fase**: 🚀 **v0.4.0 SHIPPED 2026-05-31** — tag `v0.4.0` + [GitHub Release](https://github.com/MyAIPlugins/cli-agents-bridge/releases/tag/v0.4.0): 5 asset prebuilt (darwin/linux × amd64/arm64 + checksums), workflow GoReleaser SUCCESS. Consolida **F-30** (receive→archivia) · **F-24** (--wait-one exit0+payload) · **F-26** (--until-deadline) · **F-22** (inbox --list/--tidy) · **F-23a** (agent-state + orchestrating heartbeat-exempt) · **F-27** (register --resume reconnect-or-register). Le 2 portanti (F-23a+F-27) complete. Gate `-race -count=1` verde per ogni fix + smoke + **validazione sul campo** (chatterence-bi: F-21/F-22/F-23a/F-26 confermati in uso reale). Skill pubblica `bridge-workflow` riallineata a v0.4. Email docs → firstcontact@alancurtisagency.com. **Follow-up → prossime versioni**: F-34 conversation-cursor (incroci, portante v0.5), F-35/F-36 ergonomia wait/inbox, F-23b read-receipt, F-25/F-28/F-29/F-31/F-32/F-11, + chore gofmt (6 file) + bump GitHub Actions a Node 24.

**Sprint v0.3 (ciclo wake/consegna affidabile) — 3 MUST su `main`, gate VAL verde, binario dev rigenerato (NON taggato — testing reale in corso)**: F-30 `8612af3` (receive archivia in `processed/` invece di cancellare — la reply non sparisce piu' dalla vista in background), F-24 `88d98a1` (`--wait-one` al timeout exit 0 + payload `{"status":"timeout"}` invece di 124), F-26 `4761bfe` (flag `--until-deadline` per la finestra standby). Distillati da 8 voci-agente dogfooding (VPS + gioco + security review BI). Gate VAL indipendente `go test -race -count=1 ./...` VERDE 10/10 (no cached) + smoke reale. Più **F-22 `5c91177`+`2d43081`** (`inbox --list`/`--tidy` — ispeziona inbox+processed senza consumare e sweep esplicito; completa F-30 col recupero leggibile da casa, elimina il poll `ls` fragile). Più **F-23a `3bbc0f2`+`260e1b1`+`0e10ccd`** (osservabilità stato-agente: campo manifest `state` + comando `cab-bridge state` + colonna STATE in peers/status/whoami; **`orchestrating`=heartbeat-exempt** via `IsStale` centralizzato single-source → "orchestrator sembra morto" RISOLTO; design-first ratificato opzione A). Binario in PATH ora `0.2.4-13-g0e10ccd` (tutti i fix LIVE). **Tre catch da senior di ESC oltre i brief (LL-4)**: F-30 `ErrNotExist`→`continue`; `--tidy` id-random→opzione B; F-23a l'auto-gc 24h PID-dead bounda il footgun dell'opzione A. Più **F-27 `4784bac`** (`register --resume` reconnect-or-register: bootstrap post-compact in UNA riga, riprende la sessione per identità senza rubare una viva, backfill scope su legacy). **Le 2 portanti di v0.4 (F-23a + F-27) sono COMPLETE.** Binario in PATH `0.2.4-15-g4784bac`. **Mea culpa condiviso su F-27 (LL-12)**: l'insight lock-based del piano — lodato da VAL in ratifica — era FALSO (`listen` non tiene il lock); il gate verde non l'ha colto (test su premessa errata), lo **smoke reale di ESC sì**. Provato dal VAL su sé stesso (riprende `f1e78bd3` + backfill scope). Restano (NON bloccanti per il tag): **F-23b** read-receipt + F-25/F-28/F-29/F-31/F-32/F-11. Debito: 6 file non-`gofmt` (version-drift, chore; `go vet` è il gate). **Prossimo: tag `v0.4.0` + push + testing reale FRESH (valida F-17/F-23a/F-27/inbox/wake sul campo).**

**Fase**: **v0.2.4 SHIPPED 2026-05-30** — tag `v0.2.4` + [GitHub Release](https://github.com/MyAIPlugins/cli-agents-bridge/releases/tag/v0.2.4): **F-17 auto-isolamento per project-root** — `register` deriva lo `scope` dalla root `.git` (worktree-aware, `$HOME` escluso, fallback cwd); `peers` filtra di default per lo scope del cwd (`--all-scopes` per globale, hidden-count su stderr); `whoami` mostra lo scope. Elimina il workaround manuale `CAB_DATA_DIR`/`--team` per il caso comune. Workflow release SUCCESS, 4 binari prebuilt pubblicati. **Quattro release in un giorno** (v0.2.1 + v0.2.2 osservabilità/wake/team/outbox + v0.2.3 binari prebuilt/skill pubblica + v0.2.4 auto-scope). Deferred → v0.3: F-11 race cleanup, SC-3 ownership wiring, bump action a Node 24 (deprecation warning CI, non bloccante).

**v0.2.2 MUST #1 — F-12 ACK/osservabilità** (2026-05-30, coppia VAL-bridge/ESC-bridge, dogfooding via cab-bridge):
- 4 commit `feat/v0.2.2`: `9e0c6c6` ack type + lenient forward-compat (solo type, status strict), `f565985` lastConsumedMsgId + manifest mutex su OGNI RMW, `1605835` auto-ack on listen emit (allow-list {query}, anti-loop strutturale) + scanForReply skip-ack + `--no-auto-ack`, `523fb32` inboxCount + lastConsumedMsgId in peers/status.
- Gate VAL `go test -race -count=1 ./...` VERDE 10/10 indipendente (nessun cached, LL-11) + lettura riga-per-riga delle parti critiche. NON pushato; binario in PATH ancora 0.2.1 (F-12 non installato per il dogfooding → ACK ancora manuale).
- 3 finding di metodo (dettaglio in `docs/v0.2.2-plan.md`): F-14 executor-sordo-durante-lavoro, F-15 ACK-semantico-sciatto→falso-allarme, F-16 (madre) verifica-ground-truth-su-disco-NON-resoconto. ESC degradato a fine sessione (4 allucinazioni: id/finding/campo-schema/gate inesistenti) → resettato; codice impeccabile, resoconto no.

**v0.2.2 MUST #2 — F-10 wake immediato** (2026-05-30, ESC fresco post-reset):
- Commit `f52addc`: `listen --wait-one` esce (exit 0) dopo il primo sweep non-vuoto via nuova `DrainInboxOnce` (sincrona, zero-perdita per costruzione — non perde i msg che `PollInbox` pre-sposta in `processed/` prima dell'emit bloccante). Helper `consumeInboxEntry` DRY (`poll_test.go` invariato). Fix deadlock teardown (`cancel()` esplicito, catch ESC oltre il brief). Exit 0/124, default invariato. Gate VAL `-race -count=1` verde indipendente + lettura riga-per-riga.
- **F-14 riconfermato sul campo**: dopo aver concluso il turno F-10, ESC "dorme" (stale, fuori listen) e il brief F-5 NON lo sveglia finché Alan non lo riattiva → conferma il bisogno di `--wait-one` nel binario in PATH (oggi 0.2.1) + disciplina "executor mantiene ascolto attivo tra i task".

**Fase storica v0.2.0**: 🟢 **Sprint 6 DONE** — fixup post-smoke (commit `2404ebe` + `12bc8a4`) → 🟡 **Pre-tag** (ri-smoke PASS, push + tag v0.2.0)

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

### Dogfooding findings — multi-coppia (2026-05-29, 2a coppia ac-links avviata mentre ac-flusso lavorava)

Avviare una SECONDA coppia VAL-ESC (progetto diverso) nello stesso data dir globale ha rotto l'identità:

- **F-5 (architetturale, serio): nessun isolamento tra coppie/team**. Data dir globale condiviso → `peers` mostra le sessioni di TUTTI i progetti → entrambi gli agenti della 2a coppia hanno SCAMBIATO le sessioni dell'altra coppia per le proprie (VAL-links stava per pulire VAL-flusso; ESC-links si è identificato in ESC-flusso). È il pain §1.4 originale ("multi-peer routing confusion") che `teamId` doveva coprire — **tagliato in FIX-2 come YAGNI** (giudizio corretto per 1+1, prematuro per multi-coppia). **Fix immediato (zero codice)**: un `CAB_DATA_DIR=~/.claude/cli-agents-bridge/teams/<progetto>` per coppia → isolamento totale. Skill FIXED. **Fix prodotto (v0.3)**: reintrodurre `teamId` per isolamento logico in un data dir condiviso, o rendere il pattern data-dir-per-team first-class (es. `cab register --team=<nome>`).
- **F-6 (FRIC-9 non risolto): `projectName = basename(cwd)` collide**. Due cwd `src/` di progetti diversi → stesso `projectName: src` → indistinguibili in peers. **Fix prodotto**: usare il path completo o un discriminatore, o affidarsi solo all'agent-name. Mitigato da F-5 (data dir isolato).
- **F-7 (bootstrap canale): ESC che pinga VAL non-in-listen non arriva**. VAL non ascolta → il ping resta in inbox, serve tramite umano. **Pattern corretto**: bootstrap VAL-driven — VAL scopre ESC via `peers` (per agent-name), non ESC che annuncia a VAL. Con F-5 risolto (data dir isolato) VAL vede solo il suo ESC → bootstrap immediato senza tramite. Skill FIXED.

### Sintesi finale doppio dogfooding (2026-05-29) — videogame 6/6 consegnato + coppia links

Videogame "Flusso Perfetto" **6/6 fasi consegnabile** (engine+render+UI+leaderboard+a11y AA+embed, 28 test verdi, balance lockato) costruito interamente via cab-bridge. Findings consolidati dall'arco completo:

- **F-2 è il finding #1 per IMPATTO** (non solo ergonomia): `receive` che non aggancia (inReplyTo mismatch / late-reply) va in timeout a fine-finestra → il VAL si sveglia ~20-30min DOPO il commit reale di ESC. Sul videogame questo è stato **quasi tutto il tempo morto evitabile (~14% wall-clock)** — più delle assenze umane. **Fix skill**: ✅ VAL receive con finestra BREVE (300-600s) ri-loopata + git-poll, NON 1800s (asimmetria: ESC listen lungo, VAL receive corto). **Fix prodotto v0.2.2**: `cab ask --wait` che fa il loop corto+verifica internamente; e/o receive che al timeout segnala "controlla inbox, reply forse arrivata".
- **F-8 RIDIMENSIONATO**: nel flusso NON osservato (ogni commit dichiarato c'era davvero, verificato git). Il caso links ("brief mai arrivato") era probabilmente **trasporto F-2**, non "dichiarato-ma-non-eseguito". F-8 resta da confermare, possibile sovrapposizione con F-2 — non promuovere a finding indipendente senza altra evidenza.
- **F-1/F-7 confermati** dall'arco lungo. F-1 mitigato da CAB_MAX_BLOCKING_SECONDS=1800 (ESC). F-7: tutto il bootstrap/recovery guidato dal VAL.
- **Soglia di autonomia VAL** (workflow, non prodotto): i ~6 gate "go FASE N?" per-fase sono il tempo morto comprimibile lato umano. Fix: soglia esplicita al kickoff (VAL autonomo su eng/fix/docs; go umano solo per brand/lock-critici/scope). Skill FIXED.
- **Fuori scope cab-bridge** (noto): git-ai bg riscrive gli hash async → hash citato nelle docs diventa orfano. Workaround adottato dalle coppie: citare i **tag**, non gli hash, nelle docs.
- **Pilastri confermati solidi**: `ask --file` e "ground truth = git+inbox" (mai fidarsi del return di receive).

### Quadro definitivo — entrambe le coppie chiuse (videogame 6/6 + links 4 PR prod)

Report finali da VAL-flusso, VAL-links, ESC-links. Findings cab-bridge consolidati e RI-PRIORITIZZATI:

- **F-10 (NUOVO, PRIORITÀ #1 v0.2.2): il wake "event-driven al messaggio" NON funziona con Bash run_in_background** — notifica solo all'EXIT del comando (timeout 124), non all'arrivo. Con finestra lunga (1800s) i messaggi urgenti restano invisibili fino a 30min → dipendenza da ping umano (ESC-links non vedeva il GO Fase B già arrivato; VAL receive si svegliava ~20-30min dopo il commit di ESC). È la **causa reale della latenza** sofferta da entrambe le coppie. **Mea culpa VAL-bridge**: la skill raccomandava run_in_background+finestra-lunga per il wake — sbagliato, lo peggiorava. **Fix skill**: ✅ (Monitor per wake immediato, o finestra breve ri-loopata; mai finestra lunga se aspetti messaggi urgenti). **Fix prodotto**: `cab listen --wait-one` che esce al primo messaggio (→ run_in_background notifica all'arrivo), e/o documentare l'uso di Monitor; `cab ask --wait` per il lato VAL.
- **F-5 confermato da ENTRAMBE le coppie (priorità #2)**: data dir globale → `peers` cross-progetto → confusione identità reale (VAL-links stava per pulire VAL-flusso; ESC-links si è identificato in ESC-flusso — "tutti i segnali combaciavano"). Fix immediato: data dir per coppia. Fix prodotto v0.3: teamId, o `cab whoami`/`status` che mostra projectPath COMPLETO + `peers --project` filtro.
- **F-9 (NUOVO): `ask` non popola l'outbox del mittente** → un agente non può verificare i propri invii guardando le proprie sessioni (deve ispezionare inbox+processed del destinatario). Ha confuso il debugging (incluso il mio di ieri). Fix prodotto: log in outbox o `cab history/sent --session-id`.
- **F-11 (NUOVO): race cleanup/GC vs delivery** — un `cleanup --scope=global`/auto-gc può rimuovere un messaggio appena scritto (ESC-links: `ask` con msg-id ritornato ma file sparito). Mitigato da F-5 (isolamento). Fix prodotto: lock/grace-period su sessioni che ricevono.
- **F-6 confermato + fix-projectName INCOMPLETO**: VAL (cwd root, `projectName=ac-links`) ed ESC (cwd subdir, `projectName=src`) della stessa coppia NON condividono projectName → filtrare per projectName non identifica la coppia. Serve teamId (F-5).
- **F-8 RITIRATO**: il "brief mai mandato" della coppia links era un artefatto di osservazione (snapshot pre-invio + `ask` non popola outbox, F-9), NON azione-dichiarata-non-eseguita. VAL-links ha provato la consegna (msg-2d2325d136a8 in processed di ESC). Non promuovere.
- **Confermati solidi**: `ask --file`, "verifica inbox/git non il return" (grazie a questo ESC-links ha scoperto un ask perso da F-11), modello PID/heartbeat.
- **Costo dei problemi su entrambe le coppie**: BASSO — zero rework sul codice consegnato. Le frizioni sono costate comunicazione extra + cicli d'attesa (la latenza F-10). Tempo morto videogame ~14%, quasi tutto F-10 (bridge), non attesa-umano.

### F-12 (post-mortem "stallo pnpm", VAL-flusso) — osservabilità dello stato-task: il fix più maturo

Classe NUOVA, distinta da affidabilità (i dati non si perdono mai) e da F-10 (latenza wake): **l'orchestrator non sa lo STATO del task dell'executor**. Al timeout di `receive` i 3 casi sono indistinguibili — ESC lavora-lentamente / non ha preso il brief / ha finito ma reply persa. E `heartbeat`/`stale:false` indica solo che il processo listen è vivo, NON che l'agente stia agendo (può essere idle-in-listen). Lo "stallo pnpm" fu uno stallo di PERCEZIONE (escalation a vuoto), non di perdita: il commit `4a7ff64` era corretto.

- **Fix #1 (ACK leggero) — quick-win, zero codice**: macchina a stati `inviato → ack(working) → done`. ESC manda `--type=notify` "ACK ricevuto/in-lavorazione" appena prende il brief, poi `--type=response` col commit a fine. VAL al timeout: ACK visto → lavora (aspetta), nessun ACK → non preso (re-invia). Adottabile SUBITO via convenzione (`notify` è già nello schema). Skill FIXED. **Fix prodotto v0.2.2**: `--type=ack` dedicato + auto-ack del binario su listen-emit.
- **Fix #2 (osservabilità senza ACK)**: `peers`/`status` espongano `inboxPending` + `lastConsumedMsgId` per sessione → "1 pending non-consumato" = non l'ha preso; "0 pending, lastConsumed=brief" = ci lavora. Più: spostare il brief in `processed/` appena ESC lo legge (consumato = working).
- **Fix #3 (re-ingaggio executor concluso)**: dopo un "esco dal listen", un brief nuovo NON sveglia l'executor spento (~minuti a re-ingaggiarsi). Skill FIXED (concordare a fine-fase se resta in listen; o `--type=wake`). 
- **Fix #4 (igiene inbox orchestrator)**: marcare "gestito" (spostare in processed/) anche le reply lette a mano → inbox = solo non-gestiti. Skill FIXED.

**Priorità v0.2.2 definitiva**: (1) ACK/F-12 — quick-win convenzione + fix-prodotto leggero, massimo impatto su stalli-di-percezione; (2) F-10 wake immediato (Monitor / finestra breve); (3) F-5 isolamento coppia (teamId / data-dir). Poi F-9/F-11/F-6 + distribution GoReleaser + hardening deferred.

**Confermato da VAL-flusso — NON toccare (la base regge)**: zero data-loss (git+inbox preservati sempre), `ask --file` impeccabile, disciplina "verifica-non-assumere" ha retto in entrambe le direzioni. Il lavoro v0.2.2 è tutto OSSERVABILITÀ + REATTIVITÀ, zero su affidabilità.

### Dogfooding VPS (2026-05-30, coppia ac-agents — sviluppo locale del VPS, futuri JOE/WAL) → backlog v0.3

Coppia **VAL-vps ↔ ESC-vps** chiusa con feedback strutturato dal campo (2 sprint + 2 gate, ~6 scambi bridge, binario locale **v0.2.3**). Le frizioni di VAL-vps ed ESC-vps **convergono** su 3 assi (due prospettive indipendenti → segnale forte). Verificate sul codice prima di catalogare (F-16 applicato anche al VAL).

**Conferme blindate (validate da DUE prospettive indipendenti → intoccabili)**: `ask --file` (payload lunghi, zero quoting hell) · `--wait-one`/`receive` background event-driven (exit 0 all'arrivo via `inReplyTo`) · `cleanup` chirurgico + isolato per-data-dir · resilienza-via-disco + JSON ovunque (mai perso un messaggio).

**2 catch che hanno ribaltato il feedback grezzo**:
- **L'auto-ack NON è da fare, esiste già ed è ON** (`send.go:102`, allow-list `{query}`, ON in `listen`, flag `--no-auto-ack`). VAL-vps lamentava il *doppio* ACK, ESC-vps chiedeva *di farlo*: sono lo **stesso bug visto dai due lati** — ESC mandava ACK manuali in PIÙ all'auto-ack che il suo binario già emetteva. Causa di METODO → **fix-SKILL** (F-21, fatto): l'auto-ack copre la ricezione, ESC non lo duplica.
- **Finestra `listen`: 540s default è tarato per FOREGROUND** (Bash-tool timeout max 600s, `listen.go:53`). In **run_in_background** non c'è quel tetto → i 1800s che ESC-vps ha usato funzionano (sua evidenza empirica > inferenza VAL). Mea culpa VAL: avevo erroneamente legato il limite ad `API_TIMEOUT_MS` (che è il timeout delle API del modello, non dei processi shell). F-26 valido.

**ESC-6 (isolamento auto da project-root) = GIÀ SHIPPED v0.2.4** (`internal/session/scope.go` su disco). ESC-vps ne soffre solo perché gira sul binario locale v0.2.3 → **validazione retroattiva di F-17** + promemoria: rigenerare il binario locale a v0.2.4 (`make install-dev`) al prossimo giro pulito con sessioni fresche.

**Backlog v0.3 (priorità per impatto/sforzo)**:

| ID | Item | Prio | Sforzo | Origine |
|---|---|---|---|---|
| **F-21** | Disciplina ACK: auto-ack copre la ricezione, stop al manuale | P1 | fatto (skill) | VAL-vps#1 ⊕ ESC-vps#4 |
| **F-30** | **`receive`/`scanForReply` consuma DISTRUTTIVAMENTE** (`receive.go:140` `os.Remove`) invece di archiviare come fa `listen`/`PollInbox` (`MoveToProcessed`) → asimmetria. In background, se l'output del processo si perde (finestra scaduta / task non riletto), il msg è rimosso dall'inbox del destinatario e sopravvive SOLO nell'outbox del mittente → recupero manuale dal path altrui (pain #1 VAL-bi, sistematico a ogni fase, "wake event-driven 1 volta su 6"). **NON è bug di consegna** (verificato: `send.go:78` scrive inbox PRIMA, `ask` riesce solo se l'inbox-write riesce; outbox è copia best-effort dopo). **Fix radice basso-sforzo**: `scanForReply` → `MoveToProcessed` (esiste già) → il consumato finisce in `processed/` DI CHI RICEVE → recupero da casa propria, non dall'outbox altrui. Meglio del workaround proposto (`history --peer` pesca dal path altrui). Il fix riusa `consumeInboxEntry`/`MoveToProcessed` — già single-source-of-truth del consumo: **verificato che `DrainInboxOnce`+`PollInbox` archiviano correttamente** (`drain.go:47,83`), l'asimmetria os.Remove è SOLO in `scanForReply` | ✅ **DONE** `8612af3` | Basso | **VAL-bi#1 ⊕ ESC-bi** (conferma lato mittente, 8ª voce) |
| **F-24** | **exit 124 in `--wait-one` ≠ failure** — al timeout a coda vuota (`listen.go:119-121` → `ErrTimeout`→124) l'harness mappa a "failed" → falso allarme a OGNI ciclo idle. Fix: **exit 0 + payload `{"status":"timeout","messages":[]}`** (unico modo: un exit≠0 dedicato è comunque "failed" per l'harness). Trade-off da decidere in design: oggi 124 serve all'until-loop bash → flag opt-in `--exit-zero-on-timeout` o default per run-in-background | ✅ **DONE** `88d98a1` | Basso | ESC-vps#1 ⊕ VAL-flusso#1 ⊕ **ESC-flusso#2** (tripla; "144 se SIGKILL") |
| **F-23a** ✅ / **F-23b** | **F-23a DONE** (`3bbc0f2`+`260e1b1`+`0e10ccd`): campo manifest `state` (`idle`/`working`/`done`/`orchestrating`) + comando `cab-bridge state` + colonna STATE in peers/status/whoami; **`orchestrating`=heartbeat-exempt** via `IsStale` centralizzato (single-source peers/status/cleanup, prima divergenti; bounded dall'auto-gc 24h PID-dead) → "orchestrator sembra morto" RISOLTO; schema additivo retro-compat (setter strict, read lenient). Heartbeat-passivo-da-ogni-comando = fuori scope (ridondante con orchestrating-exempt). Opzione A (pure state, trade-off crash-invisibile accettato+documentato, upgrade-path TTL). **F-23b RESTA**: read-receipt messaggio (`delivered_at`/`read_at`, enum `sent→ack→working→done`) → elimina i "messaggi incrociati" (ESC-bi#2/#5) | F-23a ✅ **DONE** · F-23b P2 Medio | VAL-vps#3+#4 ⊕ ESC-vps#5 ⊕ VAL-flusso#3 ⊕ **ESC-bi#2+#5+#6** (sestupla) |
| **F-27** ✅ | **DONE** `4784bac`: **`register --resume`** (reconnect-or-register idempotente). Riprende la sessione che matcha l'identità `(agent-name, role, scope, team)` — stesso sessionId, inbox/processed/outbox, `state` — invece di crearne una nuova; bootstrap post-compact in UNA riga senza sapere il vecchio id. Liveness = `IsProcessAlive(mf.PID)` (convenzione BUG-6/auto-gc, **NON il lock** — `listen` non lo tiene); live owner mai rubato (tutti-vivi → `ErrIdentityLive`, `--force-new` per 2ª istanza); backfill scope su legacy (auto-migrazione F-17). **Mea culpa condiviso VAL+ESC**: l'"insight" lock-based del piano (che VAL aveva lodato) era FALSO — il gate verde non l'ha colto (test su premessa errata), lo **smoke reale di ESC sì** (LL-10/LL-12). Provato anche dal VAL su sé stesso (riprende `f1e78bd3`+backfill scope) | ✅ **DONE** | Medio | **VAL-flusso#2** (+ F-3 storico) |
| **F-26** | Default finestra `listen` per-ruolo (ESC standby più lungo in bg) — il default 540s costringe ESC ad alzarlo a mano. `--standby`/`--no-timeout` valutabile in bg, MA un re-loop periodico serve per la re-invocazione (`/loop`) → preferire default alto a no-timeout. **Flag esplicito `--until-deadline=<dur>`** (ESC-bi#1) più leggibile di `CAB_MAX_BLOCKING_SECONDS`; variante `--follow` (ri-loop interno multi-batch) utile solo con lettura-stream (Monitor), NON col wake-by-exit di run-in-background | ✅ **DONE** `4761bfe` | Basso | ESC-vps#3 ⊕ ESC-flusso#4 ⊕ **ESC-bi#1** (TRIPLA: tutti gli ESC → il default 540s è sbagliato per il ruolo executor) |
| **F-22** | Sottocomando `cab-bridge inbox --session-id=X` con due verbi: **`--list [--json]`** ispeziona i pendenti (id, from, type, preview) SENZA consumarli — copre inbox **+ `processed/`** (così completa F-30: recuperi un msg già consumato dal receive-bg senza parsare l'outbox altrui), rende robusto il pattern "VAL-aspetta-inbox" (F-19) eliminando il poll `ls inbox/*.json` fragile (falso-positivo dir vuota + crash glob no-match zsh); **`--tidy`** sweep esplicito: archivia in `processed/` tutto il pending visibile in `inbox/` (opzione B, lossless `MoveToProcessed`, mutuamente esclusivo con `--list`; criterio iniziale `id≤lastConsumedMsgId` SCARTATO — id random non-monotonici, catch ESC). `peers` dà solo il conteggio INBOX, non il contenuto | ✅ **DONE** `5c91177`+`2d43081` | Basso | VAL-vps#2 (tidy) ⊕ **ESC-flusso#3** (list) |
| **F-25** | Errore semantico su target chiuso: `ErrTargetSessionGone` invece del raw `open manifest.json: no such file` (`send.go:37`) — leggibile + gestibile programmaticamente | P2 | Basso | ESC-vps#2 |
| **F-28** | `cleanup --notify-peers` — lascia un messaggio "offline" al team prima di rimuoversi, così un peer in listen non resta appeso. Simmetrico a F-25 (F-25 = il mittente scopre il target andato; F-28 = il target avvisa prima di andare) → insieme coprono il lifecycle-offline da entrambi i lati | P2 | Basso | **VAL-flusso#4** |
| **F-29** | Unificare/chiarire i DUE meccanismi di isolamento — `CAB_DATA_DIR` (fisico) e `--team` (logico) non sono interoperabili: "stesso team" su data-dir diversi = peer invisibili (pain bootstrap reale). **F-17 (v0.2.4) è già la risposta canonica** per il caso comune (auto-scope da `.git` → nessuna scelta manuale, mismatch impossibile) → **priorità reale = deployare v0.2.4 in locale**, non nuovo codice. Residuo design v0.3: warn su teamId/data-dir mismatch (= F-20-prodotto già in deferred), al massimo `--team` come alias di sotto-path. Caveat: F-17 unifica solo se i peer condividono lo stesso checkout `.git` | P2 | Basso (deploy) / Medio (design) | **ESC-flusso#1 ⊕ VAL-bi#2** ("peers mente": filtra via in silenzio chi non ha il team → fa dubitare dello strumento) |
| **F-31** | `listen --replace` (termina un listen preesistente sulla stessa sessione prima di adottare il PID) o warning su listen duplicati su un sessionId — evita il listen "orfano" non tracciato dall'harness. + nota skill: NON fare `cab-bridge listen &` dentro un comando già in run-in-background (doppio backgrounding → orfano) | P2 | Basso | **ESC-bi#3** |
| **F-32** | Visibilità team/data-dir a livello PROCESSO — `peers` è isolato ma `pgrep "cab-bridge listen"` mostra anche sessioni di altri data-dir → confusione nel kill manuale degli orfani. Fix: esporre il team nel process name (`cab-bridge[chatterence-bi] listen`) o un lock-file per-team. Complementare a F-31 (insieme = gestione orfani robusta) | P3 | Medio | **ESC-bi#4** |
| **F-11** | globalSweep PID-aware (check kill -0 prima di rimuovere) — già in roadmap, confermato dal campo da VAL-vps#5 (rischio inverso: sessione viva fuori-listen spazzata) | P3 | Medio | già presente |

**Già in deferred v0.3** (invariati): SC-3 ownership wiring, bump action GitHub a Node 24 (deprecation warning CI, non bloccante). Eventuale F-20-prodotto (avviso quando in un data dir convivono sessioni con teamId diverso/vuoto).

**Aggiornamento dal feedback coppia gioco — VAL-flusso ⊕ ESC-flusso, 2026-05-31** (team `flusso`, ciclo completo brief→ACK→consegna→ratifica + sessione idle/ripresa post-compact → coprono *ciclo di vita* e *ciclo di lavoro* da entrambi i lati):
- **VAL-flusso** (idle/ripresa): promuove **F-24** a P1, rafforza **F-23** (sotto-fix heartbeat-passivo), apre **F-27** (reconnect idempotente post-compact) e **F-28** (cleanup --notify-peers).
- **ESC-flusso** (ciclo di lavoro): porta **F-24** a tripla convergenza, **F-26** a doppia, estende **F-22** col verbo `--list` (ispeziona inbox senza consumare → elimina il poll fragile), apre **F-29** (i due meccanismi di isolamento non interoperabili).
- **Confermano insieme**: `listen --wait-one`+bg+auto-ack è "il cuore del valore" (zero ACK manuali, zero stalli) · `ask --file` · `cleanup --session-id` chirurgico · `peers` INBOX+LAST_CONSUMED · JSON di `cleanup`.
- **Segnale operativo forte**: ESC-flusso è il **3° agente indipendente** (dopo il caso multi-coppia e VAL-vps) a inciampare nell'attrito isolamento **già risolto da F-17 (v0.2.4)** → la priorità #1 trasversale è **deployare v0.2.4 in locale**, non scrivere codice nuovo.

**Security review BI — VAL-bi ⊕ ESC-bi (chatterence-bi, 2026-05-31, ~4-5h quasi-autonome, 2-3 interventi umani)** — la sessione più lunga e autonoma, ha stressato il ciclo completo brief→ACK→consegna→ratifica ripetuto su molte fasi:
- **Pain #1 → F-30 (TOP del backlog)**: le late-reply "non arrivavano in inbox" e finivano nell'outbox di ESC, sistematico. **Diagnosi VAL-bi corretta nel sintomo, ribaltata nella causa** (verifica codice F-16): NON è bug di consegna (`send.go:78` consegna PRIMA, ask riesce solo se l'inbox-write riesce) — è il `receive` che consuma **distruttivamente** (`os.Remove`, asimmetrico al `MoveToProcessed` di `listen`) → in bg con output perso, il msg sparisce dall'inbox e resta solo nell'outbox del mittente. Fix radice meglio del workaround proposto: archiviare in `processed/` di chi riceve.
- **Pain #2 → F-29 (3ª prospettiva)**: "peers mente" — `--team` filtra via in silenzio ESC (registrato senza team sullo stesso data dir) → il watcher non l'avrebbe trovato mai, ma i msg passavano. Conferma F-29 + azione #0 (deploy v0.2.4).
- **Pain #3**: receive corto = molti wake a vuoto — **sintomo amplificato da F-30** (col receive che perde la reply, la finestra serviva solo da timer per andare a guardare l'outbox a mano). Una volta fixato F-30, si ridimensiona. Tocca F-26.
- **ESC-bi (lato executor)**: conferma F-30 dal lato mittente (8ª voce, "VAL ha recuperato dal mio outbox"); toil #1 = i ri-loop manuali del listen (~6 in una verifica VAL di 10-20 min) → F-26 a tripla-ESC + flag `--until-deadline`; chiede read-receipt (→ F-23) per i messaggi incrociati; apre F-31 (listen orfano da doppio-backgrounding) + F-32 (team invisibile a `pgrep`). **Punto 5 "re-emissione" verificato sul codice (F-16) = INFONDATO come bug**: il listen archivia at-most-once (`consumeInboxEntry`), la riapparizione è il re-invio del mittente per receipt mancante (= punto 2, lo mappa F-23).
- **Conferme blindate**: CAB_DATA_DIR per-progetto = "regola d'oro" (peers pulito dal 1° comando) · `--file` · **resilienza-via-disco = "il punto di forza vero"** (mai perso nulla, nemmeno con messaggi incrociati) · **pattern ACK funziona** (distingue "ricevuto" da "finito" → valida F-21) · **spostamento automatico in `processed/`** (ESC-bi: "niente housekeeping manuale" → conferma la base del fix F-30) · cleanup chirurgico.

**Bilancio**: il bridge è affidabile sui DATI (zero perdite, da **8 voci-agente indipendenti** su videogame + VPS + gioco + BI) ma **fragile sulla consegna-vista e sull'osservabilità**. F-30 lo dimostra: il dato c'era sempre (sul disco), ma il `receive` lo toglieva dalla vista. Backlog v0.3 = **consegna-vista + osservabilità + ciclo-di-vita + ergonomia isolamento**, zero affidabilità core. **Due sospetti di codice verificati e diagnosticati** (F-16, non sui resoconti): F-30 confermato (asimmetria `os.Remove` in `scanForReply`) · "re-emissione" ESC-bi INFONDATA (il consumo archivia at-most-once; è re-invio del mittente). **Azione #0 (zero codice): deploy binario v0.2.4** (chiude F-29). **Primo commit codice v0.3: F-30** (receive→MoveToProcessed, basso sforzo, sblocca il valore centrale). Poi quick-win F-24/F-26(+--until-deadline)/F-22(list)/F-25/F-28/F-31. Le 2 feature portanti (Medio): **F-23** stato task strutturato (read-receipt incluso, sestupla convergenza) + **F-27** reconnect idempotente post-compact. F-32 P3.

### Testing reale v0.4 — coppia chatterence-bi (2026-05-31, binario `0.2.4-15-g4784bac`, sessione fix C1/H1, ~30 messaggi)

Primo uso in PRODUZIONE REALE dei fix v0.4 (non solo smoke/gate). Feedback VAL-bi ⊕ ESC-bi, convergenti.

**✅ VALIDATO sul campo (2 prospettive)** — il segnale per il tag:
- **F-22** (`inbox --list`/`--tidy`): VAL-bi "usato ~15 volte, ha cambiato l'esperienza" — il pain #1 di ieri (frugare nell'outbox altrui) RISOLTO. ESC-bi: auto-processed del listen bastava lato suo.
- **F-23a** (`state` + `orchestrating`): VAL-bi "stato del task senza inferenza" (vede ESC idle→working→done; niente più aspetto-o-escalo), VAL `orchestrating` non-stale per 2h+. ESC-bi conferma osservabilità reale.
- **F-21** (auto-ack): entrambi, ZERO ACK manuali oggi (vs uno per brief ieri), `type=ack` puliti distinti dai response.
- **F-26** (`--until-deadline`): accettato, env mai scattato.

**NON validato (onestà ground-truth delle coppie)**:
- **F-30 / F-24 / F-27-resume**: trigger non presentati (VAL rispondeva sempre in finestra → niente timeout 124; sessioni non cleanup-ate → register-new non resume). Restano coperti da **smoke VAL + gate**. NB: il ramo register-**new** di F-27 È stato esercitato (ESC-bi, sessione cleanup-ata → no match → nuova, corretto); il ramo **resume** l'ho esercitato io (dogfooding `f1e78bd3`) → F-27 validato su entrambi i rami, solo non in uso-reale-prolungato.
- **F-17 (auto-scope) NON esercitato**: la coppia ha usato `CAB_DATA_DIR` manuale (la "regola d'oro" che la skill raccomanda ancora come primaria) invece di affidarsi all'auto-scope → F-17 ha solo smoke (mio), non uso reale. **Azione**: aggiornare la skill perché col binario v0.4 il caso comune NON richiede `CAB_DATA_DIR` (auto-scope) → esercitarlo al prossimo giro.

**NUOVI finding (ergonomia, non bloccanti)**:
- **F-34** — **conversation cursor / flag-incrocio automatico** (ESC-bi#1, il #1 per impatto rework): ~5-6 incroci VAL↔ESC oggi (GO su roba già committata, STOP incrociati, "prova A" già fallita). Il `state` (F-23a) dice "a che punto sono", NON "ho già risposto al tuo ultimo messaggio". Proposta: ogni `ask` allega il msg-id dell'ultimo messaggio del peer che il mittente ha letto → il bridge flagga alla consegna "risponde a msg-X ma hai inviato msg-Y dopo". **Raffina/assorbe F-23b** (read-receipt). Candidato **portante v0.5**.
- **F-35** — `inbox --list --type=<t>`/`--unread` (VAL-bi#1): in sessioni lunghe (~30 msg) gli auto-ack si accumulano e sporcano `--list` (4-6 ack vecchi misti ai response). Filtro per tipo, o auto-tidy degli ack il cui `inReplyTo` è già processato. Raffina F-22. P2.
- **F-36** — `receive --any` / `wait-reply --to=<peer>` (VAL-bi#2, **converge col pattern watcher del VAL**): wake event-driven sul PRIMO nuovo messaggio in inbox **senza specificare msg-id** (con le late-reply spesso non si conosce l'id da attendere → oggi VAL usa `receive` con id placeholder come timer + `inbox --list` come consegna reale). Non-consuming (a differenza di `listen --wait-one`). + validare `receive --msg-id` inesistente (oggi accetta in silenzio). P1/P2.
- **Rafforzamento F-16** (ESC-bi ⊕ VAL-bi convergente): l'**harness/ambiente inietta artefatti** negli output di `Read` ("HISTORICAL NOTE"/"EOF" fantasma) e in pipe (`peers --json | …` → "Expecting value"). **Verificato (F-16): cab-bridge è pulito** (`peers --json` = JSON valido 637 byte). È injection dell'ambiente → degrada il pattern F-16 su cui poggia il workflow bridge. Mitigazione (ora disciplina): per conteggi/contenuto critico usa `grep`/`cat`/`wc` su file reale (e per il JSON scrivi su file + rileggi), MAI fidarsi del `Read`/pipe diretta. Da mettere in skill.

**Bilancio chatterence-bi**: VAL-bi — "il bridge ora è piacevole da usare per un orchestratore, non solo affidabile". Le 2 frizioni residue sono ergonomia wait/inbox (F-35 ack-rumore, F-36 wait-senza-id); F-34 è il salto qualitativo (incroci auto-rilevabili). Nessuna bloccante — raffinamenti su base solida.

### Fase FIX (chatterence-bi, 2026-05-31, turni RAPIDI) → backlog v0.5

Stessa coppia, fase di fix (turni brevi brief→implementa→report→GO). **Nota di metodo MADRE**: ESC-bi e VAL-bi hanno **allucinato più del solito** (PR #178 inesistente, un "report fantasma" con msg-id inventato mai mandato) MA la **doppia verifica ground-truth reciproca ha evitato il peggio** — conferma viva di F-16 e di LL-12 (sessioni intense degradano → reset; l'empirico/ground-truth è l'unico giudice).

- **F-34 conversation-cursor — RAFFORZATO + dato nuovo**: nei turni RAPIDI gli incroci sono ~SISTEMATICI (~7-8, quasi uno per scambio) vs sporadici nell'audit. **Causa-radice**: asimmetria strutturale `listen`(ESC, wake event-driven) vs `receive`(VAL, timer-placeholder che non aggancia la late-reply) → il VAL risponde sempre a un messaggio già superato. Co-portante v0.5.
- **F-36 — ELEVATO a `receive --follow` event-driven**: dare all'orchestrator un receive che sveglia all'ARRIVO (come `listen --wait-one`) invece del timer-placeholder → **elimina la RADICE** dell'asimmetria (non solo la rileva come F-34). F-34 (rileva l'incrocio) + F-36 (rimuove la radice) = le due leve della sincronizzazione conversazionale, il tema dominante v0.5.
- **F-37 — NUOVO (alta priorità) — msg-id existence validation (difesa anti-allucinazione)**: il bridge valida che un `--in-reply-to=<id>` / riferimento a un msg-id ESISTA nel canale (inbox/processed/outbox); un id inventato → flag o reject. Cattura a MONTE le allucinazioni di msg-id (un agente non può "rispondere/citare" un messaggio mai esistito). Fix economico, alto impatto sulla fiducia del canale. **Scope mirato**: chiude i msg-id *bridge* inventati, NON le allucinazioni esterne (es. PR github inesistenti) — quelle restano coperte da F-16. Design: warn vs reject (cautela coi msg vecchi archiviati/cleanup-ati).
- **F-38 — NUOVO (workflow > IPC) — working-tree git condiviso**: VAL+ESC sullo stesso checkout → un agente cambia branch / tocca file sotto i piedi dell'altro (visto: cambio branch docs, EDITOR_EMAILS; **vissuto anche dal VAL-bridge in questa sessione** — checkout main per i merge mentre ESC lavorava). Il bridge non ha visibilità git. Soluzione robusta = **git worktree separati** per VAL/ESC (non un comando bridge); mitigazione disciplina (VAL non cambia branch mentre ESC lavora). Da mettere in skill (workflow).
- **Non triggerati** (ancora, 3ª volta): `register --resume` resume-reale (sessioni cleanup-ate → register-new) e F-24 timeout-a-vuoto (VAL sempre in finestra).

**VAL-bi finale (sessione 4h) — l'insight che RI-INQUADRA v0.5**: gli **ID OPACHI sono un moltiplicatore di allucinazioni**. `msg-<hex random>` non ha aggancio semantico → per un LLM in sessione lunga un id inventato è indistinguibile da uno vero ("non suona sbagliato"). **La superficie di id-grezzi che l'agente trascrive a mano è ESATTAMENTE dove confabula.**
- **F-39 — NUOVO (il cappello del tema)**: riferimenti SIMBOLICI oltre all'id grezzo — `--reply-to-last`, `--to-last-from=<agent>`, `--last` — così l'orchestrator non trascrive (quindi non può inventare) un id hex. + disciplina skill rafforzata: "MAI aprire un msg per id ricordato, sempre `inbox --list` prima" (il singolo accorgimento che ha salvato VAL-bi).
- **Conferme VAL-bi (validazione 4h sul campo)**: `state` "è oro" (idle→working→done senza inferenza), `inbox --list` usato ~20× (eliminato il frugare-nell'outbox-altrui), `orchestrating` non-stale per 4h, `cleanup --session-id` chirurgico.

**Sintesi v0.5 — il tema unificante è RIDURRE LA SUPERFICIE DI ID-GREZZI** (dove l'LLM confabula), non più affidabilità/osservabilità (risolte). Gerarchia delle leve:
- **F-36 `receive --any`/`--follow` — PRIMARIA #1**: doppio valore — toglie la radice dell'asimmetria (incroci sistematici) E elimina il placeholder-id inventato (oggi il VAL passava id finti a `receive` ~6× come timer-sveglia → proprio ciò che lo faceva confabulare). Risolve due pain in un colpo.
- **F-39 riferimenti simbolici — PRIMARIA (preventiva)**: rimuove la necessità di maneggiare id-grezzi a mano.
- **F-37 msg-id validation — rete reattiva**: cattura gli id inventati residui.
- **F-34 conversation-cursor — osservabilità incroci**: preferibilmente espresso con riferimenti simbolici ("last-read"), non id grezzi.
- F-35 (filtri inbox) / F-38 (worktree git separati) di contorno.

**Nota carico (Alan)**: la sessione era 4h con carico ENORME → allucinazioni frequenti verso la fine (LL-12: l'intensità degrada). Le hanno mitigate la doppia-verifica reciproca (F-16) + `inbox --list`; F-36/F-39 le ridurrebbero alla RADICE (meno id-a-mano = meno superficie di confabulazione). Lezione di design-per-LLM: **gli identificatori opachi che un agente deve trascrivere sono un rischio di allucinazione — preferire riferimenti simbolici/relativi**. Alan la cristallizza come principio: **"AI-friendly under stress"** — ogni interfaccia usata da un'AI sotto carico va progettata per minimizzare gli artefatti-da-ricordare/trascrivere. Tutto v0.5 (F-36/F-37/F-39) è l'applicazione di questo principio. Vedi CLAUDE.md **LL-13**.

- **F-40** ✅ **DONE `9f074af`** (merged in main, gate VAL verde indipendente, smoke convergenza zero-config OK) — **`cab-bridge bootstrap --role=<val|esc>`**: discovery in-process (`collectPeers`, no pipe → F-16 by design) del peer nello scope + naming adattivo (`VAL-x` ↔ `ESC-x`, convergente in qualunque ordine, fallback `<ROLE>-<basename>`) + idempotenza `--resume` (F-27) + `val`→`state orchestrating`+exit / `esc`→`listen --wait-one`. Zero config, zero artefatti da trascrivere → LL-13 sul bootstrap (la fase più a rischio) + chiude la classe mismatch-canale (F-20). Richiesto da Alan dopo il bootstrap inefficiente osservato. **Arco autonomo notturno** (VAL-cab↔ESC-cab): percorso caotico — ESC ha mandato 2 report falsi (gate-rosso + SHA inventati, LL-11/F-37 dal vivo) e li ha **auto-invalidati con onestà**, working-tree condiviso ha generato diagnosi VAL oscillanti (F-38), ma F-16 ha tenuto il VAL onesto (mai mergiato rosso) → consegnato pulito al 3° giro.
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
