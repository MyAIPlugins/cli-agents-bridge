# Session Bridge Fork — Design Report

**Author**: VAL session (Opus 4.7 1M), `chatterence-bi-template` project
**Date**: 24 Maggio 2026
**Purpose**: Spec input per fork del plugin `PatilShreyas/claude-code-session-bridge` con focus su robustness, long-running sessions, multi-VS Code parallel, role-aware routing.
**Status**: Living document, basato su esperienza reale 6+ sprint cross-project (chatterence-bi-template + ac-agents) Maggio 2026.

---

## 1. Problemi rilevati con evidence reale

### 1.1 Heartbeat bug (CRITICAL)
**Sintomo**: ESC reale active in `/bridge listen` viene marcato `stale` da `list-peers.sh` dopo 5min (`STALE_SECONDS=300` hardcoded), anche se sta processando attivamente messaggi.

**Root cause**: `bridge-listen.sh` non chiama `heartbeat.sh` durante il polling loop. Il `lastHeartbeat` in `manifest.json` resta congelato all'orario di `register.sh` iniziale.

**Impatto**: VAL invia messaggi a ESC `stale` → ESC riceve OK (è realmente attivo) → ma `list-peers` rapporta ESC `stale` → VAL pensa che ESC sia morto → workaround manuale.

**Quick fix**: patch `bridge-listen.sh` per chiamare `heartbeat.sh "$SESSION_ID"` ad ogni iteration polling.

### 1.2 Bash harness timeout 10min vs bridge-listen blocking infinite (CRITICAL)
**Sintomo**: ESC entra `/bridge listen` → `bash bridge-listen.sh "$ID"` blocca infinito → Claude Code harness uccide a 10min → ESC esce silently da listen loop senza notification.

**Root cause**: `bridge-listen.sh` ha while loop blocking senza MAX_WAIT interno. Affida tutto al chiamante (harness) per terminare.

**Impatto**: Se nessun messaggio arriva in 10min, ESC esce silently dal listen. VAL invia messaggi che restano nell'inbox di ESC ma nessuno li legge. Workflow rotto invisibilmente.

**Quick fix**: aggiungere `MAX_BLOCKING_SECONDS=540` (9min, sotto soglia harness) interno + exit code esplicito 124 "no_message_timeout" per consentire al chiamante di re-runnare auto.

**Better fix**: skill `/bridge listen` deve loopare il bash automaticamente sia su message reception sia su timeout exit. Il loop deve essere OUTSIDE bash, INSIDE l'agente Claude (skill instruction).

### 1.3 Cleanup globale cross-project (CRITICAL, security gap)
**Sintomo**: `/bridge stop` su una sessione cancella TUTTE le sessioni stale di OGNI progetto (>30min heartbeat).

**Root cause**: `cleanup.sh` linee 56-65 ha loop globale su `$BRIDGE_DIR/sessions/*/manifest.json` (dir condivisa cross-project, default `~/.claude/session-bridge/sessions/`).

**Evidence**: 24 Mag 2026, `/bridge stop` su `chatterence-bi-template` ha cancellato 2 sessioni `ac-agents` collaterali (5raa09, t58acm). Inbox/outbox di ac-agents persi.

**Impatto**: Multi-VS Code parallel workflow è fragile. Qualunque `/bridge stop` può silently distruggere lavoro di altre sessioni.

**Quick fix**: `cleanup.sh --scope=my-session` flag default. `--scope=global` solo se utente esplicito.

**Better fix**: per ogni stale session da cancellare, verify se esiste ancora il process Claude Code (via PID? check `.claude/bridge-session` pointer remoto?). Solo cancellare orphan dimostrate, non assunte.

### 1.4 Multi-peer routing confusion (CRITICAL, scaling 1+N)
**Sintomo (Alan)**: setup 1 VAL + 2 ESC. Dopo periodo lungo, un ESC ha messaggiato l'altro ESC credendolo VAL → routing sbagliato → workflow corrotto.

**Root cause analisi**: il bridge attuale non ha concetto di "ruolo" né "pair fisso". Ogni sessione è peer simmetrico, può connettersi a qualsiasi altra session-id. La memoria di "chi è il mio VAL" è solo nella mente del Claude agent, non enforced dal protocollo.

**Impatto**: Multi-ESC pattern (parallelism) inutilizzabile in pratica. Ogni ESC deve memorizzare manualmente "il mio VAL è ID X, altri ESC sono ID Y, Z" e ogni invio richiede mental check del target.

