# PLAN_v1 — SNAPSHOT ESC (2026-05-24, OBSOLETO)

> **Status**: Snapshot iter 1, prodotto da ESC dopo primo MISSION_BRIEF. VAL ha valutato e richiesto rework v2.
> **Obsoleto su**: §3.1 (tech decision pre-Go/Rust), §3.3 (naming ora risolto = `cli-agents-bridge`).
> **Riferimenti file**: `/cli-agents-bridge/` (rinominato da claude-bridge).
> **Da usare come**: input contestuale per rework PLAN v2, NON come piano canonico.

---

## Context

Il plugin upstream PatilShreyas/claude-code-session-bridge (tag v0.1.0, commit 8d0816b, marzo 2026, MIT) è un'utility bash+jq per IPC peer-to-peer tra sessioni Claude Code in finestre VS Code separate. Funziona per il caso 1 VAL + 1 ESC short-sprint. Empiricamente verificato in 15+ sub-sprint cross-project (chatterence-bi-template, ac-agents, p1-wp-translator): ha 6 bug critici confermati + 3 bug aggiuntivi non noti + 15+ frizioni UX che lo rendono fragile per il workflow target di Alan (1 VAL + 1-3 ESC, long-run >1h, cross-cwd).

Goal del fork: trasformare il bridge da "MVP demo" a "tool quotidiano robusto" per il workflow triadico VAL↔ESC, mantenendo la filosofia KISS di Alan. Investimento target ~2-3 giorni focus session per MVP v0.2.0, ammortizzato in 1-2 settimane di uso reale.

---

## 1. Executive summary

Fork bash-only drop-in (Strategia A) per v0.2.0 MVP: fix 6 bug critici + 3 aggiuntivi confermati nel source upstream, aggiunta minimale di role/team_id nel manifest, config centralizzata. ~250 LOC scritte/modificate, backward compat con sessioni v0.1.0 esistenti, distribuzione come standalone repo + install script (no marketplace per ora). v0.3.0 deferred: daemon Python opt-in con Unix socket + SQLite WAL, ispirato a raysonmeng/agent-bridge (thin client + daemon). v1.0.0 target: production-ready con observability nativa.

Rifiuto la Strategia B (rewrite Python da subito) per due motivi: (1) i 3 VAL hanno consensus 2/3 su bash-first, (2) il deep-search ha confermato che cocoindex-code/AgentBridge usano pattern ibrido — non è rewrite, è layering. Il daemon entra in v0.3 quando l'MVP avrà validato sul campo le primitive (role routing, heartbeat live).

Naming scelto: `claude-bridge` [OBSOLETO — risolto a `cli-agents-bridge` dopo questa iterazione]. Vedi §3.3.

Effort realistico: 3 giornate ESC focus session per v0.2.0 MVP — onesto, non i 2 giorni dei 3 VAL.

---

## 2. Validazione bug upstream

Repo esaminato: PatilShreyas/claude-code-session-bridge @ commit 8d0816b (tag v0.1.0, unico tag — v0.1.1 citato nei briefing non esiste nel repo, è probabilmente version label interna). File path: `plugins/session-bridge/scripts/*.sh`. Tutti i bug confermati via github-hunter agent.

| ID | Severity | Verdict | File:linee | Note |
|---|---|---|---|---|
| BUG-1 Heartbeat dead in listen loop | 🔴 CRITICAL | CONFIRMED | `bridge-listen.sh:30-68` | Nessuna chiamata a heartbeat.sh nel while loop. lastHeartbeat resta fisso dal register.sh. STALE_SECONDS=300 in list-peers.sh → falsi stale dopo 5 min. |
| BUG-2 bridge-receive.sh timeout secco | 🔴 CRITICAL | CONFIRMED | `bridge-receive.sh:15-43` | `while [ $ELAPSED -lt $TIMEOUT ]` strict. Response post-timeout resta in inbox come pending ma mai consumata. Errore stampato su stdout (vedi BUG-7). |
| BUG-3 Multi-peer routing senza role | 🔴 CRITICAL | CONFIRMED | `register.sh:35-49` | Manifest non ha role/agentName. Routing in send-message.sh accetta qualunque TARGET_ID senza validazione semantica. |
| BUG-4 cleanup.sh globale cross-project | 🔴 CRITICAL | CONFIRMED | `cleanup.sh:55-67` | Glob `$BRIDGE_DIR/sessions/*/manifest.json` scansiona TUTTE le sessioni. Threshold 30min hardcoded. Aggravato da BUG-1: listen mode diventa stale a 30min → cancellato. |
| BUG-5 get-session-id.sh parent-path fallback | 🔴 CRITICAL | CONFIRMED | `get-session-id.sh:25-36` | case esce al primo match. Nessun longest-prefix-match. Ordine non-deterministico (dipende da glob/inode order). |
| BUG-6 Session ID collision per cwd | 🟠 MEDIUM | CONFIRMED | `register.sh:10-24` | File .claude/bridge-session letto da 2 istanze stessa cwd → stesso SESSION_ID, fusione silenziosa. No PID check, no lock. |

