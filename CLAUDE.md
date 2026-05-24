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
- File length: <600 lines per file. Split package if larger.
- Comments: only for non-obvious WHY. Don't repeat what code already says.
- Test naming: `TestXxx_Scenario` pattern. Table-driven where >3 cases.

---

## Critical workflow rules

### Day 0 spike (FIX-4) is OBLIGATORY before any bug fix code
PLAN.md §3.5 + §12. Time-box 4h. Esito determina distribution path (self-marketplace vs pure-PATH fallback). NO codice produzione prima di spike outcome documented in `docs/spike-fix4-distribution.md`.

### Commit discipline
- Commit messages: `<type>(<scope>): <subject>` — types: `feat`, `fix`, `chore`, `docs`, `test`, `refactor`
- Co-author trailer: `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>` mandatory
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

**LL-6 (2026-05-24)** — Velocity Sprint N+1 > Sprint N quando pattern stabilito
Sprint 1 reale: ~1h vs stima 1-1.5 giorni (10-15x più rapido). Cause: (1) Sprint 0 ha stabilito pattern test idiomatic Go + testify + cross-compile, riusabili; (2) refactor layout era file move + json edit, non rewrite logico; (3) ESC ha già caricato dom mental model Go pattern (heartbeat goroutine + lock O_EXCL + atomic write) dal PLAN.md §4.5 componenti. Implicazione planning: stime PLAN.md §5 day-by-day vanno calibrate verso optimistic-realistic post-Sprint 1 (5-7 giorni → probabile 4-5 effettivi). NON ridurre buffer aggressivamente in roadmap pubblica — buffer assorbe surprise spike/blocker, non scope creep. Mantenere 5-7 in PLAN come committed range, segnalare velocità accelerata come bonus interno.

---

## When in doubt

1. Consult `PLAN.md` v3 first
2. Check `memory/decisioni_architetturali_aperte.md` for resolved decisions
3. If new architectural decision needed → STOP, escalate to VAL via brief
4. NEVER make unilateral architecture changes mid-Sprint