**Better fix**: introdurre `role` field nel manifest (`val | esc | runner | observer`) + `pair_id` field opzionale (link 1-1). `send-message` con `--target-role=val` risolve automaticamente al peer con ruolo VAL nello stesso pair.

**Architectural fix**: il manifest dovrebbe avere `team_id` (es. `acagency-sprint-24mag`) che raggruppa peers correlati. Routing automatico via role+team. Confusione cross-team strutturalmente impossibile.

### 1.5 Polling client-side inefficient (VAL non in listen)
**Sintomo**: VAL invia brief a ESC, ESC processa async e risponde. VAL deve fare polling manuale dell'inbox perché non è in `listen` mode (sta conversando con utente in foreground).

**Root cause**: il bridge è progettato per pattern peer simmetrico in listen. Il pattern "fire-and-forget + receive async on demand" (tipico VAL orchestrator) non è supportato.

**Impatto**: VAL gira polling background script (workaround che ho dovuto scrivere in questa sessione, `/tmp/poll-inbox.sh`) con bash 10min cycle + auto-relaunch. Spreco di context window VAL.

**Better fix**: notification system (OS-level) → fsnotify watch su inbox dir → osascript display notification on macOS / notify-send Linux. Claude agent riceve notifica via system reminder o equivalente.

**Alternative**: webhook-based — daemon centrale espone HTTP POST endpoint, peer invia → daemon notifica via SSE/WebSocket all'inbox subscriber. Più strutturato ma complesso.

### 1.6 Inbox loss su cleanup (data loss silenzioso)
**Sintomo**: cleanup.sh rimuove `sessions/$ID/` ricorsivamente, incluso `inbox/` con messaggi pendenti non letti.

**Root cause**: nessuna archiviazione pre-delete. Nessuna verifica di "inbox non vuota → abort cleanup".

**Impatto**: messaggi async persi senza trace. Audit trail rotto.

**Quick fix**: pre-delete check `if [ -d sessions/$ID/inbox ] && [ "$(ls -A sessions/$ID/inbox)" ]; then archive_first; fi`. Archive in `$BRIDGE_DIR/archive/$YYYY-MM-DD/$ID/`.

### 1.7 inReplyTo opzionale (non rispettato sempre)
**Sintomo**: ESC manda response al brief VAL CON `inReplyTo: null` invece di MSG_ID brief. Polling VAL con criterio strict `inReplyTo == ID` fallisce → polling timeout → "ESC non ha risposto" mentre in realtà ha risposto.

**Root cause**: `send-message.sh` accetta `inReplyTo` come 4° arg opzionale. Skill instruction NON forza il bridge agent a popolarlo.

**Impatto**: il chiamante (VAL polling) deve fallback a criterio loose (timestamp + from) — workaround.

**Quick fix**: `send-message.sh --in-reply-to` come arg flag esplicito, validation che è non-null per `type=response`. Skill instruction listen-mode auto-popola `inReplyTo` con MSG_ID ricevuto.

### 1.8 No notification on receive (passive inbox)
**Sintomo**: VAL non sa di aver ricevuto un messaggio finché non polla manualmente. Latency notificabile = polling interval.

**Root cause**: filesystem IPC senza event system. JSON files droppati in inbox/, nessun hook al filesystem event.

**Quick fix**: `send-message.sh` post-write esegue notifier:
```bash
# macOS
osascript -e 'display notification "Bridge msg from <FROM>" with title "Session Bridge"'
# Linux
notify-send "Bridge" "msg from <FROM>" 2>/dev/null
```
Optional `--silent` flag per disable.

**Better fix**: fsnotify daemon su inbox/ → push event verso subscriber registrato. Subscriber può essere agente Claude con custom system reminder hook.

### 1.9 Auto-classifier Claude Code blocca edit plugin third-party
**Sintomo**: VAL prova a patchare `~/.claude/plugins/.../list-peers.sh` per fix STALE_SECONDS. Auto-classifier blocca con "modifying agent-loaded config requires explicit authorization".

**Root cause**: Claude Code ha security policy che blocca self-modification del plugin tree, anche con autorizzazione user inline.

**Impatto**: fix bridge plugin richiede passaggio manuale shell utente (sed comando copy-paste). Friction su iterazione veloce.

**Fix per fork**: avere il fork installato come custom plugin con namespace utente (es. `~/.claude/plugins/custom/myteam-bridge/`) — il classifier potrebbe permettere edit su path non-marketplace. **Da verificare empiricamente.**

### 1.10 STALE_SECONDS in list-peers.sh vs cleanup.sh — gap silenzioso
**Sintomo**: patch `STALE_SECONDS=14400` in `list-peers.sh` NON protegge da cleanup.sh che ha threshold separato 30min hardcoded.