### 2.1 Bug aggiuntivi trovati (non noti ai 3 VAL)

- **BUG-7**: `bridge-receive.sh:41` stampa errore "No response received after Ns" su **stdout** invece che stderr. Il chiamante che cattura via command substitution può confondere l'errore con response valida.
- **BUG-8**: incoerenza STALE_SECONDS: `list-peers.sh` usa 300s (5min), `cleanup.sh` usa 1800s (30min). Una sessione può essere "stale" in list-peers ma non ancora cancellata. UX confusing.
- **BUG-9**: `connect-peer.sh` non aggiorna heartbeat del sender. La sessione che fa connect può già essere stale al momento del connect.

### 2.2 Pattern strutturale (architetturale, sotto i bug)

Nessuno script del loop operativo (listen, receive, connect) aggiorna heartbeat. L'intera architettura presuppone che heartbeat venga schedulato esternamente — ma nessun comando /bridge lo schedula. È un design gap, non solo un bug singolo. Il fork deve risolverlo strutturalmente, non patch per patch.

---

## 3. Decisioni architetturali

### 3.1 Strategia tecnologica → A (bash KISS) per v0.2.0, evoluzione C (ibrida bash+Python daemon) opzionale in v0.3+

[OBSOLETO — Alan ha aperto a Go e Rust dopo questa iterazione. §3.1 va rifatta da zero considerando 4 opzioni: bash / Python / Go / Rust.]

**Scelta originale ESC v1**: bash + jq drop-in fix per MVP. Daemon Python opt-in deferred a v0.3.0 (solo se validation MVP genera richiesta reale).

