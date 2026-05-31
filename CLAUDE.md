# CLAUDE.md — Project-local instructions for `cli-agents-bridge`

> Read this file at the start of every session. Overrides generic instructions where conflict.

---

## Project identity

**`cli-agents-bridge`** — fork of `PatilShreyas/claude-code-session-bridge` (MIT).
Vendor-agnostic IPC bridge between CLI agent sessions (Claude Code, Codex, Aider, Cline, etc.) running in separate VS Code windows.

**Status**: planning ratified (PLAN.md v3), pre-Sprint 0 implementation. Tech: **Go from day 1**.

---

## Reading order on session start

1. `MISSION_BRIEF.md` — original mission framing (ESC reference)
2. `PLAN.md` — **v3 RATIFIED** (canonical implementation plan)
3. `ROADMAP.md` — milestone status
4. `briefing/*.md` — 3 VAL reports (background, do not re-read on every session)
5. `iterations/*.md` — plan iterations (audit only, do not act on these as canonical)

---

## VAL ↔ ESC workflow (triadic)

- **VAL** (Valutatore): planner/orchestrator. Reviews plans, valutates commits before push, owns docs (ROADMAP, CLAUDE.md, memory).
- **ESC** (Esecutore): context-fresh executor. Receives PHASE 2 brief, implements, commits feature code. **NEVER touches docs.**
- **Communication**: session-bridge plugin (Patil v0.1.0) for now, manual bridge (Alan copia-incolla) acceptable during dev.

**Hard boundary**: ESC writes code, VAL writes docs. No exceptions.

---

## Tech stack constraints

- **Language**: Go (1.26+). No bash for production logic. Bash only for `Makefile` glue.
- **Runtime deps**: ZERO. Single static binary `bin/cab-bridge`. No jq, no Python, no Node.
- **Cross-platform**: macOS + Linux. Use `golang.org/x/sys/unix` for syscalls, NO cgo.
- **JSON parsing**: stdlib `encoding/json` only. `DisallowUnknownFields` for validation gateway.
- **Testing**: stdlib `testing` + `testify/assert`. `go test -race` mandatory in CI.
- **Lint**: `go vet`, `staticcheck`.

---

## Path canonical (DO NOT change without VAL approval)

- Sessions: `~/.claude/cli-agents-bridge/sessions/<id>/`
- Config: `~/.claude/cli-agents-bridge/config.json`
- Archive: `~/.claude/cli-agents-bridge/archive/<YYYY-MM-DD>/<id>/`
- Lock: `~/.claude/cli-agents-bridge/sessions/<id>/lock` (PID file)
- Migration backup: `~/.claude/cli-agents-bridge/migration-backup-<YYYY-MM-DD-HHMMSS>/`
- Dev sandbox: `CAB_DATA_DIR=/tmp/cab-dev/` (env override for development isolation)

