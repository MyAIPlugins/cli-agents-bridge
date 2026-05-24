# ROADMAP — `cli-agents-bridge`

> Vista milestone del progetto. Per dettaglio tecnico vedi `PLAN.md` (v3 RATIFIED).
> Aggiornato dal VAL ad ogni completamento milestone.

---

## Status corrente — 2026-05-24

**Fase**: 🟢 **Sprint 0 DONE** (commit `c142c8d`) → 🟡 **Pre-Sprint 1**

**Sprint 0 deliverable consegnati**:
- Day 0 FIX-4 spike eseguito → Esito A definitivo (self-marketplace verified end-to-end in Claude Code 2.1.150)
- Go module baseline `github.com/myAIPlugins/cli-agents-bridge` + cross-compile matrix (darwin-arm64, linux-amd64, linux-arm64, no cgo)
- Security P0 implementati: SC-1 umask 077, SC-3 ownership check, SC-4 ValidateSessionID regex, EnforceDirPerms 700/600
- 4 funzioni Test × 27 sub-test pass con `go test -race` clean
- `.github/workflows/ci.yml`: go vet + test -race + cross-compile matrix
- Docs stub: README, LICENSE MIT, ARCHITECTURE, CHANGELOG, PRIVACY, SECURITY, spike-fix4-distribution
- Layout finding: Patil-style subdir mandatory per marketplace install (Sprint 1 day 1 refactor)
- Caveat documentato: `${CLAUDE_PLUGIN_DATA}` + `${CLAUDE_PLUGIN_ROOT}` EMPTY in 2.1.150 (non bloccante, architettura `$HOME`-derived)

**Sblocco Sprint 1**: nessuno (decisioni chiuse, baseline pronta). Sprint 1 day 1 = layout Patil-style refactor pre bug fix.

---

## Milestone overview

| Milestone | Status | Target | Note |
|---|---|---|---|
| **M0** Planning ratification | ✅ DONE | 2026-05-24 | PLAN v3 RATIFIED (synthesis ESC v2 + ultraplan + VAL gate) |
| **M1** Sprint 0 — Day 0 spike + Go baseline | ✅ DONE | 2026-05-24 | commit `c142c8d`, Esito A definitivo, security P0 + Go module + CI cross-compile |
| **M2** Sprint 1-4 — Layout refactor + Bug fix + tests | ⏳ NEXT | +4-5 giorni | Sprint 1 day 1 = layout Patil-style refactor (1-2h pre bug fix). Poi 9 regression test + 5 scenario integration |
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
