# PLAN.md v2 — Fork `cli-agents-bridge`

> **Iterazione 2** (rework richiesto da VAL su 7 FIX). PLAN_v1_ESC.md archiviato in `iterations/`.
> **Data**: 2026-05-24. **Autore**: ESECUTORE (sessione fresh post rework brief).
> **Status**: PROPOSED, pronto per re-valutation VAL.

---

## Context

Fork del plugin `PatilShreyas/claude-code-session-bridge` v0.1.0 (commit `8d0816b`, MIT, marzo 2026). Empiricamente: **6 bug critici + 3 bug aggiuntivi confermati** (vedi §2), **15+ frizioni UX** documentate da 3 VAL convergenti in `briefing/`.

**Cambio strategico v2 vs v1** (post-rework):
- **Tech**: bash drop-in → **Go rewrite** (single binary, type safety, 10y compat, MVP 4 giorni vs 3 di bash)
- **Backward compat**: shared dir Patil → **namespace separato** `~/.claude/cli-agents-bridge/`
- **Distrib**: install.sh in custom plugin path inesistente → **self-marketplace GitHub** (path reale `${CLAUDE_PLUGIN_DATA}`)
- **Security baseline esplicita in MVP** (5 control P0, 2 P1)
- **Schema manifest trimmed**: rimossi `teamId`, `supportedRoles`, `allowedSenderRoles`, `threadId` (YAGNI)
- **Metriche successo MVP misurabili** (§10)

---

## 1. Executive summary

Rewrite del plugin in **Go single binary** distribuito via **self-marketplace GitHub** (`/plugin marketplace add tuo-org/cli-agents-bridge`). Risolve 9 bug confermati nel source upstream + introduce role routing, longest-prefix session lookup, atomic write con `fdatasync`, security baseline P0/P1 (umask 077, perms 700/600, ownership check, session-ID regex validation, lock file safe). Namespace storage separato (`~/.claude/cli-agents-bridge/sessions/`) per eliminare rischio cleanup cross-distruttivo con plugin Patil coesistente. Schema manifest v2 minimale (4 campi nuovi: `schemaVersion`, `role`, `agentName`, `pid`). MVP target 4 giornate focus session (1 dev part-time + smoke test Alan ~45 min); 8 deliverable scope-chiuso; 6 regression test per i bug critici. Daemon Unix socket Go opt-in **gated** a v0.4 con due gate-condition esplicite (latenza polling >200ms misurata, peer count >3 reale). Marketplace ufficiale Anthropic submission deferred a v1.0 dopo 1-2 mesi di uso.

---

## 2. Validazione bug upstream

[CONFERMATO dal VAL v1, ricapitolato sinteticamente — non re-verificato.]

Repo: `PatilShreyas/claude-code-session-bridge` @ commit `8d0816b` (tag `v0.1.0` unico — il `v0.1.1` citato nei briefing non esiste come tag upstream, è label interna VAL).

| ID | Severità | Verdict | File:linee |
|---|---|---|---|
| BUG-1 Heartbeat dead in listen loop | 🔴 CRITICAL | CONFIRMED | `bridge-listen.sh:30-68` |
| BUG-2 `bridge-receive.sh` timeout secco | 🔴 CRITICAL | CONFIRMED | `bridge-receive.sh:15-43` |
| BUG-3 Multi-peer routing senza role | 🔴 CRITICAL | CONFIRMED | `register.sh:35-49` |
| BUG-4 `cleanup.sh` globale cross-project | 🔴 CRITICAL | CONFIRMED | `cleanup.sh:55-67` |
| BUG-5 `get-session-id.sh` parent fallback | 🔴 CRITICAL | CONFIRMED | `get-session-id.sh:25-36` |
| BUG-6 Session ID collision per cwd | 🟠 MEDIUM | CONFIRMED | `register.sh:10-24` |
| **BUG-7** `bridge-receive.sh` error su **stdout** invece di stderr | 🟡 HIGH | CONFIRMED | `bridge-receive.sh:41` |
| **BUG-8** STALE_SECONDS inconsistente: list-peers 300s vs cleanup 1800s | 🟡 HIGH | CONFIRMED | `list-peers.sh` + `cleanup.sh` |
| **BUG-9** `connect-peer.sh` non aggiorna heartbeat sender | 🟡 HIGH | CONFIRMED | `connect-peer.sh` |

### 2.1 Pattern strutturale (root cause sotto i 9 bug)

Nessuno script del loop operativo (listen, receive, connect) aggiorna heartbeat. L'intera architettura presuppone che heartbeat venga schedulato esternamente, ma **nessun comando `/bridge` lo schedula**. È un design gap fondamentale: la fix richiede un'astrazione di lifecycle (lifecycle manager) non patch puntuali.

In Go: heartbeat diventa un metodo del session manager invocato da goroutine dedicata con `time.Ticker`. Atomic update via `fdatasync` + `rename`. Risolto strutturalmente.

---

## 3. Decisioni architetturali

### 3.1 Strategia tecnologica → **C (Go rewrite per MVP) + E gated (Go daemon opzionale v0.4)**

Confronto 5 opzioni con scoring deep-searcher (peso: Sicurezza 20% / Portabilità 20% / Manutenibilità 3y 25% / IPC idiomatico 15% / Effort MVP 20%):

| Opzione | Score | Verdict |
|---|---|---|
| A: bash+jq drop-in | 4.8 | ❌ Tech debt strutturale, jq non-POSIX, no type safety, bash deprecated su macOS |
| B: Python rewrite | 6.2 | ❌ Runtime dependency, stdlib stabile ma Python 3.x minor break asyncio possibile |
| **C: Go rewrite** | **8.2** | ✅ **Scelto** — single static binary, Go 1 compat ≥10y, type safety, goroutines per polling |
| D: Rust rewrite | 6.4 | ❌ Setup rustup non installato, learning curve Alan non-Rust, async/Pin/Box ostile a freddo |
| E: Ibrido (bash thin + daemon) | 5.8 | 🟡 Doppio codebase = 2x test surface. Pattern valido come **evoluzione v0.4** non MVP |