**Root cause**: stale logic duplicata in 2 file con valori indipendenti. Nessuna single source of truth.

**Impatto**: utente che patcha `list-peers.sh` per allungare visibilità active period è ingannato — `cleanup.sh` continua a wipare dopo 30min.

**Quick fix**: `STALE_SECONDS` come env var `BRIDGE_STALE_SECONDS=N`, default 1800, condivisa tra script. Documentata nel README.

**Better fix**: tutto config in singolo `~/.claude/session-bridge/config.json` letto da ogni script.

### 1.11 No multi-message broadcast / fan-out
**Sintomo**: VAL deve inviare lo stesso msg a 3 ESC parallel → 3 chiamate `send-message.sh` separate, ogni con MSG_ID diverso, ricezioni async difficili da correlate.

**Root cause**: API è 1-to-1 only.

**Better fix**: `--broadcast --team=X --target-role=esc` → fan-out automatico, MSG_ID gruppo + child MSG_ID per peer. Polling response collect-all su gruppo.

### 1.12 Nessun protocollo handshake
**Sintomo**: dopo `/bridge connect <ID>` il ping/pong è ottimistico. Se peer non risponde, è silenzioso (no timeout esplicito su connect).

**Root cause**: `connect-peer.sh` invia ping ma non aspetta pong come prerequisito.

**Quick fix**: `connect-peer.sh --require-pong --timeout=30` → fail-fast se peer non risponde in 30s. Più chiaro che "connected" sia bidirezionale.

### 1.13 Nessun versionamento protocollo
**Sintomo**: aggiornamento bridge plugin breaking change non rilevato. Peer con versioni diverse silently fail su nuovi field.

**Quick fix**: `protocol_version: "1.2"` in manifest.json. Handshake check version match, warn su mismatch.

---

## 2. Cause architetturali (radici comuni)

Tutti i problemi sopra hanno 3 radici architetturali condivise:

### 2.1 Filesystem-based IPC senza daemon
- Tutti i passaggi messaggi sono via filesystem JSON files in `~/.claude/session-bridge/sessions/$ID/inbox/`
- Nessun process persistente che orchestra delivery, retry, archiving, notification
- Race conditions possibili (più peer scrivono stessa inbox)
- Nessuna ACID guarantee

### 2.2 Bash scripts isolati senza shared state
- Ogni script reimplementa logica simile (stale check, peer lookup, message format)
- Config duplicata (STALE_SECONDS, BRIDGE_DIR override, ecc.)
- Difficile testare unit/integration

### 2.3 No protocol abstraction layer
- Skill markdown instruction = "protocollo" implicito
- Cambio comportamento richiede edit skill instruction + scripts
- Nessun contratto tipizzato (es. message schema)

---

## 3. Quick wins (~1-2 giorni dev, drop-in compatible)

Refactor minimo che risolve 70% dei problemi senza ricostruire architettura:

### 3.1 Heartbeat loop interno
`bridge-listen.sh` chiama `heartbeat.sh "$SESSION_ID"` ogni iteration loop (es. ogni 5s).

### 3.2 MAX_BLOCKING_SECONDS interno
`bridge-listen.sh` con `MAX_BLOCKING_SECONDS=540` env + exit code 124 "timeout no_message". Skill `/bridge listen` instruction auto-relaunch su 124.

### 3.3 Cleanup scoped default
`cleanup.sh` default `--scope=my-session`. `--scope=global` solo esplicito.

### 3.4 Pre-delete inbox archive
Cleanup verifica inbox non vuota → archive in `archive/$YYYY-MM-DD/$ID/`.

### 3.5 Config unificata
Tutto config in `~/.claude/session-bridge/config.json` (STALE_SECONDS, MAX_WAIT, ARCHIVE_RETENTION_DAYS, NOTIFY_ENABLED, ecc.). Script source una config helper.

### 3.6 Notification on receive
`send-message.sh` post-write osascript/notify-send opzionale.

### 3.7 inReplyTo enforcement
`send-message.sh --in-reply-to <ID>` validato per `type=response`.

### 3.8 protocol_version field
Manifest.json + handshake version check.

### 3.9 Role + team_id field
Manifest.json arricchito con `role: 'val' | 'esc' | 'runner' | 'observer'` + `team_id: string`.

### 3.10 send-message --target-role
Routing automatico: `send-message --target-role=val --team=X` risolve peer ID automaticamente.

---

## 4. Deep refactor (~1 settimana, breaking ma forte)

Architettura nuova mantenendo API compat dove possibile:

