# Bridge plugin fork — improvement report

**Autore**: VALUTATORE (Claude Code, sessione ac-agents weekend 2026-05-23/24)
**Data**: 2026-05-24 sera (post Sett. 5 closure 9/10 sub-sprint done)
**Trigger**: Alan request — "vorrei forkare questo plugin di bridge, abbiamo già visto i suoi limiti per long runs, sessioni parallele in più VS codes, e ho anche provato la modalità 1 VAL e 2 ESC, è andata ma poi a un certo punto si sono incasinati e un ESC ha messaggiato l'altro credendo fosse il VAL... possiamo fare decisamente di meglio!!"
**Status**: report VAL-side (NO commit autonomo, Alan decide se integrare)
**Target audience**: dev che farà fork in nuovo progetto VS Code

**Plugin upstream**: `PatilShreyas/claude-code-session-bridge` v0.1.1 (MIT, 2026-03)

---

## TL;DR

Bridge Patil session-bridge funziona, ma ha **15 friction empirically osservate** in 2 giorni di uso intensivo (15+ sub-sprint via bridge cross-project ac-agents + chatterence-bi-template). Fork prioritario su 5 improvements Tier 1 ad alto valore (heartbeat live, long-poll receive, role disambiguation, status API, bridge-aware CLI). Tier 2 (4 improvements) + Tier 3 (3 improvements) addressable post-MVP fork. Tutte le modifiche sono backward-compatible col protocollo file JSON corrente. Stima fork iniziale: ~2-3 giorni focus session.

---

## Context

### Cosa è bridge

Plugin Claude Code che permette a 2+ istanze di Claude Code (sessioni VS Code separate) di comunicare via file JSON in `~/.claude/session-bridge/sessions/<id>/{inbox,outbox}/`. Pattern:
- Una sessione fa `/bridge listen` → polling inbox ogni 3s
- Altra sessione fa `/bridge ask <question>` → scrive in inbox target, attende response
- Listener (Claude Code istanza) processa msg + risponde dal **contesto live** della sessione (no API extra, no approximation)

### Come l'ho usato

Pattern **VAL ↔ ESC** (workflow triadico Alan + Architetto + VALUTATORE + ESECUTORE):
- VAL (planner/orchestrator) in una finestra VS Code, dispatcher di brief
- ESC (executor context-fresh) in altra finestra, riceve brief + esegue + risponde
- VAL ed ESC dialogano via bridge senza copia/incolla manuale Alan

### Empirical evidence

**ac-agents Sett. 5 weekend 2026-05-23/24**: 9 sub-sprint via bridge + 4 ADR + 12 commit pushati origin/main. Pattern bridge-friendly per task multi-file refactor + cross-file recon + scope chiaro. Pattern bridge-unfriendly per ops/deploy + smoke E2E + design pure (ADR).

**Cross-project chatterence-bi-template**: 7 sub-sprint precedenti (Alan reference).

**Modalità avanzata testata da Alan**: 1 VAL + 2 ESC (multi-peer). Funziona inizialmente, poi "si sono incasinati e un ESC ha messaggiato l'altro credendo fosse il VAL" — **bug fondamentale**: bridge non disambigua peer identity in routing.

---

## 15 Friction osservate (catalog con severity)

### 🔴 HIGH severity (blocker UX, frequent)

#### F1 — Heartbeat non aggiornato durante `/bridge listen` polling
- **Behavior corrente**: `manifest.json lastHeartbeat` viene scritto SOLO a `register.sh` + `connect-peer.sh`. Durante listen polling loop (3s) lo script NON aggiorna heartbeat
- **Sintomo**: dopo 5 min in listen, `list-peers.sh` mostra status "stale" anche se peer alive e responsive
- **Pattern G32 conseguente**: "bridge stale ≠ ESC dead" — VAL dovrebbe imparare a non fidarsi del heartbeat, ma è confusing UX
- **Evidence**: 4 falsi allarmi in 2 giorni ("ESC morto, serve nuovo session" → ESC era alive, commit arrivato post)
- **Fix proposto**: `bridge-listen.sh` aggiorna `manifest.json.lastHeartbeat` ogni N secondi (default 60s) durante polling cycle. Atomic write via temp file + mv (pattern già usato altrove). ~10 LOC modifica.