**Motivazione scelta C (Go)**:
1. **Risolve strutturalmente i 9 bug**, non superficialmente. Pattern bash atomic-rewrite + status check + heartbeat goroutine sono nativi in Go, contorti in bash.
2. **Go 1.26.0 darwin/arm64 già installato** via Homebrew (verificato). Zero setup overhead.
3. **Cross-compile zero-friction**: `GOOS=linux CGO_ENABLED=0 go build` → static binary deployabile su macOS+Linux senza Python/jq runtime deps.
4. **Manutenibilità 3 anni**: Go compatibility promise (mai violata da 2012). Future Alan riapre codice e compila. Bash invece: macOS ha già spostato default a zsh, jq syntax non intuitiva a freddo.
5. **Plugin Claude Code marketplace**: schema 2026 supporta `bin/` directory che viene auto-added al `PATH`. Binary Go in `bin/cab-bridge` è il pattern integration idiomatico — il wrapper bash entry-point (20 LOC, opzionale) può anche essere eliminato usando direttamente il binary.

**Trade-off Go onesti (non nascosti)**:
- **Verbosità err handling**: `if err != nil {...}` ricorrente. Per ~600-900 LOC totali tollerabile.
- **Effort MVP +1 giorno vs bash**: 4 giorni vs 3 di bash patch. Onesto: scrivere Go test idiomatic + cross-compile setup CI costa tempo iniziale. Ammortizzato già al v0.3.
- **Socket path 108-char limit su macOS**: il path `~/.claude/cli-agents-bridge/sessions/<id>/bridge.sock` può eccedere. **Mitigation**: il socket (v0.4 daemon) andrà in `/tmp/cab-<short-id>.sock`, mentre i file dati restano in `~/.claude/...`.
- **Goroutine leak su crash**: il process lifecycle deve usare `context.Context` con cancellation per evitare leak. Pattern standard, ma da rispettare con disciplina.

**Daemon v0.4 in Go (stessa stack, gated)**: due gate-condition esplicite per attivare daemon:
- **G1**: latenza filesystem polling misurata >200ms in sessioni reali long-run
- **G2**: peer count concorrente >3 in scenario reale

Se G1 ∨ G2 non si verificano in 2 settimane post-MVP, daemon **non si fa**. Pattern CocoIndex (auto-start daemon detached al primo uso, fallback graceful a filesystem mode).

**Tradeoff scartati v2**: B (Python runtime dep), D (Rust learning curve+rustup overhead), E in MVP (scope creep 2-codebase). A (bash drop-in) era la scelta v1 — refused dopo rework per le ragioni di manutenibilità sopra.

### 3.2 Scope MVP v0.2.0 → **8 deliverable scope-chiuso (invariato dalla v1, implementazione Go)**

Gli stessi 8 deliverable di v1 §3.2, ma implementati in Go:

1. **Fix BUG-1** Heartbeat goroutine in `bridge listen` (Go `time.Ticker` + atomic manifest update)
2. **Fix BUG-2 + BUG-7** `bridge receive` long-poll fino a `--max-deadline` (default 30 min) + errore su stderr + exit code 124 timeout
3. **Fix BUG-3** Manifest schema v2 minimale: `schemaVersion`, `role`, `agentName`, `pid` (vedi §4.3 trimmed)
4. **Fix BUG-4** `bridge cleanup` default scope=my-session, --scope=global opt-in con confirm prompt
5. **Fix BUG-5** `bridge register` longest-prefix-match con tracking `BEST_MATCH_LEN`
6. **Fix BUG-6** Lock file PID safe (`O_EXCL` semantics + kill -0 check + stale recovery) + `--force-new` flag
7. **Enabling**: config loader centralizzato (`~/.claude/cli-agents-bridge/config.json` + env override) — risolve BUG-8 + elimina hardcoded
8. **Enabling**: `bridge send --file <path>` flag (FRIC-2) + JSON schema validation pre-write (via `encoding/json` strict mode)

**OUT of scope MVP** (deferred v0.3+): daemon Unix socket, SQLite WAL, notification osascript/notify-send, conversation thread view, transcript persistente, dashboard, retry built-in, encryption, multi-machine, TUI, listen background mode.

### 3.3 Naming → **`cli-agents-bridge`** (RISOLTO, confermato)

Decisione chiusa pre-rework. Non si rimette in discussione. Motivazione completa in `memory/decisioni_architetturali_aperte.md`.

### 3.4 Backward compatibility → **Namespace separato `~/.claude/cli-agents-bridge/` (Opzione B)** [FIX-3]

**Scelta hard**: namespace separato. Nessuna coesistenza filesystem con plugin Patil.

**Motivazione**:
- **Safety primaria**: `cleanup.sh` di Patil ha BUG-4 non fixato. Se condividessimo `~/.claude/session-bridge/sessions/`, una sessione `cli-agents-bridge` stale >30min verrebbe **silently distrutta** da qualunque `/bridge stop` di Patil running in altro VS Code. Inaccettabile.
- **Self-contained semantica**: il fork è un prodotto distinto, non una patch upstream. Namespace dedicato comunica chiaramente la separazione.
- **Migration esplicita**: `cab-bridge migrate-from-patil` (subcommand opt-in) copia manifest+inbox/outbox da `~/.claude/session-bridge/` a `~/.claude/cli-agents-bridge/` con schema v2 upgrade. Idempotente, dry-run support, backup pre-migration in `~/.claude/cli-agents-bridge/migration-backup-<date>/`.
- **Coesistenza slash command**: per evitare collision con Patil installato, durante dev cab usa `/cab`. Al release v0.2.0 stable, l'utente decide via install prompt: (a) keep both: cab=`/cab`, Patil=`/bridge`; (b) replace: cab=`/bridge`, Patil uninstalled by user. **Default safe = (a)**, no auto-uninstall third-party.

**Path canonical v0.2.0**:
- Sessions: `~/.claude/cli-agents-bridge/sessions/<id>/`
- Config: `~/.claude/cli-agents-bridge/config.json`
- Archive: `~/.claude/cli-agents-bridge/archive/<YYYY-MM-DD>/<id>/`
- Lock: `~/.claude/cli-agents-bridge/sessions/<id>/lock` (PID file)