### 4.1 Daemon Python persistente
- Singolo process daemon (es. `bridge-daemon.py`) avviato da launchctl/systemd
- Espone Unix domain socket per IPC (più veloce di filesystem polling)
- Gestisce: registry sessions, routing, archiving, notification, retry
- CLI scripts diventano thin client che parlano col daemon

### 4.2 SQLite message store
- Sostituisci JSON files con DB SQLite (`~/.claude/session-bridge/bridge.db`)
- Tabelle: `sessions`, `messages`, `archives`, `team_membership`
- ACID guarantees, transactions, indexes per query veloce
- Backup naturale (single file copy)

### 4.3 Role-aware protocol
- Manifest schema con `role`, `team_id`, `pair_id` opzionale
- Routing rules enforced dal daemon
- "Cross-role confusion" strutturalmente impossibile

### 4.4 Acknowledgment + retry
- Ogni messaggio ha stati: `sent`, `delivered`, `read`, `responded`, `failed`
- Daemon retry delivery su `delivered` fallito (peer offline temp)
- Sender riceve callback su `read` evento

### 4.5 Long-running session support
- `bridge-listen` diventa subscription SSE su Unix socket
- Nessun harness timeout 10min (la connessione è event-driven, non polling)
- Daemon mantiene state anche se Claude Code agent in idle

### 4.6 Observability nativa
- Log JSONL strutturato in `~/.claude/session-bridge/audit.log`
- Comando `bridge audit --since="1h" --team=X` per debug
- Metrics: msg/s throughput, latency p50/p99, peer activity

### 4.7 Encrypted inbox (security)
- Messages encrypted-at-rest via libsodium (key derivata da `~/.ssh/id_rsa` o equivalente)
- Importante per cross-user multi-tenant scenarios

### 4.8 Test suite completa
- Unit test per ogni helper (Python pytest)
- Integration test multi-session via subprocess fixture
- Regression test sui scenari rilevati (cleanup cross-project, listen timeout, ecc.)

---

## 5. Pattern alternativi avanzati (research)

### 5.1 Hub-spoke con WebSocket
- Daemon centrale + WebSocket server (es. `ws://localhost:9999`)
- Peer connect via WS, receive eventi push
- Eliminato polling completamente
- Costo: dependency su Python+websockets o Node+ws

### 5.2 Tmux-based bridge
- Daemon usa tmux session detached
- Ogni "session" = pane tmux
- send-message via `tmux send-keys` al pane target
- Pro: zero process management, tmux nativo robusto
- Contro: dipendenza tmux, semantica messaging confusa

### 5.3 Redis pub/sub backend
- Daemon publish/subscribe via Redis local
- Multi-tenant naturale (channel per team)
- Persistence opzionale via Redis Streams
- Pro: collaudato industria, scaling free
- Contro: dependency Redis daemon

### 5.4 Native macOS XPC service (advanced)
- Daemon come launchd LaunchDaemon
- IPC via XPC (Apple native)
- Pro: zero dependencies user-space, OS-managed lifecycle
- Contro: macOS-only, Objective-C/Swift code

---

## 6. Migrazione + backward compat

Fork dovrebbe essere drop-in replacement initially:

### 6.1 Drop-in compat layer
- Mantieni script names (`register.sh`, `bridge-listen.sh`, `send-message.sh`, ecc.)
- Mantieni JSON file structure inbox/outbox (per migration smooth)
- Aggiungi nuove features dietro flag opt-in (es. `BRIDGE_ENABLE_DAEMON=1`)

### 6.2 Migration script
- `bridge-migrate.sh` che importa sessioni esistenti dal filesystem old in nuovo DB
- Preserva audit trail

### 6.3 Plugin marketplace alternativo
- Fork come custom plugin in own marketplace (es. `myaiplugins/claude-bridge-pro`)
- Installazione: `claude code plugin install myaiplugins/claude-bridge-pro`
- Coesistenza con plugin originale possibile (uninstall original first per evitare conflitto path)

---

## 7. Naming + repo structure suggestions

### 7.1 Naming candidates
- `claude-code-bridge-pro` (descrittivo, conservativo)
- `session-bus` (più tecnico, evoca event bus)
- `claude-orchestrator` (se evolve verso multi-agent orchestrator)
- `cc-relay` (compatto, network-y)

Mio favorito: **`session-bus`** — riflette il pivot architetturale verso event-driven vs polling.