Motivazione originale:
1. 2/3 VAL convergono su bash-first (VAL#2, VAL#3). VAL#1 propone Python daemon ma ammette esplicitamente "Pragmatico: start MVP. Risolve subito i dolori reali".
2. KISS Alan first: Python daemon = process management, launchd plist, error recovery, version upgrade flow. Costo strutturale ~5x bash fix per benefit ~30% (latency da 3s → 30µs, ma il workflow VAL↔ESC non è latency-sensitive — il round-trip umano-on-loop è >30s).
3. Plugin Claude Code è bash-native: marketplace e `claude plugin install` si aspettano bash. Python richiede comunque shim bash entry-point — meglio non avere Python finché non c'è ROI dimostrato.
4. Deep-searcher ha trovato prior art ibrida (raysonmeng/agent-bridge, cocoindex-code): conferma che il pattern corretto è bash thin client + daemon opt-in, NON rewrite completo.
5. Validation prima di scale: 3 giornate MVP risolvono 80% pain con bug confermati. Se dopo 2 settimane d'uso emergono pain bash-irrisolvibili (es. notification real-time critiche), allora v0.3 daemon ha contesto reale.

Tradeoff scartati v1:
- B (Python rewrite): complessità implementation + deploy + Python version dependency. ROI insufficiente per single-user single-machine.
- C ibrida diretta in MVP: scope creep. Triplicare LOC per feature non validata = anti-pattern Alan ("Avoid implementazioni a metà").

### 3.2 Scope MVP v0.2.0 → 8 deliverable, scope chiuso

Massimo 8 deliverable (i 6 bug critici + 2 enabling features):

1. Fix BUG-1 Heartbeat live in bridge-listen.sh polling loop
2. Fix BUG-2 + BUG-7 bridge-receive.sh long-poll fino a max-deadline (default 30 min) + errore su stderr, exit code 124 per timeout esplicito
3. Fix BUG-3 Manifest schema v2 additivo con role + agentName + teamId (vedi §4.3)
4. Fix BUG-4 cleanup.sh default `--scope=my-session`, `--scope=global` opt-in esplicito
5. Fix BUG-5 get-session-id.sh longest-prefix-match algorithm
6. Fix BUG-6 register.sh PID lock check + `--force-new` flag
7. Enabling: `bridge-config.sh` central config loader (risolve BUG-8 + elimina hardcoded values)
8. Enabling: `send-message.sh --file <path>` flag + JSON validation pre-write con jq -e schema check

OUT of scope MVP (deferred v0.3+):
- Daemon Python, Unix socket, SQLite
- Notification osascript/notify-send (incluso solo se aggiunge <20 LOC)
- Conversation thread view, transcript log, dashboard, retry built-in
- Encryption, multi-machine, TUI
- Listen background mode (richiede process management complesso)

### 3.3 Naming → claude-bridge

[OBSOLETO — RISOLTO 2026-05-24: nome definitivo = `cli-agents-bridge`. Vedi memory `decisioni_architetturali_aperte.md` per motivazione completa. NON rimettere in discussione.]

### 3.4 Backward compatibility → Drop-in replacement con schema additivo v2

**Scelta**: drop-in compat layer su filesystem layout + slash commands + script entry-point. Schema manifest/message additivo: v1 readable senza modifiche (defaults safe), v2 emesso dai nuovi register.

Motivazione:
- Migration zero-friction per chi ha già sessioni Patil attive (no data loss)
- Slash commands invariate (/bridge listen, /bridge ask, /bridge peers, /bridge stop) → muscle memory preservata
- Schema additivo elimina rischio breaking change su upgrade futuri
- Fork può convivere col plugin Patil installato (path separato), facilita A/B testing

**[VAL ha contestato]**: cleanup.sh di Patil distruggerebbe sessioni cli-agents-bridge stale (BUG-4 originale non fixato lato Patil). Co-existenza fragile. ESC v2 deve decidere: force uninstall Patil come prereq install vs namespace separato `~/.claude/cli-agents-bridge/sessions/`.

Implementazione v1:
- register.sh scrive `"schemaVersion": 2` + nuovi field
- Script di read fanno `jq -r '.role // "neutral"'` con default safe
- bridge-migrate.sh opzionale per upgrade manifest v1 → v2 (additive only, no data movement)

### 3.5 Distribuzione → Standalone repo + install script bash, NO marketplace per MVP

**Scelta**: GitHub repo public MIT + install.sh che symlinka in `~/.claude/plugins/custom/cli-agents-bridge/`. Marketplace deferred a v1.0.

Motivazione:
- Marketplace richiede approvazione + maintenance commitment: rischio per fork in early stage con breaking change probabili in v0.3
- Custom plugin path bypassa auto-classifier: VAL#1 §1.9 ha empirically verificato che Claude Code blocca self-edit del plugin tree marketplace. Custom path = edit libero per iterazione veloce
  - **[VAL ha contestato]**: VAL#1 §1.9 dice testualmente "Da verificare empiricamente". Era IPOTESI, non fatto. ESC v2 deve verificare nel rework.
- Install script bash KISS: `curl -fsSL .../install.sh | bash` standard nell'ecosistema — niente package manager dependency
- Marketplace v1.0: dopo che API è stabile e ha 1-2 mesi uso reale

Implementazione:
- install.sh clona repo in `~/.claude/plugins/custom/cli-agents-bridge/`
- Symlink commands in `~/.claude/commands/` (compat con slash commands)
- uninstall.sh reversibile
- update.sh git pull + version check

---

## 4. Architettura proposta

### 4.1 Diagramma high-level

```
┌──────────────────────────────────────────────────────────────┐
│  CLI agent instance #1 (VAL)         CLI agent #2 (ESC)      │
│  ┌───────────────────┐                 ┌───────────────────┐ │
│  │ slash command     │                 │ slash command     │ │
│  │ /bridge ask <id>  │                 │ /bridge listen    │ │
│  └────────┬──────────┘                 └────────┬──────────┘ │
│           ▼                                     ▼            │
│  ┌───────────────────┐                 ┌───────────────────┐ │
│  │ bash scripts:     │                 │ bash scripts:     │ │
│  │ send-message.sh   │                 │ bridge-listen.sh  │ │
│  │ bridge-receive.sh │                 │ heartbeat loop    │ │
│  └────────┬──────────┘                 └────────┬──────────┘ │
└───────────┼─────────────────────────────────────┼────────────┘
            │                                     │
            ▼                                     ▼
   ┌────────────────────────────────────────────────────┐
   │  ~/.claude/session-bridge/  (filesystem IPC)       │
   │  ├── config.json              (NEW central config) │
   │  └── sessions/<id>/                                │
   │      ├── manifest.json    (schema v2: role,team)   │
   │      ├── inbox/*.json     (atomic mv + flock)      │
   │      ├── outbox/*.json                             │
   │      └── lock              (PID file, BUG-6 fix)   │
   └────────────────────────────────────────────────────┘
```

Transport: filesystem polling JSON (invariato v0.1.0). Daemon Python: assente in MVP, opzionale v0.3.

### 4.2 Repo structure

```
cli-agents-bridge/
├── README.md                          # Quickstart + features + diff vs Patil
├── CHANGELOG.md                       # v0.2.0 → v1.0
├── LICENSE                            # MIT (compat upstream)
├── ARCHITECTURE.md                    # Design records + diagrams (this PLAN.md merged)
├── install.sh                         # Curl-bash installer
├── uninstall.sh
├── update.sh
├── plugin.json                        # Claude Code plugin manifest
├── commands/                          # Slash command markdown
│   ├── bridge.md                      # Multiplexer (listen/ask/peers/stop/status)
│   ├── bridge-listen.md
│   ├── bridge-ask.md
│   ├── bridge-peers.md
│   ├── bridge-stop.md
│   └── bridge-status.md               # NEW: lightweight status check
├── scripts/
│   ├── bridge-config.sh               # NEW: central config loader (sourced first)
│   ├── register.sh                    # FIX: PID lock, --force-new, schema v2
│   ├── get-session-id.sh              # FIX: longest-prefix-match
│   ├── heartbeat.sh                   # invariato
│   ├── bridge-listen.sh               # FIX: heartbeat loop + MAX_BLOCKING_SECONDS
│   ├── bridge-receive.sh              # FIX: long-poll + stderr + exit 124
│   ├── send-message.sh                # FIX: --file flag, --in-reply-to validation, schema check
│   ├── connect-peer.sh                # FIX: heartbeat update + role check
│   ├── list-peers.sh                  # FIX: role/agentName columns + STALE_SECONDS from config
│   ├── cleanup.sh                     # FIX: --scope my-session default
│   └── lib/
│       └── json-helpers.sh            # jq wrappers + schema validation
├── config/
│   └── default.json                   # Config template (STALE_SECONDS, POLL_INTERVAL, etc.)
├── schemas/
│   ├── manifest-v2.schema.json        # JSON Schema manifest
│   └── message-v2.schema.json         # JSON Schema message
├── tests/
│   ├── unit/                          # BATS tests per ogni script
│   ├── regression/                    # Repro BUG-1..BUG-6 fix verification
│   └── integration/                   # Multi-session via subprocess
└── docs/
    ├── migration-from-patil.md
    ├── multi-esc-patterns.md          # Role routing examples
    └── troubleshooting.md
```

### 4.3 Schema manifest v2 (additivo, backward compat)

```json
{
  "sessionId": "abc123",
  "schemaVersion": 2,
  "projectName": "cli-agents-bridge",
  "projectPath": "/Users/alan/develop/cli-agents-bridge",
  "agentName": "VAL-main",
  "role": "val",
  "teamId": "cli-agents-bridge-fork-2026-05-24",
  "startedAt": "2026-05-24T18:00:00Z",
  "lastHeartbeat": "2026-05-24T18:01:00Z",
  "status": "active",
  "pid": 12345,
  "capabilities": ["query", "context-dump", "conversation"],
  "supportedRoles": ["val"],
  "allowedSenderRoles": ["*"]
}
```

**[VAL ha contestato]**: `teamId`, `supportedRoles`, `allowedSenderRoles` sono YAGNI in MVP. `allowedSenderRoles=["*"]` di default è dead code. Tagliare da v0.2.0, mantenere solo `role`, `agentName`, `pid`, `schemaVersion`.

### 4.4 Schema message v2 (additivo)

```json
{
  "id": "msg-abc123",
  "schemaVersion": 2,
  "from": "session-id-1",
  "fromRole": "val",
  "fromAgentName": "VAL-main",
  "to": "session-id-2",
  "toRole": "esc",
  "type": "query",
  "timestamp": "2026-05-24T18:00:00Z",
  "status": "pending",
  "content": "<payload>",
  "inReplyTo": null,
  "threadId": null,
  "metadata": {
    "fromProject": "cli-agents-bridge",
    "processingState": "pending"
  }
}
```

**[VAL ha contestato]**: `threadId: null` se non c'è view threading è dead field.

---

## 5. Roadmap milestone

### v0.2.0 MVP (target: 3 giornate ESC focus session)

Scope chiuso: 8 deliverable §3.2. Acceptance criteria = 6 regression test sui bug + 5 scenario test §7.

| Day | Deliverable | LOC stimati |
|---|---|---|
| 1 | Clone Patil + setup repo structure + bridge-config.sh + lib/json-helpers.sh + tests skeleton | ~80 |
| 1 | Fix BUG-1 (heartbeat loop) + BUG-3 (manifest v2 schema) + register.sh refactor | ~70 |
| 2 | Fix BUG-2/BUG-7 (long-poll + stderr) + BUG-5 (longest-prefix) + BUG-6 (PID lock) | ~60 |
| 2 | Fix BUG-4 (scope cleanup) + BUG-8 (config unified) + BUG-9 (connect heartbeat) | ~50 |
| 3 | send-message --file + schema validation + role compat + slash command refresh | ~40 |
| 3 | Regression tests + install.sh + README + smoke test multi-VS Code | (no prod LOC) |

Total: ~300 LOC scritte/modificate, ~150 LOC test BATS.

Release criteria:
- 6 regression test verde su BUG-1..BUG-6
- Backward compat: una sessione Patil v0.1.0 esistente continua a funzionare contro un peer cli-agents-bridge v0.2.0
- Install script idempotente (run 2x non rompe nulla)
- README con quickstart 5-min + differenze vs Patil

### v0.3.0 (target: 1-2 settimane post-MVP validation)

Scope deciso DOPO 1-2 settimane uso reale MVP. Candidate features:
- Notification on receive (osascript/notify-send, ~30 LOC)
- bridge transcript log persistente + bridge thread <id> view
- bridge status dashboard (text output)
- --retry N --backoff su send-message
- Listen background mode con file lock + SIGUSR1
- Auto-gc orphan sessions in register

### v0.4.0 — Daemon Python opt-in (target: 1-2 settimane, GATED)

[OBSOLETO sulla scelta Python — Alan ha aperto a Go come opzione daemon. ESC v2 valuta Go vs Python per daemon.]

Gate condition: 2+ pain point validati che bash non risolve. Se MVP basta, non si fa.

### v1.0.0 target (3-6 mesi)

- Marketplace submission Claude Code
- Encryption opt-in (libsodium)
- Multi-machine via Tailscale ACL
- Observability metrics Prometheus-style
- Documentation completa + esempi

---

## 6. Strategy backward compat — Migration da Patil v0.1.0

### 6.1 Coesistenza filesystem

cli-agents-bridge legge/scrive sullo stesso `~/.claude/session-bridge/sessions/` di Patil. Schema v2 additivo: campi extra ignorati dai script Patil, campi mancanti hanno defaults safe nei script cli-agents-bridge.

**[VAL ha contestato]**: scenario fragile. cleanup.sh di Patil cancella sessioni stale > 30min senza distinzione di schema → data loss possibile. Decisione hard richiesta in v2: force uninstall Patil come prereq OR namespace separato.

### 6.2 Migration script opzionale

`scripts/migrate-from-patil.sh`:
- Scansiona `~/.claude/session-bridge/sessions/*/manifest.json`
- Per ogni manifest v1, aggiunge campi v2 con defaults
- Backup pre-migration in `~/.claude/session-bridge/migration-backup-<date>/`
- Idempotente (re-run safe)

### 6.3 Coexistenza con plugin Patil installato

- install.sh rileva plugin Patil in `~/.claude/plugins/marketplace/`
- Warning + opzione: (a) keep both (slash command Patil resta /bridge, cli-agents-bridge usa /cb), (b) replace (uninstall Patil + install cli-agents-bridge a /bridge)
- Default = warning + manual choice

---

## 7. Testing strategy

### 7.1 Unit (BATS — Bash Automated Testing System)

Per ogni script: 3-5 test case (happy path, error path, edge case). Mock filesystem + jq via temp dirs.

### 7.2 Regression — 6 test, uno per BUG critico

| Test | Repro | Pass criteria |
|---|---|---|
| BUG-1 | Spawn bridge-listen 10 min idle | lastHeartbeat in manifest < 90s dal NOW per tutta la durata |
| BUG-2 | VAL send con timeout=10s, ESC risponde a 30s | bridge-receive --max-deadline=60 ritorna response (no perdita) |
| BUG-3 | Setup 1 VAL + 2 ESC, ESC tenta send a peer role=esc | Send blocca con errore "esc→esc forbidden (use --allow-mesh)" |
| BUG-4 | Sessione X attiva + sessione Y stale 35min in altro project | cleanup.sh default lascia entrambi (scope my-session) |
| BUG-5 | /p1/ + /p1/sub/ registered, invoke da /p1/sub/nested/ | get-session-id ritorna ID di /p1/sub/ |
| BUG-6 | 2 istanze Claude in stessa cwd, both fanno register | Seconda istanza fail con error "session already locked by PID X, use --force-new" |

### 7.3 Integration — 5 scenari multi-process

Spawn N processi bash che simulano sessioni:
1. 1 VAL + 1 ESC round-trip 10 messaggi
2. 1 VAL + 2 ESC role routing
3. Long-run 30min listen senza messaggi (heartbeat persistence)
4. Cleanup cross-project safety (3 progetti attivi)
5. Backward compat (1 peer Patil v0.1.0 + 1 peer cli-agents-bridge v0.2.0)

### 7.4 Smoke test manuale finale

Pre-release: 2 finestre VS Code reali, cli-agents-bridge installato in entrambe, eseguire `tests/smoke-test.md` checklist (15-20 step). Verifica empirica come VAL ha fatto nei 15 sub-sprint.

---

## 8. Risk register

| Risk | Probability | Impact | Mitigation |
|---|---|---|---|
| R1: Schema v2 rompe sessioni Patil esistenti | Media | Alto | Schema additivo + integration test cross-version (§7.3 scenario 5) |
| R2: 3 giorni MVP slip a 5+ giorni (scope creep) | Alta | Medio | Scope chiuso §3.2. Tier 2/3 deferred fisicamente in roadmap, no inline temptation |
| R3: Auto-classifier Claude Code blocca edit custom plugin | Media | Alto | Custom path `~/.claude/plugins/custom/` (non marketplace) — DA VERIFICARE empiricamente in v2 |
| R4: flock su macOS si comporta diversamente da Linux | Bassa | Medio | Test su Darwin 25.x specifico durante MVP day-1 |
| R5: Plugin Patil upstream rilascia v0.2 con fix sovrapposti | Bassa | Basso | Fork mantiene compat schema → merge selettivo possibile |
| R6: 1 VAL + N ESC pattern troppo niche, ROI fork insufficiente | Bassa | Medio | Empirical evidence Alan 15+ sub-sprint → ROI già validato pre-fork |

---

## 9. Open questions per VAL (originali ESC v1)

1. Repo GitHub: vuoi un repo public MIT da day-1 oppure repo privato fino a v0.3.0? (suggerimento ESC: privato fino a v0.2.0 stabile, poi public)
2. Branch strategy fork: clone Patil come git remote upstream + main proprio, oppure fork pulito senza relazione git? (suggerimento ESC: clone con upstream remote per merge selettivo futuro)
3. Slash command collision: se Alan ha già plugin Patil attivo, durante MVP development useremo /cb temporaneo o sovrascriviamo /bridge? (suggerimento ESC: /cb durante dev, poi swap a /bridge al release v0.2.0)
4. Co-existence ac-agents/chatterence sessioni live: durante development fork potremmo accidentalmente toccare bridge dir condivisa. Backup pre-dev `~/.claude/session-bridge/` consigliato? (suggerimento ESC: SÌ, backup + use isolated BRIDGE_DIR=/tmp/bridge-dev/ durante test)
5. Test su quale macchina: MVP test cross-VS-Code richiede 2 finestre. Confermi disponibilità ~30 min Alan-time per smoke test pre-release?

---

## 10. Next step concreto — primo PHASE 2 brief (proposto v1)

[OBSOLETO — il prossimo step ora è PLAN v2 rework, NON Sprint 0 implementation. Vedi MISSION_BRIEF_v2 per istruzioni rework.]

---

**End of PLAN_v1 snapshot.** Da usare come reference per rework v2.