**Tradeoff scartati**:
- **A: force uninstall Patil come prereq install**: ostile a utenti che vogliono A/B testing. Aumenta install friction.
- **Shared dir con flag opt-out**: complica codice + non risolve race (Patil non sa del nostro flag).

### 3.5 Distribuzione → **Self-marketplace GitHub via `/plugin marketplace add`** [FIX-4]

**Scelta**: distribuire come marketplace GitHub indipendente. NO custom path inesistente, NO standalone install script.

**Motivazione** (rivista dopo verifica empirica github-hunter):
- La hypothesis del v1 (`~/.claude/plugins/custom/` = bypass auto-classifier) era **FALSA**. Quel path non esiste come concetto nel sistema Claude Code 2026. I path reali sono: `~/.claude/plugins/cache/` (marketplace cache, **effimera**, sovrascritta agli update) + `--plugin-dir <path>` (dev mode, edit liberi).
- L'auto-classifier opera per **semantica di azione**, non per path-based deny. Non c'è meccanismo da bypassare.
- Pattern canonico 2026 per fork third-party: **self-marketplace**. Repo GitHub contiene `.claude-plugin/marketplace.json` che pubblica se stesso. Utenti aggiungono con `/plugin marketplace add tuo-org/cli-agents-bridge` + install con `/plugin install cli-agents-bridge@cli-agents-bridge`.

**Implementazione**:
- `.claude-plugin/plugin.json` (manifest, schema 2026 — vedi §4.6)
- `.claude-plugin/marketplace.json` (self-marketplace registration)
- `bin/cab-bridge` (Go binary, auto-added a PATH dal plugin system)
- `commands/*.md` (slash commands)
- `${CLAUDE_PLUGIN_DATA}` runtime env var per persistent state (resiste agli update, è la directory canonica per dati plugin)

**Dev workflow ESC durante MVP**:
```bash
claude --plugin-dir ~/develop/cli-agents-bridge  # edit liberi
/reload-plugins                                  # ricarica senza restart
```

**Marketplace Anthropic ufficiale**: deferred a v1.0 (dopo 1-2 mesi di uso reale, API stable).

**Tradeoff scartati**:
- **Standalone repo + curl-bash install.sh**: bypassa ecosystem plugin (no `/plugin update`, no version management, no `/reload-plugins`). Friction maintenance alta.
- **Submission marketplace ufficiale Anthropic in MVP**: maintenance commitment + breaking change probabili in v0.3 = early rejection.
- **Manual install copia in `~/.claude/plugins/cache/`**: la cache è gestita dal plugin system, copie manuali vengono sovrascritte agli update.

---

## 4. Architettura proposta

### 4.1 Diagramma high-level

```
┌──────────────────────────────────────────────────────────────┐
│  CLI agent #1 (VAL)                  CLI agent #2 (ESC)      │
│  ┌───────────────────┐                 ┌───────────────────┐ │
│  │ slash command     │                 │ slash command     │ │
│  │ /cab ask <id>     │                 │ /cab listen       │ │
│  └────────┬──────────┘                 └────────┬──────────┘ │
│           ▼                                     ▼            │
│  ┌───────────────────┐                 ┌───────────────────┐ │
│  │ cab-bridge (Go)   │                 │ cab-bridge (Go)   │ │
│  │  send / receive   │                 │  listen loop      │ │
│  │  with goroutine   │                 │  + heartbeat tick │ │
│  └────────┬──────────┘                 └────────┬──────────┘ │
└───────────┼─────────────────────────────────────┼────────────┘
            ▼                                     ▼
   ┌────────────────────────────────────────────────────┐
   │  ~/.claude/cli-agents-bridge/  (filesystem IPC)    │
   │  ├── config.json                                   │
   │  ├── sessions/<id>/                                │
   │  │   ├── manifest.json     (schema v2 minimale)    │
   │  │   ├── inbox/*.json      (atomic mktemp+rename)  │
   │  │   ├── outbox/*.json                             │
   │  │   └── lock              (PID, O_EXCL safe)      │
   │  ├── archive/<YYYY-MM-DD>/<id>/                    │
   │  └── migration-backup-<date>/  (one-time, opt-in)  │
   └────────────────────────────────────────────────────┘
```

Transport: filesystem polling JSON (MVP). Daemon Unix socket: assente in MVP, opzionale v0.4 gated.

### 4.2 Repo structure

```
cli-agents-bridge/
├── README.md                          # Quickstart + features + diff vs Patil
├── CHANGELOG.md                       # v0.2.0 → v1.0
├── LICENSE                            # MIT (compat upstream)
├── ARCHITECTURE.md                    # Design records (this PLAN.md merged post-release)
├── PRIVACY.md                         # GDPR data flow doc (NEW §9.GDPR-5)
├── SECURITY.md                        # Threat model + reporting (NEW §9)
├── go.mod / go.sum                    # Go module
├── Makefile                           # build/test/cross-compile/install
├── .claude-plugin/
│   ├── plugin.json                    # Manifest (schema 2026)
│   └── marketplace.json               # Self-marketplace registration
├── cmd/
│   └── cab-bridge/
│       └── main.go                    # Entry-point CLI multiplexer
├── internal/
│   ├── config/                        # Config loader + defaults
│   ├── session/                       # Manager: register/lookup/lock
│   ├── transport/                     # Filesystem polling + atomic write
│   ├── message/                       # Schema v2 marshal/validate
│   ├── routing/                       # Role-based routing rules
│   ├── security/                      # umask, perms, ownership check, regex validation
│   └── cleanup/                       # Scope-aware cleanup + archive
├── commands/                          # Slash command markdown
│   ├── cab.md                         # Multiplexer doc
│   ├── cab-listen.md
│   ├── cab-ask.md
│   ├── cab-peers.md
│   ├── cab-stop.md
│   └── cab-status.md                  # Lightweight status check
├── bin/                               # Build output (cross-compile artifacts)
│   └── cab-bridge                     # macOS arm64 default; linux/amd64+arm64 in CI
├── config/
│   └── default.json                   # Config template
├── schemas/
│   ├── manifest-v2.schema.json        # JSON Schema (reference + test fixture)
│   └── message-v2.schema.json
├── tests/
│   ├── unit/                          # *_test.go per ogni package internal/
│   ├── regression/                    # 6 test repro BUG-1..BUG-6 + 3 nuovi
│   ├── integration/                   # Multi-process via subprocess
│   └── smoke-test.md                  # Checklist manuale Alan
├── scripts/
│   ├── migrate-from-patil.sh          # Wrapper bash → invoca cab-bridge migrate
│   └── install-dev.sh                 # Helper symlink per --plugin-dir mode
└── docs/
    ├── migration-from-patil.md
    ├── multi-esc-patterns.md          # Role routing esempi
    ├── security-model.md              # Threat model dettagliato
    └── troubleshooting.md
```