#### F2 — `bridge-receive.sh` timeout secco senza retry
- **Behavior corrente**: `bridge-receive.sh <session> <msg-id> <timeout>` polling 3s su inbox cercando `inReplyTo == msg-id`. Se timeout scade → "No response received after Ns. The peer may be inactive." + exit 1
- **Sintomo**: VAL setta timeout 600s per long task, ESC impiega 720s, **response persa** (arriva post-timeout, file JSON sta nell'inbox ma bridge-receive ha già exit-ato). VAL deve fare empirical check git status per recovery
- **Evidence**: timeout false positive Sub-sprint 5 PHASE 2 (900s → ESC commit dopo), Sub-sprint 8 PHASE 2 (720s → ESC commit dopo), Sub-sprint 3 PHASE 2 (720s → commit pre-timeout), Sub-sprint 10 PHASE 2 (720s → BLOCK ESC corretto)
- **Fix proposto**: 
  - Mode A: long-poll **fino a result** (configurable max-deadline, default 30 min)
  - Mode B: persistent listen → response written + notify event (file-based + optional process signal SIGUSR1)
  - Mode C: **status check API separato** — `bridge-status.sh <my-session> <msg-id>` ritorna `{status: "pending"|"processing"|"completed"|"failed", elapsed: Ns, peer_alive: bool}`. VAL può polling status invece di bloccarsi su receive.
  - Mode C **raccomandato** — più flessibile + foundation per future async patterns.

#### F3 — Multi-peer routing senza identity disambiguation
- **Behavior corrente**: `send-message.sh <target-session-id>` scrive in `inbox/<target-session-id>/`. Listener vede `from: <sender>` ma NON valida ruolo. Multi-peer (1 VAL + 2 ESC) non hanno identità semantica nel protocollo
- **Sintomo Alan-reported**: "1 VAL + 2 ESC è andata ma poi a un certo punto si sono incasinati e un ESC ha messaggiato l'altro credendo fosse il VAL"
- **Root cause hypothesis**: ESC istanze hanno session ID + project name nel manifest, ma NO role tag (VAL/ESC/ARCHITETTO). Quando ESC1 deve rispondere a VAL, parse `from` field e cerca peer by project name → match ambiguo se project name "src" su entrambe ESC istanze
- **Fix proposto**:
  - **Manifest schema extension**: aggiungi `role` field enum `["val", "esc", "architect", "neutral"]` + `agent_name` string libero (es. "VAL-1", "ESC-frontend", "ESC-backend")
  - `register.sh` accetta `--role <role>` + `--agent-name <name>` flags (env override `BRIDGE_ROLE`, `BRIDGE_AGENT_NAME`)
  - `send-message.sh` valida target peer role compatible (es. VAL invia a ESC role, ESC risponde a VAL role; ESC NON può inviare a peer role==ESC direct, deve passare via VAL hub-pattern OR explicit override flag)
  - `list-peers.sh` mostra role + agent-name colonne
  - **CRITICAL**: pattern hub VAL-centric (default safe) vs mesh peer-to-peer (advanced, opt-in via flag)

#### F4 — Session ID collision per project dir
- **Behavior corrente**: `register.sh` riusa `bridge-session` file in `$PROJECT_DIR/.claude/bridge-session` se exists. Conseguenza: 2 istanze Claude Code in stesso project dir = stesso bridge session ID = NON si vedono come peer separati
- **Sintomo**: setup iniziale ac-agents 2026-05-23 mattina — collision risolta solo facendo ESC partire da subdir diversa (`docs/` poi `src/`). 25 min persi in debug.
- **Fix proposto**: 
  - `register.sh` accetta `BRIDGE_SESSION_ID_OVERRIDE` env var → forza nuovo session ID se set (ignora bridge-session file esistente)
  - O meglio: aggiungi parametro discriminator implicit (PID + timestamp) al file naming → `.claude/bridge-session.<discriminator>` per supportare N istanze stesso project
  - Auto-detect: se 2 istanze attive lo stesso project, log warning + permettere convivenza con session ID separati
- **Bonus**: `register.sh --force-new` flag per esplicito skip riuso

#### F5 — `listen` mode tool channel completamente bloccato
- **Behavior corrente**: durante `/bridge listen`, ESC dedicata 100% a polling + processing peer queries. Non può fare lavoro proprio (codice, file edit, ecc.) finché user non Ctrl+C
- **Sintomo**: ESC è "server-only" mentre listen, ma in realtà ESC ha capability di lavoro proprio (executor context-fresh). Costringe a Ctrl+C + reload se Alan vuole interagire direttamente con ESC fuori da bridge
- **Fix proposto**:
  - Hybrid mode: `/bridge listen --background` → polling fork in background process, ESC tool channel libero
  - Process control: file lock + signal handling per safe interrupt
  - **Trade-off**: complessità implementation vs UX. Mode A (current) resta default, Mode B opt-in.

### 🟡 MEDIUM severity (UX friction, occasional)

#### F6 — Inbox cleanup manuale (status pending → read)
- **Behavior corrente**: messaggi in inbox restano con `status: "pending"` finché qualcuno (umano o script) li riscrive a `read`. Nessun garbage collection automatico
- **Sintomo**: dopo 2 giorni inbox VAL aveva 6 messaggi non-read di vari stati, alcuni duplicati (Sub-sprint 7 response inviata 2x ESC, ricevuta 1x via bridge-receive, secondo persisteva pending)
- **Fix proposto**:
  - `bridge-receive.sh` auto-mark `status: "read"` quando ritorna response al chiamante
  - `bridge-listen.sh` auto-mark `status: "read"` post-processing msg
  - Cron-like cleanup: `bridge-gc.sh` archive read messages >24h → `archive/` subdir
  - View command: `bridge inbox` → tabella status counts

#### F7 — No conversation thread view
- **Behavior corrente**: messaggi hanno `inReplyTo` field MA non c'è UI per visualizzare thread X = lista messaggi correlati
- **Sintomo**: durante debug bridge VAL ho dovuto fare jq custom per filtrare inbox + chain inReplyTo manualmente
- **Fix proposto**: `bridge thread <root-msg-id>` → mostra tree messaggi (root + tutti `inReplyTo == root` + ricorsivo)

#### F8 — No retry built-in
- **Behavior corrente**: send-message scrive file, attendere response via receive timeout. Se timeout o crash, retry manuale
- **Sintomo**: Sub-sprint 3 PHASE 2 timeout 720s, response persa, recovery manuale via empirical git check
- **Fix proposto**: `bridge send --retry N --backoff exponential` → re-send msg con nuovo MSG_ID se response non arriva entro deadline (idempotency su content hash per evitare duplicati ESC-side processing)

#### F9 — Naming manifest `projectName = basename(pwd)` confuso
- **Behavior corrente**: `register.sh` setta `projectName = $(basename $PROJECT_DIR)`. Se cwd="/Users/alan/develop/ac-agents/src" → projectName="src". Confusing in multi-peer ("src" both ESC istanze)
- **Sintomo**: `list-peers.sh` mostra `PROJECT: src` su 2 ESC, indistinguibili
- **Fix proposto**: derivato da F3 — `--agent-name` field libero per disambiguazione user-friendly. projectName resta basename ma agent-name è display primary.

#### F10 — Pre-existing ESC session non auto-cleanup
- **Behavior corrente**: se ESC crash/finestra chiusa senza `/bridge stop`, manifest persiste finché manual cleanup. `list-peers.sh` mostra stale ma non rimuove
- **Sintomo**: 2 orphan sessions chatterence-bi-template visibili in mia VAL session ac-agents
- **Fix proposto**: 
  - `bridge gc` comando esplicito (manual)
  - Auto-gc su `register.sh` se lastHeartbeat >24h → rimuovi (con safety prompt)
  - O timer cron systemd timer (se installato)

### 🟢 LOW severity (nice-to-have, defer)

#### F11 — No encryption (messaggi plain JSON filesystem)
- **Behavior corrente**: messaggi salvati come JSON in `~/.claude/session-bridge/sessions/<id>/inbox/*.json`. Protezione = Unix file permissions (600)
- **Sintomo**: README upstream avvisa esplicitamente "no encryption, don't send secrets"
- **Fix proposto** (low priority single-user single-machine): age-encrypt content body con session-local keypair generato a register. Overhead minimo per msg ma adds complessità. Deferrible.

#### F12 — Single-machine only (no remote)
- **Behavior corrente**: bridge usa filesystem locale, no network protocol
- **Sintomo**: workflow remote/distributed impossibile (es. Alan dev su Mac + ESC su VPS)
- **Fix proposto**: complesso. Opzioni:
  - Tailscale + filesystem-over-tailscale mount
  - HTTP/gRPC server con bridge protocol over network
  - **Defer**: single-machine è 99% use case attuale

#### F13 — No protocol versioning
- **Behavior corrente**: schema JSON messaggi fisso (id, from, to, type, status, content, inReplyTo). Cambi schema = breaking
- **Sintomo**: aggiungere `role` (F3) o altri field richiede coordinare upgrade tutte sessioni
- **Fix proposto**: aggiungi `schemaVersion: 1` field a manifest + messaggi. `bridge-listen.sh` valida version compat + log warning su mismatch.

#### F14 — Bus factor 1 upstream
- **Behavior corrente**: Patil session-bridge maintenance = 1 maintainer
- **Sintomo**: rischio abandonment + fork necessario se broken
- **Mitigation** (questo report): fork preventivo. Tier 3 ulteriore: PR upstream improvements rilevanti se Patil ricettivo, mantieni fork sync.

#### F15 — No CLI/TUI dashboard live state
- **Behavior corrente**: stato bridge visualizzabile solo via `list-peers.sh` snapshot + manual jq inbox/outbox
- **Sintomo**: durante debug bridge VAL ho fatto tante chiamate `cat manifest.json + ls inbox/` per snooping
- **Fix proposto**: `bridge dash` comando interactive TUI (es. blessed/textual-like) per live monitoring peers + inbox + outbox + heartbeat. Nice-to-have, defer.

---

## Improvements prioritizzati (3 tier)

### Tier 1 (MVP fork, ~2-3 giorni focus session)

Priorità massima, valore alto, complessità bassa-media. Sblocca workflow VAL↔ESC senza friction:

1. **F1 Heartbeat live polling** — `bridge-listen.sh` modifica ~10 LOC. Atomic write manifest ogni 60s. **Beneficio**: rimuove falsi positivi stale, semplifica VAL diagnosis ESC alive.

2. **F2 Status check API + long-poll receive** — nuovo script `bridge-status.sh` (~50 LOC). Mode C raccomandato. **Beneficio**: VAL può polling status invece bloccare receive, foundation async future.

3. **F3 Multi-peer role disambiguation** — manifest schema extension + register flags + send validation (~80 LOC). Hub VAL-centric default + opt-in mesh. **Beneficio**: risolve Alan-reported "ESC ha messaggiato altro ESC", abilita 1-VAL-N-ESC pattern robusto.

4. **F4 Session ID collision prevention** — register.sh flag `--force-new` + env override (~15 LOC). **Beneficio**: setup multi-istanza stesso project senza debug 25-min loss.

5. **F6 Inbox auto-cleanup + view command** — bridge-receive/listen auto-mark read + `bridge inbox` view (~30 LOC). **Beneficio**: cleanup manuale eliminato.

**Stima totale Tier 1**: ~180 LOC modificate/aggiunte, ~2-3 giorni focus session (1 dev). Tests scenarios sotto.

### Tier 2 (post-MVP, ~1-2 settimane)

Quality of life + advanced patterns:

6. **F5 Listen background mode** — process fork + signal handling. Complessità media (process management bash). Beneficio: ESC tool channel libero durante listen.

7. **F7 Conversation thread view** — `bridge thread <id>` CLI (~25 LOC + jq). Beneficio: debug + audit trail.

8. **F8 Send retry built-in** — `--retry N --backoff` flag (~40 LOC + idempotency check). Beneficio: robustezza network/timeout failures.

9. **F10 Auto-gc orphan sessions** — register.sh check + cleanup (~20 LOC). Beneficio: housekeeping automatic.

### Tier 3 (defer indefinitely or major version)

Architectural changes:

10. **F11 Encryption** — age-encrypted body. Single-user single-machine = low priority.
11. **F12 Multi-machine remote** — major architectural change. Defer until use case empirical.
12. **F13 Protocol versioning** — additive, può essere fatto incrementally con Tier 1.
13. **F14 PR upstream** — community contribution, not code change.
14. **F15 TUI dashboard** — nice-to-have, complessità medio-alta.

---

## Architecture changes proposed (Tier 1 details)

### Manifest schema v2 (backward-compatible additive)

```json
{
  "sessionId": "abc123",
  "schemaVersion": 2,
  "projectName": "src",
  "projectPath": "/Users/alan/develop/ac-agents/src",
  "agentName": "ESC-frontend",
  "role": "esc",
  "startedAt": "2026-05-24T...",
  "lastHeartbeat": "2026-05-24T...",
  "status": "active",
  "capabilities": ["query", "context-dump", "conversation"],
  "supportedRoles": ["esc"],
  "allowedSenderRoles": ["val", "architect"]
}
```

Field nuovi:
- `schemaVersion: 2` (F13)
- `agentName: string` (F9 + F3 display)
- `role: enum ["val", "esc", "architect", "neutral"]` (F3)
- `supportedRoles: array<string>` (mio role auto-derivato, redundant ma esplicito)
- `allowedSenderRoles: array<string>` (whitelist chi può inviare a me, default permissive `["*"]` per backward-compat)

### Message schema v2

```json
{
  "id": "msg-...",
  "schemaVersion": 2,
  "from": "abc123",
  "fromRole": "val",
  "fromAgentName": "VAL-main",
  "to": "def456",
  "toRole": "esc",
  "type": "query",
  "timestamp": "...",
  "status": "pending",
  "content": "...",
  "inReplyTo": null,
  "metadata": {
    "urgency": "normal",
    "fromProject": "src",
    "processingState": "pending|processing|completed|failed",
    "processingStartedAt": null,
    "processingEndedAt": null
  }
}
```

Field nuovi:
- `schemaVersion: 2` (F13)
- `fromRole` + `toRole` + `fromAgentName` (F3 routing validation)
- `metadata.processingState` + timestamps (F2 status check support)

### Nuovi script Tier 1

```
scripts/
├── bridge-listen.sh          [MODIFY] add heartbeat loop (F1)
├── bridge-receive.sh         [MODIFY] auto-mark read (F6)
├── bridge-status.sh          [NEW]    status check API (F2)
├── bridge-inbox.sh           [NEW]    inbox view table (F6)
├── register.sh               [MODIFY] role + agent-name flags + force-new (F3, F4)
├── send-message.sh           [MODIFY] role validation pre-send (F3)
├── connect-peer.sh           [MODIFY] role compat check (F3)
└── list-peers.sh             [MODIFY] add role + agent-name columns (F3, F9)
```

### Backward compatibility

- Schema v1 messaggi/manifest letti senza errori, mancanti field = defaults (`role: "neutral"`, `allowedSenderRoles: ["*"]`)
- `bridge-listen.sh` log warning ma processa msg v1
- Fork può convivere con session Patil v1 stesso filesystem (no breaking)

---

## Test scenarios per validate fork

### Scenario 1: long-run heartbeat persistence (F1)
- ESC `/bridge listen` per 30 min idle
- VAL `list-peers.sh` ogni 5 min
- **Expected**: status = "active", lastHeartbeat aggiornato ogni ~60s (delta heartbeat < 90s sempre)
- **Failure mode current bridge**: status "stale" dopo 5 min

### Scenario 2: long-run ESC work post-receive timeout (F2)
- VAL `bridge ask` con timeout 60s
- ESC riceve, processa 300s (5 min lavoro reale)
- VAL receive timeout, exit 1
- VAL `bridge-status <my-session> <msg-id>` ogni 30s
- **Expected**: status = "processing" durante work ESC, "completed" quando arriva response, response leggibile post-completion via `bridge thread` o read inbox
- **Failure mode current bridge**: response persa, recovery solo via empirical git check

### Scenario 3: 1 VAL + 2 ESC role disambiguation (F3)
- VAL session `val-1` role=val
- ESC session `esc-frontend` role=esc
- ESC session `esc-backend` role=esc
- VAL invia query a esc-frontend
- esc-frontend tenta inviare msg direct a esc-backend (mesh)
- **Expected default hub**: send-message BLOCK con error "esc → esc forbidden, use VAL hub"
- **Expected opt-in mesh**: send-message OK con flag `--allow-mesh`
- **Failure mode current bridge**: esc-frontend invia a esc-backend pensando sia val-1, esc-backend processa come fosse VAL → loop confusion

### Scenario 4: 2 istanze stesso project dir (F4)
- VAL `cd /path && claude` → register, sessionId `aaa`
- ESC `cd /path && claude` (stesso path) → register, **expected**: nuovo sessionId `bbb` (NO riuso `aaa`) con warning logged "another session active in same project dir"
- `list-peers.sh` mostra 2 peers distinti
- **Failure mode current bridge**: ESC riusa `aaa`, le 2 sessioni si "fondono" come stessa entità

### Scenario 5: inbox cleanup automatic (F6)
- VAL invia 10 msg a ESC
- ESC `/bridge listen` processa tutti
- VAL `bridge inbox` → table mostra 0 pending, 10 read (post-processing)
- VAL `bridge inbox --archive >7d` → archiviazione automatic
- **Failure mode current bridge**: 10 msg restano `pending` finché manual jq edit

---

## Anti-pattern noti da evitare nel fork

### AP-1: heartbeat polling synchronous bloccante
- **Problema**: heartbeat scritto INLINE nel polling loop sync rallenta inbox check
- **Fix**: heartbeat in background subshell con `& disown` o setInterval-like pattern (cron daemon nella sessione)

### AP-2: schema v2 mandatory senza migration
- **Problema**: forzare schema v2 rompe sessioni v1 esistenti
- **Fix**: additive only, defaults safe, schema_version field opzionale con default 1

### AP-3: role mesh peer-to-peer default
- **Problema**: ESC → ESC libero crea Alan-reported "incasinamento" routing
- **Fix**: hub VAL-centric DEFAULT, mesh OPT-IN esplicito

### AP-4: encryption opzionale dopo first release
- **Problema**: aggiungere encryption post-deploy = migration complessa
- **Fix**: se decidi NO encryption Tier 3, NON refactorare dopo. Se decidi SÌ, fai subito Tier 1.

### AP-5: TUI dashboard prematuro
- **Problema**: TUI complesso (blessed/textual deps) + 1 user, low ROI
- **Fix**: CLI commands `bridge inbox`, `bridge thread`, `bridge status` sufficient per debug. TUI defer Tier 3.

### AP-6: protocol changes senza version negotiation
- **Problema**: cambi schema = breaking, debug nightmare
- **Fix**: schemaVersion field PRESENTE da v2, listen valida version + log warning su msg v1 obsoleti

### AP-7: scope creep oltre 5 priorità Tier 1
- **Problema**: fork tentazione "while we're here" feature add → schedule slip
- **Fix**: discipline Tier 1 closed, Tier 2 separate sprint, Tier 3 defer indefinitely

---

## Pattern G observed (catalog session ac-agents Sett. 5 weekend)

Pattern di workflow emersi empirically usando bridge intensivamente. Possono guidare design fork:

- **G16 hybrid VAL judgment autonomy** + Architetto direct + ESC bridge fit → fork può expose flags per scope routing (es. `--mode atomic|phase-split`)
- **G19 trust gradient + scope discipline** → fork rispetta boundary role (F3 implementation)
- **G24+G27 reverse** "ESC catch VAL brief gaps" → fork può loggare brief gaps detect via response pattern analysis (advanced, Tier 3)
- **G31 bus factor mitigation** → fork stesso è risposta a G31
- **G32 timeout ≠ dead** → fork F1+F2 risolvono empirical
- **G37 boundary VAL/ESC** → fork F3 enforcement role-based
- **G42 candidato ESC empirical catch via implementation iteration** → fork può loggare iteration count (pre-commit hook fix tentativi)
- **G43 candidato Architetto direct write design pure** → fork può flag `--design-only` per skip bridge ESC su task design (e.g. ADR write)
- **G44 candidato strategic stopping point** → fork può integrare "session summary" command (es. `bridge session-summary` produce report metriche giornaliere)

---

## Riferimenti empirical (commits + sessions ac-agents)

Use case validation reale durante weekend 2026-05-23/24:

### Sub-sprint via bridge (12 totali Sett. 5)

| Sub-sprint | Commit | Bridge fit | Timeout? | ESC catch |
|---|---|---|---|---|
| 1.2 OAuth | `d2201e1`, `9206ebc` | PHASE 1/2 | NO | scope insufficient + HUNTER_CALENDAR_ID suffix |
| 2 DB schema | `6ad2153` | atomic | NO | monolithic vs modular |
| 3 power-up | `54ccece` | PHASE 1/2 | NO | G27 lesson integrated (suffix validation) |
| 4 callback | `6336865` | PHASE 1/2 | YES 600s | reject delete vs update |
| 5 runtime | `14b0a3a` | PHASE 1/2 | YES 900s | architettura subprocess executor power-up |
| 7 systemd | `9559351` | atomic | NO | naming + runbook number conflict |
| 8 ticketing | `e4cc3b9` | PHASE 1/2 | YES 720s | "proactive_to_hunter" enum + writeJournalEntry rootDir + ThreadSchema/ThreadRow + Ranger CLAUDE.md cap deferred |
| 10 closure | `f538af9`, `9006544` | atomic split 2 commit | YES 480s + BLOCK | file VAL-only override |

### Bridge timeout false positives (G32 evidence)

4 occurrences in 2 giorni. Pattern consistente:
1. VAL `bridge-receive` timeout
2. VAL controlla `git log` empirically
3. ESC ha committato post-timeout
4. Response bridge persa, recovery manuale

### Multi-peer chaos (Alan-reported)

Setup 1-VAL + 2-ESC funzionante inizialmente, "poi un ESC ha messaggiato l'altro credendo fosse il VAL". Root cause hypothesis F3 (role disambiguation assente).

---

## Stima fork roadmap

### MVP fork (Tier 1, 5 improvements)
- **Effort**: ~2-3 giorni focus session 1 dev
- **LOC**: ~180 modificate/aggiunte (additive)
- **Backward compat**: SÌ (schema v1 readable, default safe)
- **Testing**: 5 scenari documentati sopra
- **Release target**: fork v0.2.0 (post-Sett. 5 ac-agents closure)

### v0.3.0 (Tier 2)
- Effort: ~1-2 settimane
- F5 listen background + F7 thread view + F8 retry + F10 auto-gc
- Quality of life

### v1.0+ (Tier 3)
- Encryption (F11), multi-machine (F12), TUI (F15)
- Defer until empirical evidence demand

---

## Action items per Alan

1. **Verify report content** + clarify se mancano improvements/friction osservati tu cross-project
2. **Decidi**: PR upstream Patil prima (offerta) o fork direct (faster)
3. **Setup nuovo progetto VS Code** per fork lavoro (suggerisco `~/develop/claude-bridge-fork`)
4. **Brief ESC** (nuovo agente in nuovo progetto) con questo report come reference
5. **Coordina con Architetto** se vuole codify pattern G44 + altri post-fork validation empirical

---

## Onestà finale VAL

### Limiti questo report

- **Self-report metric bias** possibile (G29) — stime LOC + giorni focus sono empirical-grounded ma non misurate test concrete
- **Pattern G observation** = mia interpretazione ac-agents weekend, potrebbe NON generalizzare
- **Multi-peer 1-VAL + N-ESC**: io ho usato solo 1-VAL + 1-ESC, F3 fix proposto basato su Alan empirical evidence + my hypothesis hub-pattern
- **Architecture changes**: schema v2 proposto, ma non ho prototipato + testato

### Quello che funziona già bene di Patil bridge

NON è un report distruttivo. Bridge Patil ha **value reale**:
- Pattern "agent risponde dal contesto live" è geniale (zero API extra, no approximation)
- File JSON transport KISS + auditable
- Slash command UX semplice
- 3 sec polling cycle OK per task >1 min

Fork = **evolution** non revolution. Mantenere file JSON transport + slash command UX, aggiungere robustness + multi-peer support.

### Stima realistic ROI fork

- **Effort**: ~2-3 giorni MVP
- **Beneficio**: rimuove 5 friction blocker observabili in workflow VAL↔ESC daily
- **Ammortizza**: dopo ~10-15 sessioni multi-sprint
- **Alternative**: stay on Patil + patches ad-hoc (long-term debito)

Raccomandazione: **fork MVP Tier 1, defer Tier 2/3, monitor Patil upstream per merge opportunity**.

---

**End of report.**

Buona notte Alan. Sett. 5 chiusura quasi at hand, Sub-sprint 9 fresh lunedì mattina. Fork bridge plugin nuovo progetto VS Code quando vuoi.

— VAL