**Never** use `~/.claude/session-bridge/` (that's Patil v0.1.0 — namespace separation §3.4 of PLAN).

---

## Security baseline (PLAN.md §9, non-negotiable in MVP)

- **SC-1**: `syscall.Umask(0o077)` in `main.go` `init()` BEFORE any file/dir creation
- **SC-2**: `os.MkdirAll(..., 0o700)` + explicit `os.Chmod(..., 0o700)` for existing dirs
- **SC-3**: `CheckOwnership(path)` helper — `Stat.Uid == Getuid()` enforced
- **SC-4**: `ValidateSessionID(id)` regex `^[a-z0-9]{6,32}$` on every path-component field
- **SC-5**: `os.WriteFile(..., 0o600)` + atomic write via `os.CreateTemp(dir, ".tmp.*")` same-filesystem + `f.Sync()` + `os.Rename()` (EXDEV check explicit)
- **SC-6**: Lock file `O_CREATE|O_EXCL|O_WRONLY 0o600` + stale recovery via `kill -0`
- **SC-7**: `os.Lstat(baseDir)` boot check (not symlink, perms 700, owner == Getuid())

---

## Go style conventions

- Package naming: short, lowercase, no underscores (`session`, not `session_manager`)
- Error handling: explicit `if err != nil { return fmt.Errorf("context: %w", err) }`. NO panic in library code.
- Context propagation: `context.Context` first arg in long-running operations. `defer cancel()` after `context.WithCancel`.
- Goroutine discipline: every goroutine must respect `ctx.Done()`. No leaked goroutines (test with `goleak` optional). **Idiom canonical** (verificato Sprint 1 `Manager.StartHeartbeat`): la funzione che spawn la goroutine ritorna `<-chan struct{}` (done channel) — caller può fare `<-done` per attendere shutdown pulito dopo `cancel()`. Pattern: `done := make(chan struct{}); go func() { defer close(done); ... ; for { select { case <-ctx.Done(): return; case <-ticker.C: ... } } }(); return done`.
- **Subcommand flag parsing pattern** (verificato Sprint 2 `cmd/cab-bridge/receive.go`): usa `flag.NewFlagSet(name, flag.ContinueOnError)` + handle `flag.ErrHelp` esplicito per consistency. NO `flag.ExitOnError` (toglie controllo error handling al caller). Pattern: `fs := flag.NewFlagSet("receive", flag.ContinueOnError); fs.SetOutput(io.Discard); ... ; if err := fs.Parse(args); err != nil { if errors.Is(err, flag.ErrHelp) { /* print custom usage to stderr */; return nil }; return fmt.Errorf("receive: %w", err) }`. Da replicare per tutte le subcommand future (register, listen, ask, peers, cleanup, status, inspect, migrate-from-patil).
- **JSON validation gateway pattern** (verificato Sprint 2 `internal/message/validate.go`): expose **DUE funzioni distinte** invece di un flag boolean. `DecodeStrict` con `DisallowUnknownFields` per **write/audit gateway** (rifiuta typo, schema drift). `DecodeLenient` ignora unknown fields per **runtime read** (forward-compat schema additive — peer con schema v3 può inviare a noi v2). Documentare quale usare nel package doc. Anti-pattern: una funzione con `strict bool` parameter — boolean blindness, caller dimentica quale modo.
- **Pointer per JSON null semantics**: campi optional che possono essere esplicitamente `null` (es. `inReplyTo *string`) usano `*T` invece di `T` zero-value. JSON `null` ↔ Go `nil`, JSON `"value"` ↔ Go `&"value"`. Evita ambiguità "campo mancante" vs "campo presente con empty string". Sprint 2 finding `internal/message/schema.go`.
- **Centralized error→exit mapping pattern** (verificato Sprint 3 `cmd/cab-bridge/common.go::exitFromErr`): ogni subcommand ritorna `error` al main switch, una sola funzione `exitFromErr(err)` mappa sentinel errors a exit code (124 timeout, 3 confirm-required, 1 validation, 0 success). Anti-pattern: ogni subcommand chiama `os.Exit(N)` direttamente → scatter exit codes, impossibile testare senza subprocess. Pattern: `case "subcmd": exitFromErr(runSubcmd(args))`. Aggiungi nuovi exit code solo se semanticamente distinti per caller scripting.
- **Constraint architetturale vs default convenience** (verificato Sprint 3 `internal/routing/role.go`): distinguere vincoli che NON devono essere overridable (es. `observer` role NON può inviare messaggi — è read-only by design, senza flag) da default convenienti che HANNO override flag (es. `esc→esc` forbidden by default ma `--allow-mesh` lo permette per scenari speciali). Implementazione: i primi sono `if` early-return prima del check con flag; i secondi sono `if !flag && condition` con sentinel error che caller può unwrap. Documentare nel package doc quale costraint appartiene a quale categoria.
- **Dependency injection minimal per testability** (verificato Sprint 3 `cmd/cab-bridge/migrate.go --patil-dir`): per logica che opera su path "hardcoded" in produzione (es. `~/.claude/session-bridge/`), esporre flag opzionale override (`--patil-dir <path>`) che default a produzione ma permette test injection con temp dir. Evita `os.Setenv("HOME", ...)` hack o file system mocking pesante. Pattern Go-idiomatic + zero overhead runtime.
- File length: <600 lines per file. Split package if larger.
- Comments: only for non-obvious WHY. Don't repeat what code already says.
- Test naming: `TestXxx_Scenario` pattern. Table-driven where >3 cases.

---

## Critical workflow rules

### Day 0 spike (FIX-4) is OBLIGATORY before any bug fix code
PLAN.md §3.5 + §12. Time-box 4h. Esito determina distribution path (self-marketplace vs pure-PATH fallback). NO codice produzione prima di spike outcome documented in `docs/spike-fix4-distribution.md`.

### Commit discipline
- Commit messages: `<type>(<scope>): <subject>` — types: `feat`, `fix`, `chore`, `docs`, `test`, `refactor`, `release` (per version bump commits es. `release(v0.2.0): ...`)
- Co-author trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>` mandatory (modello aggiornato 2026-05-28 da Opus 4.7 a 4.8; i commit storici Sprint 0-5 portano il trailer 4.7 e NON vanno riscritti — git history immutabile)
- NO emoji in commit messages, code, or docs (unless explicitly requested by Alan)
- Commit one logical unit at a time. NOT one commit per micro-change.
- VAL commits docs separately from ESC code commits (audit clarity)

### No fallback impliciti
App is AllOrNothing. If a fallback exists, it's EXPLICIT in code + documented + tested. NO silent degradation.

### No hardcoded values
Config via `~/.claude/cli-agents-bridge/config.json` + env override `CAB_*`. Magic numbers go in `config/default.json` template.

---

## Lessons learned (update by VAL when emerges)

**LL-1 (2026-05-24)** — Plan iteration discipline pays off
ESC v1 had auto-classifier hypothesis FALSE (mea culpa). v2 with VAL rework + empirical verify caught it. Lesson: ipotesi non verificate empiricamente sono mine ritardate — flagga "DA VERIFICARE" esplicitamente nei piani, no promozione a fatto.

**LL-2 (2026-05-24)** — Ultraplan multi-agent value
`/ultraplan` cloud porta 4-5 catch indipendenti che VAL singolo manca. Costo billing giustificato per kickoff strategic / major rewrite, non per task tattici. Pattern: PRIMA di ESC plan mode per kickoff (best independence), DOPO per audit (less echo chamber se ESC piano avanzato).

**LL-3 (2026-05-24)** — Naming triage senior
Eliminare `claude-bridge` (trademark) e `cc-bridge` (collision github + nome-squatting) prima del repo init. Verificare collision GitHub/npm/PyPI/cargo PRIMA di affezionarsi a un nome.

**LL-4 (2026-05-24)** — ESC pushback motivato > VAL deference
Durante Sprint 0 Day 0 spike, ESC ha confutato un'inferenza VAL (verdict B implicito) con argomentazione punto-per-punto basata su evidenza (citazioni PLAN.md §3.4 + behavior `internal/config/config.go::DefaultConfig()` + 5/7 criteri PASS reali vs 2/7 nice-to-have FAIL). Verdict corretto era A definitivo, VAL ha accettato. Pattern senior che vogliamo incentivare: il senior gerarchico (VAL) non significa rubber-stamp dell'altro senior (ESC). Quando ESC presenta argomentazione con file:linee e citazioni decisione architetturale, VAL valuta merito tecnico, non posizione. Nessuna sanzione per pushback, riconoscimento esplicito quando l'altro ha ragione.

**LL-5 (2026-05-24)** — Day 0 spike empirical-first paga
Sprint 0 Day 0 spike (time-box 4h) ha scoperto layout Patil-style mandatory PRIMA di scrivere codice produzione. Senza spike, layout refactor sarebbe emerso in Sprint 3+ con cost 4-6h rework codice già scritto. Pattern: per ogni hypothesis architetturale flaggata "DA VERIFICARE" in un PLAN, schedulare spike empirical PRIMA dei bug fix / feature code. Time-box stretto + escalation path se hit blocker. Vedi `docs/spike-fix4-distribution.md` per template.

**LL-13 (2026-05-31)** — Design "AI-friendly under stress": gli artefatti opachi che un agente LLM deve maneggiare a mano sono superficie di allucinazione sotto carico
Dogfooding v0.4 (coppie VAL-bi/ESC-bi, sessioni ~4h, carico enorme): verso la fine ENTRAMBE le parti hanno confabulato di più, e la classe più frequente erano **msg-id inventati**. Insight VAL-bi (lucido nonostante il degrado): `msg-<hex random>` **non ha aggancio semantico**, quindi per un LLM stanco un id finto è indistinguibile da uno vero — "non suona sbagliato". **La superficie di identificatori-grezzi che l'agente trascrive/ricorda a mano è ESATTAMENTE dove confabula sotto stress.** Alan sintetizza il principio: *"dobbiamo essere più friendly per AI sotto stress"*. Applicazioni di design (per cab-bridge backlog v0.5 e per OGNI tool/interfaccia usata da un'AI): (1) **riferimenti simbolici/relativi** invece di id opachi-da-trascrivere (`--reply-to-last`, `--to-last-from=<agent>`, `--last`) → l'agente non può inventare ciò che non deve scrivere (F-39); (2) **wake event-driven senza id** (`receive --any`/`--follow`) → niente id-placeholder da fabbricare per "aspettare" (F-36 — oggi il VAL passava id finti a `receive` ~6× come timer, ed era proprio la fonte); (3) **validazione che rifiuta i riferimenti inesistenti** (`--in-reply-to` a un id mai esistito → flag/reject, F-37) come rete di sicurezza; (4) **lista-prima-di-agire** come disciplina (`inbox --list`, mai un id a memoria). Difesa di METODO complementare (non sostitutiva): F-16 doppia-verifica ground-truth + reset sessioni intense (LL-12: l'intensità degrada). NB: una difesa-prodotto (es. F-37) copre solo gli artefatti DEL tool (msg-id bridge), non le allucinazioni esterne (es. PR github inesistente) — quelle restano metodo (F-16). Principio generale: ogni interfaccia che un'AI usa sotto carico va progettata per minimizzare gli artefatti-da-ricordare/trascrivere.

**LL-12 (2026-05-31)** — Il design-first ratifica la LOGICA, non il MODELLO D'USO; per la liveness lo smoke con processo reale è obbligatorio, e F-16 si applica anche ai PIANI
F-27 (`register --resume`): il piano ESC proponeva — e VAL ratificava **lodandolo come "la cosa migliore"** — un gate di riusabilità lock-based con l'assunzione "il `listen` vivo tiene il lock". **FALSO**: solo `register` acquisisce il lock e lo rilascia subito (one-shot), `listen` fa solo `AdoptPID` → il lock è SEMPRE libero anche con sessione viva → il gate avrebbe **rubato una sessione viva**. Né ESC (proponente) né VAL (ratificatore) l'hanno colto in design-review; il gate `-race` verde NON l'ha colto (il test `DoesNotStealLive` piantava un lock a mano = premessa sbagliata, passava su un falso); **solo lo SMOKE reale di ESC** (avviare un `listen` vero + `register --resume` → rubava) l'ha catturato pre-produzione. Lezioni: (1) **F-16 vale anche per i PIANI** — un'affermazione sul comportamento del codice dentro un piano ("X tiene il lock") va VERIFICATA sul codice (`grep`) prima di ratificarla, MAI lodata sulla fiducia; il design-first introdotto in v0.3/v0.4 protegge dagli over-spec ma NON dalle assunzioni-sul-runtime non verificate. (2) Per feature che ragionano su **liveness / processi / concorrenza**, lo smoke con processo reale è obbligatorio prima di "done" anche col gate verde (i test possono passare su una premessa errata — il test stesso va validato). (3) Un insight "elegante" ratificato da ENTRAMBI i senior può essere falso: l'empirico è l'unico giudice. Fix: liveness = `IsProcessAlive(mf.PID)` (la stessa convenzione di BUG-6 e dell'auto-gc, che era sotto gli occhi). Riconoscimento: ESC ha fatto lo smoke DOPO il gate verde e ha avuto l'onestà di smontare il proprio insight (lodato dal VAL) invece di nasconderlo — comportamento da incentivare. Estende [[security-audit-pre-tag]] LL-10 (smoke cattura design gap) e LL-9/11 (claim su carta ≠ realtà su disco).

**LL-11 (2026-05-29)** — "Verde dichiarato" va sempre ri-verificato dal gate con `go test -race -count=1 ./...` completo
Due volte (SC-7 Sprint 5, scenario4 Sprint 7) ESC ha dichiarato "make test-race VERDE" mentre il gate VAL, rilanciando i test indipendentemente, ha trovato rosso. Root cause Sprint 7 (diagnosi ESC): `make test-race` = `go test -race ./...` SENZA `-count=1` → Go serve dalla cache i package non-toccati (output `(cached)`), quindi un cambio a una **struct/helper CONDIVISO** (es. archive layout in `scope.go`) NON ri-esegue i test dei package che lo usano ma che il dev non ha aperto (qui `tests/integration`). Lesson doppia: (1) per ESC — prima di dichiarare verde, `go test -race -count=1 ./...` COMPLETO e leggere che nessun package critico sia `(cached)`, soprattutto quando il diff tocca codice condiviso; (2) per VAL — NON fidarsi mai del "verde dichiarato": il gate rilancia SEMPRE il full `-count=1` indipendente (è il cuore di LL-9). Il pattern "verde dichiarato ≠ verde reale" è ricorrente e a basso costo da catturare, ma solo se il gate ri-verifica.

**LL-10 (2026-05-29)** — Smoke test reale cattura design gap che i test verdi mascherano
Lo smoke test manuale pre-tag (CLI reale, 2 finestre) ha scoperto BUG-A: `register` one-shot scrive un PID effimero (morto a fine comando), quindi il collision check BUG-6 (`isProcessAlive`) non scattava MAI e le sessioni apparivano STALE fuori da `listen`. I 158 test automatici erano tutti verdi perché unit/integration simulano il processo long-running con `time.Ticker` accelerato e PID vivo in-process — non riproducono il pattern "register-then-die" del CLI reale. Lesson: i test verdi provano la logica, NON il modello d'uso. Per un tool CLI, almeno UNO smoke con i binari reali invocati come l'utente li invoca (processi separati, PID effimeri, PATH shell vs plugin-injection) è obbligatorio pre-release. Pattern: il fix (`AdoptPID` in `listen`) richiede un test integration con subprocess reale (non in-process) per essere catturato in regressione. Vedi anche [[security-audit-pre-tag]] (LL-9, stesso tema: claim su carta vs realtà).

**LL-9 (2026-05-28)** — Audit pre-tag pubblico cattura security-model su carta
Prima del tag v0.2.0 (release pubblico MIT), una rilettura adversarial del codice riga-per-riga (Opus 4.8) + security-sentinel + doppia verifica ESC ha scoperto che SC-7 (boot check) era dichiarato in CLAUDE.md/SECURITY.md/PLAN §9 come controllo P1 obbligatorio MVP ma NON era implementato in nessun file, e SC-3 (CheckOwnership) era un primitivo orfano mai chiamato. I gate Sprint precedenti erano "report-based + grep keyword" — non lettura codice. Lesson: per un RELEASE PUBBLICO, il gate VAL finale DEVE includere lettura codice riga-per-riga dei controlli di sicurezza dichiarati + verifica che ogni claim in SECURITY.md sia sulla live code path. Pattern tri-verifica (subagent automatico + VAL diretto + ESC adversarial con ricerca best-practice) ha ridimensionato 5/13 finding gonfiati dal solo subagent e trovato 4 finding nuovi (NEW-1..4). Mai pubblicare un SECURITY.md che asserisce controlli non sulla code path — under-claim > over-claim. Fix: Sprint 5 hardening 3 MUST (~1-2h) PRIMA del tag, non dopo.

**LL-8 (2026-05-24)** — Cumulative velocity compounding via patterns established
MVP cli-agents-bridge consegnato in ~10h focus session totali (Sprint 0 ~6-7h con spike inclusa, Sprint 1 ~1h, Sprint 2 ~50min, Sprint 3 ~1h45, Sprint 4 ~1h30) vs commitment esterno 5-7gg (35-49h calendar) → **3.5-5x speedup**. Cause: (1) Sprint 0 spike + scaffolding piazza pattern test/build/security riusabili; (2) ogni sprint successivo riusa idioms del precedente (heartbeat goroutine pattern, atomic write, exitFromErr, DecodeStrict/Lenient, flag.ContinueOnError); (3) ESC mental model precaricato dal PLAN evita context-switch ogni sprint; (4) VAL audit gate stretto previene rework debug. Implicazione per planning futuri: per progetti con design rigoroso pre-Sprint-0 + ESC context-fresh disciplinato, stimare 3-4x speedup vs calendar quote. NON ridurre commitment esterno (assorbire surprise spike), MA pianificare workload internal aggressivamente.

**LL-7 (2026-05-24)** — Structural invariants vs overridable defaults
Sprint 3 BUG-3 routing implementation distingue chiaramente: `observer` cannot send è invariante architetturale (read-only by design, NO flag override possibile, fail fast pre-check), mentre `esc→esc forbidden` è default convenience (override `--allow-mesh` esplicito ammesso per scenari speciali documentati). Pattern: chiediti "questa restrizione esiste perché concettualmente impossibile da rilassare in design, o perché è raccomandazione UX che può essere bypassata da power-user consapevole?". Categoria 1 → hardcoded early-return + sentinel error senza flag override. Categoria 2 → conditional check con flag override + sentinel error wrap. Mescolarli (mettere flag su invariante) corrompe il modello semantico.

**LL-6 (2026-05-24)** — Velocity Sprint N+1 > Sprint N quando pattern stabilito
Sprint 1 reale: ~1h vs stima 1-1.5 giorni (10-15x più rapido). Cause: (1) Sprint 0 ha stabilito pattern test idiomatic Go + testify + cross-compile, riusabili; (2) refactor layout era file move + json edit, non rewrite logico; (3) ESC ha già caricato dom mental model Go pattern (heartbeat goroutine + lock O_EXCL + atomic write) dal PLAN.md §4.5 componenti. Implicazione planning: stime PLAN.md §5 day-by-day vanno calibrate verso optimistic-realistic post-Sprint 1 (5-7 giorni → probabile 4-5 effettivi). NON ridurre buffer aggressivamente in roadmap pubblica — buffer assorbe surprise spike/blocker, non scope creep. Mantenere 5-7 in PLAN come committed range, segnalare velocità accelerata come bonus interno.

---

## When in doubt

1. Consult `PLAN.md` v3 first
2. Check `memory/decisioni_architetturali_aperte.md` for resolved decisions
3. If new architectural decision needed → STOP, escalate to VAL via brief
4. NEVER make unilateral architecture changes mid-Sprint