### 4.3 Schema manifest v2 (trimmed YAGNI) [FIX-2]

```json
{
  "sessionId": "abc123",
  "schemaVersion": 2,
  "projectName": "cli-agents-bridge",
  "projectPath": "/Users/alan/develop/cli-agents-bridge",
  "agentName": "VAL-main",
  "role": "val",
  "pid": 12345,
  "startedAt": "2026-05-24T18:00:00Z",
  "lastHeartbeat": "2026-05-24T18:01:00Z",
  "status": "active",
  "capabilities": ["query", "context-dump", "conversation"]
}
```

**Campi nuovi v2 minimali**: `schemaVersion`, `agentName`, `role`, `pid`. Solo questi 4.

**Rimossi vs v1 (YAGNI esplicito)**:
- `teamId`: nessuna feature MVP lo usa. Riconfermare in v0.3 quando entrano feature team-routing (broadcast, fan-out).
- `supportedRoles`: ridondante con `role` (singolo valore).
- `allowedSenderRoles`: ACL non implementata in MVP (vedi §9 security baseline P2). `["*"]` default = dead code.

**Backward compat read**: schema v1 letto con defaults safe (`role="neutral"`, `agentName=projectName`, `pid=0`).

### 4.4 Schema message v2 (trimmed) [FIX-2]

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
  "metadata": {
    "fromProject": "cli-agents-bridge",
    "processingState": "pending"
  }
}
```

**Rimosso vs v1**: `threadId`. Nessuna view threading in MVP → dead field. Riconfermare in v0.3 quando entra `cab thread <id>` view.

### 4.5 Componenti core + responsabilità

| Package Go (internal/) | Responsabilità | Risolve |
|---|---|---|
| `config` | Load `config/default.json` + override `~/.claude/cli-agents-bridge/config.json` + env vars | BUG-8, hardcoded |
| `session/manager` | Register + longest-prefix lookup + heartbeat goroutine | BUG-1, BUG-5, BUG-9 |
| `session/lock` | PID lock O_EXCL + stale recovery (kill -0) + `--force-new` flag | BUG-6 |
| `transport/fs` | Atomic write (mktemp same-dir + fdatasync + rename), polling con `time.Ticker` | BUG-2, atomic write FIX-7 |
| `message/schema` | Marshal/Unmarshal v2, strict validation via `encoding/json` DisallowUnknownFields + regex constraints | FRIC-7, BUG-3 |
| `routing` | Role-based compat check pre-send (default: val↔esc, --allow-mesh per esc↔esc) | BUG-3 |
| `security/perms` | umask 077 syscall, chmod 700/600 enforce, ownership check, session ID regex validation | §9 SC-1..SC-5 |
| `cleanup` | Scope-aware: my-session default, --scope=global con confirm, pre-delete inbox archive | BUG-4 |
| `cmd/cab-bridge` | CLI multiplexer (register/listen/ask/receive/peers/cleanup/status/migrate) + commands routing | UX unificata |

### 4.6 Plugin manifest `.claude-plugin/plugin.json` (schema Claude Code 2026)

```json
{
  "name": "cli-agents-bridge",
  "displayName": "CLI Agents Bridge",
  "version": "0.2.0",
  "description": "Robust multi-peer IPC bridge between CLI agent sessions (Claude Code, etc.)",
  "author": {
    "name": "Alan",
    "email": "firstcontact@alancurtisagency.com",
    "url": "https://github.com/<TBD-org>/cli-agents-bridge"
  },
  "homepage": "https://github.com/<TBD-org>/cli-agents-bridge",
  "repository": "https://github.com/<TBD-org>/cli-agents-bridge",
  "license": "MIT",
  "keywords": ["ipc", "multi-session", "bridge", "val-esc", "multi-agent"],
  "commands": ["./commands/"],
  "dependencies": []
}
```

`bin/cab-bridge` viene auto-added al PATH dal plugin system (verificato github-hunter). `${CLAUDE_PLUGIN_DATA}` runtime env var per state persistente.

---

## 5. Roadmap milestone

### v0.2.0 MVP (target: **4 giornate ESC** focus session — onesto, +1 giorno vs v1 per Go vs bash)

Scope chiuso §3.2 (8 deliverable). Acceptance criteria = 6 regression test + 5 scenario test §7 + 5 metriche §10.

| Day | Deliverable | LOC stimati Go |
|---|---|---|
| 1 | Repo setup + go.mod + Makefile + cross-compile CI + `internal/config` + `internal/security/perms` + tests skeleton | ~150 |
| 1 | `internal/session/manager` (register, longest-prefix, heartbeat goroutine) + `internal/session/lock` (PID safe) → fix BUG-1, BUG-5, BUG-6 | ~180 |
| 2 | `internal/transport/fs` (atomic write fdatasync+rename, polling Ticker) → fix BUG-2, BUG-7 + atomic write FIX-7 | ~120 |
| 2 | `internal/message/schema` v2 trimmed + `internal/routing` role check → fix BUG-3 | ~100 |
| 3 | `internal/cleanup` scope-aware + archive → fix BUG-4 + connect.go heartbeat → BUG-9. Config unified → BUG-8 | ~100 |
| 3 | `cmd/cab-bridge` multiplexer + `commands/*.md` slash commands + `bridge send --file` flag | ~150 |
| 4 | Regression tests (6 bug) + integration tests (5 scenario) + smoke test manuale Alan (~45 min) + README + PRIVACY.md + SECURITY.md | (no prod LOC) |

**Total**: ~800 LOC Go (cmd + internal) + ~400 LOC test Go + 6 commands markdown + docs. Realistico per Go vs ~300 LOC bash di v1.

**Release criteria v0.2.0**:
- 6 regression test green su BUG-1..BUG-6 + 3 su BUG-7..9
- 5 scenario integration green
- 5 metriche successo §10 misurabili at smoke test
- Cross-compile macOS arm64 + linux amd64/arm64 in CI
- Plugin install via `/plugin marketplace add` funzionante (verificato in repo fresh)
- Smoke test manuale Alan cross-VS-Code passed
- README + PRIVACY.md + SECURITY.md presenti

### v0.3.0 (target: 1-2 settimane post-MVP validation)

Scope DOPO 1-2 settimane uso reale. Candidate (prioritization da rivalutare):
- Notification on receive (osascript/notify-send via syscall, ~30 LOC Go)
- `cab transcript` log persistente (JSONL append-only)
- `cab thread <id>` conversation view + `threadId` reintrodotto nel message schema
- `cab status` dashboard text
- `cab send --retry N --backoff exp` con idempotency content-hash
- Listen background mode (Go goroutine + signal handling)
- Auto-gc orphan sessions in register
- `teamId` reintrodotto nel manifest se broadcast/fan-out richiesto

### v0.4.0 — Daemon Unix socket Go opt-in (target: 1-2 settimane, **GATED**)

**Gate conditions** (ENTRAMBE da soddisfare per attivare):
- **G1**: latenza filesystem polling misurata >200ms in sessione reale long-run >1h
- **G2**: peer count concorrente >3 in scenario reale

Se G1 ∧ G2 NON si verificano in 2 settimane post-v0.3, daemon **non si fa**.

Se gate-pass: daemon Go con `net.Listen("unix", ...)` su `/tmp/cab-<short-id>.sock` (bypass 108-char macOS limit), auto-start detached al primo uso (pattern CocoIndex), filesystem mode resta funzionante in fallback.

### v1.0.0 target (3-6 mesi post-v0.2)

- Marketplace submission Anthropic ufficiale
- SQLite WAL message store (opt-in, retention queries)
- Encryption opt-in (libsodium o age-encrypt, motivare use case reale prima)
- Multi-machine via Tailscale ACL (motivare empirically prima)
- Observability metrics Prometheus-style
- Documentation completa + esempi triadici Architetto+VAL+ESC

---

## 6. Strategy backward compat → Migration esplicita da Patil v0.1.0 (no shared dir) [FIX-3]

### 6.1 Decisione hard: namespace separato

**Nessuna coesistenza filesystem.** `cli-agents-bridge` opera in `~/.claude/cli-agents-bridge/`. Plugin Patil opera in `~/.claude/session-bridge/`. Indipendenti.

**Motivazione**: cleanup.sh di Patil (BUG-4 upstream non fixato) distruggerebbe sessioni cli-agents-bridge stale >30min. Inaccettabile. Vedi §3.4.

### 6.2 Migration script `cab-bridge migrate-from-patil`

Subcommand del binary Go (no bash wrapper separato per ridurre surface).

```
cab-bridge migrate-from-patil [--dry-run] [--include-archive]
```

Comportamento:
1. Backup `~/.claude/session-bridge/` → `~/.claude/cli-agents-bridge/migration-backup-<YYYY-MM-DD-HHMMSS>/`
2. Scansiona `~/.claude/session-bridge/sessions/*/manifest.json`
3. Per ogni manifest v1: trasforma in v2 con defaults (`role="neutral"`, `agentName=projectName`, `pid=0`, `schemaVersion=2`)
4. Copia inbox/outbox preservando `*.json` files
5. Scrive in `~/.claude/cli-agents-bridge/sessions/<id>/`
6. Idempotente (re-run skippa sessioni già migrate via marker `.migrated`)
7. NON tocca source Patil (utente decide se rimuovere manualmente dopo verify)

### 6.3 Coesistenza plugin runtime

Lato runtime, i due plugin sono completamente isolati:
- File namespace separati
- Slash command separati: cab=`/cab`, Patil=`/bridge` (default install)
- Binary separati (cab-bridge in `bin/`, Patil usa bash scripts in `plugins/`)
- Cleanup scope: ogni plugin opera solo sul proprio namespace

Utente può installare both, transition graduale, eventualmente uninstall Patil manualmente. Zero auto-uninstall third-party.

---

## 7. Testing strategy

### 7.1 Unit (Go `testing` standard + `go test -race`)

Per ogni package `internal/*`: 3-5 test case (happy + error + edge). `testify/assert` per ergonomia. `go test -race` per detection race condition.

### 7.2 Regression — 9 test (uno per BUG)

| Test | Repro | Pass criteria |
|---|---|---|
| BUG-1 | Spawn `cab listen` 10 min idle | `lastHeartbeat` < 90s per tutta durata |
| BUG-2 | VAL send timeout=10s, ESC risponde a 30s | `cab receive --max-deadline=60` ritorna response (no perdita) |
| BUG-3 | 1 VAL + 2 ESC, ESC tenta send a peer role=esc | Send block con error "esc→esc forbidden (use --allow-mesh)" exit 2 |
| BUG-4 | Sessione X attiva (cab) + sessione Y stale 35min (cab altro project) | `cab cleanup` default lascia entrambi (scope my-session) |
| BUG-5 | `/p1/` + `/p1/sub/` registered, invoke da `/p1/sub/nested/` | `cab register` ritorna ID di `/p1/sub/` |
| BUG-6 | 2 istanze cab in stessa cwd, both fanno register | Seconda istanza fail con "session locked by PID X, use --force-new" exit 1 |
| BUG-7 | bridge-receive con timeout | Error message su stderr, exit 124 esplicito |
| BUG-8 | STALE_SECONDS modificato in config | list-peers + cleanup leggono stesso valore (no inconsistenza) |
| BUG-9 | connect-peer su sessione stale | Heartbeat aggiornato pre-connect |

### 7.3 Integration — 5 scenari multi-process

Spawn N processi `cab-bridge` (subprocess Go test) per simulare sessioni:
1. 1 VAL + 1 ESC round-trip 10 messaggi (baseline)
2. 1 VAL + 2 ESC role routing (BUG-3 enforcement)
3. Long-run 30min listen idle (heartbeat persistence)
4. Cleanup cross-project safety (3 progetti cab attivi simultaneamente)
5. Migration: source `~/.claude/session-bridge/` con 3 sessioni Patil v1 → run migrate-from-patil → verify in `~/.claude/cli-agents-bridge/`

**NB**: scenario v1 "backward compat 1 peer Patil + 1 peer cab" RIMOSSO. Con namespace separato (§3.4) non c'è interop runtime — solo migration one-shot.

### 7.4 Smoke test manuale finale (~45 min Alan)

Pre-release: 2 finestre VS Code reali, cab installato in entrambe via `/plugin marketplace add`, eseguire `tests/smoke-test.md` checklist:
- 5 step setup (install, register VAL, register ESC, list peers, connect)
- 5 step happy path (send brief, receive response, role check, cleanup own, status)
- 5 step edge case (timeout 35min listen, send con --file, cleanup --scope=global confirm, force-new collision, migrate from Patil dry-run)

Verifica empirica replica i 15 sub-sprint cross-project documentati dai VAL.

### 7.5 CI cross-compile + lint

GitHub Actions:
- `go vet`, `go test -race ./...`, `staticcheck`
- Cross-compile macOS arm64 + linux amd64/arm64
- Upload binaries come release artifacts su tag push

---

## 8. Risk register

| Risk | Probability | Impact | Mitigation |
|---|---|---|---|
| **R1**: Schema v2 trimmed troppo aggressivo → reintroduzione `teamId`/`threadId` rompe interop sessioni v0.2 in v0.3 | Bassa | Medio | Schema additivo: aggiungere campi in v0.3 NON rompe v0.2 reader (`encoding/json` ignora unknown fields se non DisallowUnknownFields strict mode — usato solo per validation gateway, non runtime read) |
| **R2**: 4 giorni MVP slip a 6+ giorni (scope creep + Go learning curve test idiomatic) | Media | Medio | Scope chiuso §3.2. Tier 2/3 deferred fisicamente in roadmap. Go test pattern documentato in `docs/dev-conventions.md` day 1 |
| **R3**: Plugin marketplace install path cambiato in Claude Code 2026.x patch release | Bassa | Alto | Manifest schema 2026 verificato (github-hunter). Self-marketplace pattern documentato ufficialmente. Monitor anthropics/claude-code release notes |
| **R4**: Socket path 108-char limit macOS colpisce v0.4 daemon | Media (futuro) | Medio | Mitigation pre-architetto: socket in `/tmp/cab-<short-id>.sock` (≤64 char garantiti), dati in `~/.claude/cli-agents-bridge/`. Documentato §3.1 |
| **R5**: Goroutine leak su crash listen | Media | Medio | `context.Context` con cancellation propagato in tutto session lifecycle. `defer cancel()` standard. `go test -race` in CI |
| **R6**: Migration from Patil corrompe inbox messages non letti | Bassa | Alto | Backup pre-migration obbligatorio (no skip flag). Dry-run default-on per smoke test. NON tocca source Patil |
| **R7**: 1 VAL + N ESC pattern troppo niche, ROI fork insufficiente | Bassa | Medio | Empirical evidence 15+ sub-sprint cross-project → ROI già validato pre-fork |
| **R8**: jq dependency rimossa → utenti che usavano script bash custom per parse manifest break | Bassa | Basso | Migration doc spiega: usa `cab-bridge inspect <session-id>` invece di `cat manifest | jq` |

---

## 9. Security baseline [NUOVA, FIX-5]

### 9.1 Threat model

**In-scope** (minacce reali single-user macOS/Linux):
- **TM-1** Malware locale stesso UID legge inbox/outbox (PII briefing, codice)
- **TM-2** Path traversal via session ID injection
- **TM-3** TOCTOU su lock/manifest in scenario multi-ESC
- **TM-4** Cleanup cross-session distruttivo (BUG-4 originale)
- **TM-5** Symlink attack su creation dir
- **TM-6** Cross-session impersonation (manifest spoofing)

**Out-of-scope MVP** (motivati esplicitamente):
- Attaccante remoto (local-only, zero network surface)
- Supply chain attack sul plugin (fuori scope baseline)
- Privilege escalation (single-user workflow, no root)
- Encryption end-to-end (single-user single-disk = theater vs FileVault disk-level)
- Multi-tenant shared machine (esplicitamente fuori scope design)

### 9.2 Security Controls — P0 obbligatori v0.2.0

**SC-1 umask 077 enforcement**
- Dove: in `main.go` `init()` chiamata `syscall.Umask(0o077)` PRIMA di qualsiasi file/dir creation
- Verifica: regression test crea file via cab-bridge, verifica `stat` ritorna 600

**SC-2 mkdir permessi 700**
- Dove: `internal/session/manager.go` register → `os.MkdirAll(sessionDir, 0o700)` + esplicit `os.Chmod(sessionDir, 0o700)` se dir preesistente
- Esteso a `~/.claude/cli-agents-bridge/`, `sessions/`, `archive/`, `inbox/`, `outbox/`

**SC-3 Ownership check pre-read/write file altrui**
- Dove: `internal/security/perms.go` helper `CheckOwnership(path string) error`
- Verifica `os.Stat(path).Sys().(*syscall.Stat_t).Uid == os.Getuid()`
- Edge case root (`Getuid()==0`): log warning "running as root, ownership check skipped", non abort
- Edge case NFS: documentato come known limitation, no fix

**SC-4 Session ID regex validation (path traversal prevention)**
- Dove: `internal/security/validate.go` `ValidateSessionID(id string) error`
- Regex strict: `^[a-z0-9]{6,32}$` (compatibile con random 6-char attuale + friendly naming futuro)
- Applicato a tutti i field che diventano path component: `sessionId`, `inReplyTo`, `from`, `to`
- Rischio aggiuntivo (RC-1 da security-sentinel): valori da JSON usati come path → validation obbligatoria

**SC-5 File permessi 600**
- Garantito da SC-1 (umask) + `os.WriteFile(path, data, 0o600)` esplicito
- Atomic write helper (`internal/transport/fs/atomic.go`): mktemp con `os.CreateTemp(dir, ".tmp.*")` (stesso filesystem garantito) → write → `f.Sync()` (fdatasync) → `os.Rename()` atomic

### 9.3 Security Controls — P1 importanti v0.2.0

**SC-6 Lock file PID safe (no symlink attack, no TOCTOU)**
- Pattern: `os.OpenFile(lockPath, O_CREATE|O_EXCL|O_WRONLY, 0o600)` — atomic creation, fail se esiste
- Stale recovery: read PID, `syscall.Kill(pid, 0)`, se errore `ESRCH` lock stale → rimuovi + retry una volta
- Cleanup: `defer os.Remove(lockPath)` + signal handler (SIGTERM/SIGINT)
- Mitigation symlink: SC-2 (perms 700 su parent dir) impedisce malware non-stesso-UID da creare symlink

**SC-7 Base dir integrity check at boot**
- All'avvio cab-bridge: `os.Lstat(baseDir)` → check non è symlink, perms 700, owner uguale a Getuid()
- Se symlink: error "security: base dir is symlink, possible attack, aborting" exit 1
- Se perms diversi: warn + auto-chmod 700

### 9.4 Security Controls — P2 deferred v0.3.0 (motivato esplicito)

**SC-8 PII detection regex su content**: theater per questo use case. Sender VAL sa cosa scrive. Threat reale (malware locale legge transcript) coperto da SC-1/SC-2 con perms 600. Implementato come **runtime warning** all'avvio listen ("messages stored plaintext, do not send secrets"), no regex scanning.

### 9.5 Rischi aggiuntivi VAL non menzionati (RC-* da security-sentinel)

- **RC-1**: JSON value usati come path → SC-4 applicato a tutti i field path-relevant
- **RC-2**: `mv tmp target` cross-filesystem non atomic → mitigazione FIX-7 (mktemp stesso dir di target, EXDEV check con warning esplicito)
- **RC-3**: bash scripts senza `set -euo pipefail` → in Go non rilevante (errors esplicit-by-design), in `scripts/migrate-from-patil.sh` (wrapper bash residuo) set obbligatorio

### 9.6 GDPR / EU compliance checklist (local-only data)

- **GDPR-1 Data minimization**: `BRIDGE_RETENTION_DAYS=7` default nel config. Subcommand `cab cleanup --retention=N` opzionale scheduled
- **GDPR-2 Right to erasure**: `cab purge --session <id>` rm -rf safeguarded (SC-4 validation prima)
- **GDPR-3 Data localization**: `PRIVACY.md` documenta esplicitamente "no data leaves the local machine"
- **GDPR-4 Logging minimale opt-in**: transcript persistente (v0.3 candidate) sarà opt-in default OFF
- **GDPR-5 Trattamento documentato**: `PRIVACY.md` + `SECURITY.md` (no DPA needed, basta trasparenza)

---

## 10. Metriche successo MVP [NUOVA, FIX-6]

**Definizione "v0.2.0 successful at 1 settimana post-release"** = soglie misurabili (non "funziona bene").

Periodo misurazione: **1 settimana di uso reale Alan post-release**, baseline = comportamento Patil pre-fork.

| ID | Metrica | Soglia successo | Misurazione |
|---|---|---|---|
| **M1** | Falsi positivi "stale" su sessione attiva | **0** (zero) in 7 giorni di listen long-run | Smoke check manuale ogni giorno: `cab peers` non mostra "stale" su ESC in listen attivo |
| **M2** | Incident cleanup cross-project distruttivi | **0** in 7 giorni | Verifica empirica: dopo ogni `cab cleanup` su un project, contare sessioni vive in altri project (deve essere invariato) |
| **M3** | Response perse per timeout secco | **0** su 50+ messaggi long-run (>60s response) | Log integrato `cab-bridge --debug` traccia send+receive id, mismatch detected |
| **M4** | ESC→ESC routing accidentale (Alan-reported original) | **0** su 5+ sessione 1-VAL-2-ESC | Routing role check blocca; smoke test scenario 2 mandatory |
| **M5** | Latenza round-trip ping-pong media | **<5s** (baseline Patil ~8s) | `cab status --benchmark` esegue 10 ping, riporta p50/p99 |
| **M6** | Slack di Alan in setup nuovi peer | **<60s** da `cab listen` a "ready" | Smoke test cronometra: register + listen + peer visible in altra VS Code |

**Failure criteria** (>=1 metrica below soglia per 2 settimane = rollback / hotfix priority):
- M1 o M2 violate = hotfix mandatory entro 48h (sono security/correctness regressions)
- M3 violate = root cause analysis + patch entro 1 settimana
- M4 violate = blocking, rollback v0.2 a v0.1 fork
- M5 o M6 violate = backlog v0.3 priority (no hotfix)

---

## 11. Open questions per VAL (residual)

Cose dove ESC ha preso decisione default ma necessitano conferma Alan/VAL prima di Sprint 0:

1. **Repo GitHub org**: account personale Alan (`advertalis`) o org dedicata (`<TBD-org>` placeholder nel manifest §4.6)? Suggerimento ESC: org dedicata se intent è community-distribution, personal se private MVP.
2. **Slash command default `/cab` o `/bridge`**: durante MVP development userò `/cab` (no collision con Patil). Al release v0.2.0, install prompt: keep both (`/cab`) o replace (`/bridge`)? Suggerimento ESC: keep both default, no auto-uninstall third-party.
3. **Migration backup retention**: `migration-backup-<date>/` resta indefinitamente o auto-purge dopo N giorni? Suggerimento ESC: manual purge (utente decide), no auto-delete data critici.
4. **Smoke test Alan-time**: confermo ~45 min disponibili per smoke test pre-release (giornata 4 MVP)?
5. **CI public**: GitHub Actions su org Alan → free tier sufficiente per Go cross-compile (verificato). Confermi setup workflow?
6. **Co-existence ac-agents/chatterence sessioni live durante dev**: dev userà namespace dedicato `~/.claude/cli-agents-bridge-dev/` via env `CAB_DATA_DIR` override per non toccare prod Patil di Alan?

Se nessuna risposta entro PHASE 2 start, ESC procede con suggerimenti default. Tutti reversibili.

---

## 12. Next step concreto — primo PHASE 2 brief

Quando VAL ratifica PLAN.md v2, primo brief PHASE 2 a ESC:

> **PHASE 2 — Sprint 0: Go module setup + security baseline**
>
> Task: baseline repo Go cli-agents-bridge v0.2.0-dev con security baseline P0 day-1.
>
> Step ordinati:
> 1. `cd /Users/alan/develop/cli-agents-bridge`
> 2. Repo structure §4.2 (folders + README placeholder + LICENSE MIT + SECURITY.md + PRIVACY.md stub)
> 3. `go mod init github.com/<TBD-org>/cli-agents-bridge` (placeholder org da confermare)
> 4. `cmd/cab-bridge/main.go` skeleton con `syscall.Umask(0o077)` in init (**SC-1 day-1**)
> 5. `internal/security/perms.go` con `CheckOwnership`, `ValidateSessionID` (regex `^[a-z0-9]{6,32}$`), `EnforceDirPerms` (**SC-3, SC-4 day-1**)
> 6. `internal/security/perms_test.go` con 3 test base (regex valid/invalid/path-traversal, ownership match/mismatch, umask propagation)
> 7. `internal/config/config.go` loader (`~/.claude/cli-agents-bridge/config.json` + env `CAB_*` override) + `config/default.json` template
> 8. `Makefile` con target: `build`, `test`, `test-race`, `cross-compile-all`, `install-dev`, `lint`
> 9. `.claude-plugin/plugin.json` + `.claude-plugin/marketplace.json` self-marketplace
> 10. `.github/workflows/ci.yml` (go vet + test -race + cross-compile macOS arm64 + linux amd64/arm64)
> 11. Commit unico: "Sprint 0: Go module baseline + security P0 (umask, perms, validate)"
>
> **Done criteria**:
> - `make test-race` passa
> - `make build` produce `bin/cab-bridge` macOS arm64
> - `make cross-compile-all` produce linux amd64+arm64 binaries
> - `bin/cab-bridge --version` ritorna `0.2.0-dev`
> - File creati dal binary hanno perms 600 (verificato manualmente)
>
> Stima effort: 3h (setup Go + security primitives + CI).

Successivi PHASE 2 sprint:
- **Sprint 1**: BUG-1 + BUG-5 + BUG-6 (session manager + heartbeat goroutine + PID lock safe)
- **Sprint 2**: BUG-2 + BUG-7 + atomic write helper (transport/fs + message schema v2)
- **Sprint 3**: BUG-3 + BUG-4 + BUG-8 + BUG-9 (routing role + cleanup scoped + config unified + connect heartbeat)
- **Sprint 4**: send --file + multiplexer cmd + slash commands + migration subcommand + smoke test + release v0.2.0

---

## Note finali ESECUTORE — Confronto v1 vs v2

**Cambiamenti chiave (FIX richiesti)**:
1. **FIX-1 Tech decision rifatta**: Go vince score 8.2 (vs bash 4.8, Python 6.2, Rust 6.4, Ibrido 5.8) per single binary + Go 1 compat + IPC idiomatic. Effort +1 giorno onesto.
2. **FIX-2 Schema trim**: rimossi `teamId`, `supportedRoles`, `allowedSenderRoles`, `threadId`. Mantenuti solo `schemaVersion`, `role`, `agentName`, `pid`. Schema additive resta upgrade-safe.
3. **FIX-3 Co-existenza Patil risolta hard**: namespace separato `~/.claude/cli-agents-bridge/`. Zero shared dir = zero cleanup cross-distruttivo.
4. **FIX-4 Auto-classifier hypothesis FALSA**: path `custom/` non esiste. Distribuzione canonical 2026 = self-marketplace GitHub. Plugin install via `/plugin marketplace add` + cache management automatic.
5. **FIX-5 Security baseline esplicita**: 5 control P0 (umask, perms 700/600, ownership, regex validation), 2 P1 (lock safe, base integrity), 1 P2 deferred (PII detection theater). 5 GDPR voci.
6. **FIX-6 Metriche successo concrete**: 6 metriche numeriche con soglie misurabili + failure criteria.
7. **FIX-7 Atomic write semantics**: mktemp stesso dir target (same-filesystem) + `fdatasync` pre-rename + EXDEV explicit warn. Documentato per macOS APFS + Linux ext4.

**Sorprese tech significative**:
- Hypothesis auto-classifier era pura immaginazione (mea culpa v1). Sistema reale è meglio: marketplace mature, `${CLAUDE_PLUGIN_DATA}` persistent, `bin/` PATH-injection automatic, `monitors` experimental ideale per IPC.
- Go scoring 8.2 vs bash 4.8 più ampio del previsto. La manutenibilità 3 anni di bash è realmente bassa (macOS deprecated default, jq syntax obscura).

**Brutally honest residual concerns**:
- **4 giorni MVP** è realistic-optimistic per dev part-time supervised. Se Alan vuole 2-3 giorni hard cap, devo scendere a bash patch (Strategia A) — Go costa il giorno extra in cambio di manutenibilità 3 anni superiore.
- **Daemon v0.4 gate-conditions** sono molto strette per design (intenzionalmente: anti scope-creep). Se Alan vuole daemon più aggressivamente, va spostato a v0.3 con motivazione esplicita.
- **Marketplace ufficiale Anthropic** richiede maintenance commitment continuo — deferred a v1.0 è corretto, ma Alan potrebbe avere strategia diversa (vendor lock-in vs community).

**Segnalazione docs** (ROADMAP/CLAUDE.md/memory): non toccati come da regola. Suggerisco al VAL post-release v0.2.0:
- Creare `CLAUDE.md` repo-locale con regole Go style + test pattern + commit conventions (no emoji, "Sprint N" prefix)
- Aggiornare memory globale `feedback_bridge_cleanup_global_side_effect.md` con riferimento namespace separato risolutivo
- Aggiornare memory `decisioni_architetturali_aperte.md` chiudendo 3.1 (Go) + 3.4 (namespace) + 3.5 (self-marketplace)

**git-ai**: contesto di questo PLAN preservato nei prossimi PHASE 2 commit.

---

**Status**: PLAN v2 ready for VAL re-valutation. NON committato (come da brief). Segnalo readiness in chat.

— ESECUTORE