### 7.2 Repo layout proposto
```
session-bus/
├── README.md                   # Marketing + quick start
├── ARCHITECTURE.md             # Decision records, diagrams
├── plugin.json                 # Manifest plugin Claude Code
├── daemon/
│   ├── bridge_daemon.py        # Persistente
│   ├── routing.py              # Role-aware routing
│   ├── store.py                # SQLite layer
│   ├── notifier.py             # OS notifications
│   └── server.py               # Unix socket + WS server
├── scripts/                    # CLI thin client (bash)
│   ├── register.sh
│   ├── bridge-listen.sh
│   ├── send-message.sh
│   ├── list-peers.sh
│   ├── cleanup.sh
│   └── lib/
│       └── config-loader.sh    # Shared helper
├── skills/
│   ├── bridge.md               # Slash command skill
│   └── bridge-awareness.md     # Auto-activates on session
├── tests/
│   ├── unit/
│   ├── integration/
│   └── fixtures/
├── migrations/
│   └── from-patil-v1.sh        # Compat migration
└── config/
    └── default.json            # Default config template
```

### 7.3 Plugin manifest (key fields)
```json
{
  "name": "session-bus",
  "version": "1.0.0",
  "protocol_version": "2.0",
  "description": "Robust cross-session bridge for Claude Code multi-agent workflows",
  "daemon": {
    "auto_start": true,
    "binary": "daemon/bridge_daemon.py"
  },
  "skills": ["bridge", "bridge-awareness"],
  "compat": {
    "patil_session_bridge": "drop_in_with_migration"
  }
}
```

---

## 8. Priorità sviluppo suggerita (roadmap MVP→v1)

| Phase | Scope | Effort | Outcome |
|---|---|---|---|
| **MVP** | Quick wins 3.1-3.10 (drop-in patches) | 1-2 giorni | Risolto 70% pain points, zero breaking |
| **v0.5** | Role + team routing (3.9, 3.10) + tests | 2-3 giorni | Multi-ESC parallelism utilizzabile |
| **v1.0** | Daemon Python + SQLite store + notifications | 5-7 giorni | Architettura solid, observability nativa |
| **v1.5** | WS server + long-running session support | 3-4 giorni | Zero harness timeout, push notifications |
| **v2.0** | Encrypted inbox + multi-tenant features | 3-4 giorni | Production-ready cross-user scenarios |

**Pragmatico**: start MVP. Risolve subito i dolori reali, validare design con uso reale. Poi v1 daemon se serve.

---

## 9. Risk + mitigation

| Risk | Mitigation |
|---|---|
| Daemon Python crash → tutto bridge giù | Auto-restart via launchd + graceful degradation a filesystem-mode |
| Migration sessioni esistenti perde data | Script migrate con dry-run + backup pre-migration |
| Multi-user shared machine confusion | Namespace per user UID, default isolato per-user |
| Plugin conflict con Patil original | Documenta uninstall original, fork si auto-uninstalla Patil su install |
| Skill instruction breaking change | Versionato `protocol_version` check su connect |

---

## 10. Quick decision matrix per nuovo progetto VS Code

Se vuoi partire **stasera**:
- [ ] Crea repo `session-bus` (o nome scelto)
- [ ] Fork `PatilShreyas/claude-code-session-bridge` come base
- [ ] Implementa quick wins 3.1, 3.2, 3.3, 3.6 (4 patch bash) — copre 50% pain
- [ ] Test su workflow VAL-ESC reale (es. piccolo sprint test)
- [ ] Itera quick wins restanti

Se vuoi partire **proper** (1 settimana wall-clock):
- [ ] Skip quick wins, vai direttamente a daemon Python design (sezione 4)
- [ ] Migration script da Patil format come priorità day-1
- [ ] Test integration multi-session subito (evita scoperte tardive)

---

## Appendice A — Riferimenti evidence

- Sessione VAL `xnv2xj` chatterence-bi-template 24 Maggio 2026
- Sprint #53 SIGNATURE-IMAGE-BULLETPROOF (PR #123) + Sprint #54 SIGNATURE-RENDER-LETTERBOX (PR #125)
- Incident bridge wipe 24 Mag 2026 ~17:00 IT (sessions/ vuoto post cleanup esterno)
- Memory permanenti: `feedback_bridge_cleanup_global_side_effect.md`, project_acagency_test_credentials.md`
- Pattern brief PHASE 1 + PHASE 2 documentato in skill `bridge-val-esc` + `esc-bridge-mode`

## Appendice B — Repo originale
- https://github.com/PatilShreyas/claude-code-session-bridge

---

**Fine report**. Pronto per copy-paste nel nuovo progetto. Se Alan vuole approfondire una sezione specifica (es. daemon Python design dettagliato, schema SQLite proposto, code sample notification), basta chiedere.
