---
doc: bridge-fork-proposal
created: 2026-05-24
val: Claude Opus 4.7 (valutatore role)
target: input per nuovo progetto Alan, fork plugin `PatilShreyas/claude-code-session-bridge` v0.1.1
empirical-source: session p1-wp-translator (~2h dual-ESC VAL+Architetto) + cross-project ac-agents (5 sprint) + chatterence-bi-template (7 sprint)
status: PROPOSAL — Alan ratifica scope fork + priority
---

# Bridge Plugin Fork — Proposal di miglioramento

Report completo limitazioni + bug + frizioni del plugin `claude-code-session-bridge` v0.1.1 osservate empiricamente durante long-run multi-ESC. Input per fork ad alta robustezza.

## 1. Executive summary

**Plugin attuale**: utility bash + jq (MIT, autore Shreyas Patil). Permette peer-to-peer message passing tra session Claude Code via JSON files in `~/.claude/session-bridge/sessions/{id}/{inbox,outbox}/`. Polling-based.

**Pro confermati**: zero deps esterne, MIT, semplice. Funziona per use case base (1 VAL + 1 ESC, short sprint).

**Contro empiricamente verificati**: 5 bug critici + 15 frizioni operative + assunzioni architetturali che limitano use case avanzati (multi-ESC, long-run, sub-cwd condivise, sessions interrupted).

**Verdict fork**: vale assolutamente. Non riscrivere da zero — fork + refactor mirato è approccio safe. Target: stesso DX semplice + robustezza enterprise + multi-peer native.

## 2. Context di utilizzo empirico

### Sessione 2026-05-24 p1-wp-translator (questa)
- 1 VAL + 2 ESC simultanei (analisi/ + src/) per ~2h
- Triade parallela VAL+ESC-A+ESC-B con working dir disgiunte
- 3 incidenti di cleanup-misbehavior (1 zombie + 2 wrong-session kill)
- 1 ping timeout falso positivo (response in outbox ma `bridge-receive` non vista)

### Cross-project precedente
- ac-agents: 5 sprint pilota 2026-05-23 con 1 VAL + 1 ESC (workflow base, no incidenti)
- chatterence-bi-template: 7 sprint precedenti con 1 VAL + 1 ESC (validato)

### Volumi
- ~50+ message scambiati cumulati 2026-05-24
- 5 brief PHASE 1/2 (lunghi 1-2K caratteri ciascuno)
- 9 commit prodotti su main durante test

## 3. Bug critici identificati (con repro + fix proposto)

### BUG-1 — `cleanup.sh` ignora `BRIDGE_SESSION_ID` env var

**Severità**: 🔴 CRITICA — può causare kill di session sbagliata

**Repro empirico**:
1. VAL session `jc4tbb` registrata in cwd `/p1-wp-translator/` (parent)
2. ESC-A in cwd `/p1-wp-translator/analisi/` (subdir) imposta `BRIDGE_SESSION_ID=km343j` env + invoca `cleanup.sh`
3. Script ignora `$BRIDGE_SESSION_ID`, usa solo `PROJECT_DIR` default `$(pwd)` = `/analisi/`
4. Lookup confronta `/analisi/` con `manifest.json:projectPath` di ogni session
5. ESC-A km343j ha projectPath `/analisi/` (match esatto) → MA il bug fallback parent path (BUG-2) prima ha matchato `/p1-wp-translator/` parent
6. **Killata wrong session `jc4tbb` (VAL parent)** invece di `km343j` (ESC-A subdir)

**Code path bug**: `cleanup.sh` non legge mai `$BRIDGE_SESSION_ID`. Hardcoded `SESSION_ID=$(get-session-id.sh)` che fa lookup automatico.

**Fix proposto**:
```bash
# cleanup.sh — priority order esplicita
if [ -n "$BRIDGE_SESSION_ID" ]; then
  SESSION_ID="$BRIDGE_SESSION_ID"   # priority 1: env override
elif [ -f "$PWD/.claude/bridge-session" ]; then
  SESSION_ID=$(cat "$PWD/.claude/bridge-session")   # priority 2: cwd file
else
  echo "Error: no session ID specified. Set BRIDGE_SESSION_ID or run from cwd with .claude/bridge-session" >&2
  exit 1
fi

# Verify manifest:projectPath matches our cwd EXACTLY (NO fallback)
MANIFEST="$SESSIONS_DIR/$SESSION_ID/manifest.json"
PROJECT_PATH=$(jq -r '.projectPath' "$MANIFEST")
if [ "$PROJECT_PATH" != "$PWD" ] && [ "$PROJECT_PATH" != "$PROJECT_DIR" ]; then
  echo "Error: session $SESSION_ID belongs to $PROJECT_PATH, not your cwd $PWD. Use BRIDGE_SESSION_ID + matching PROJECT_DIR explicitly." >&2
  exit 1
fi
```

### BUG-2 — `get-session-id.sh` fallback parent path lookup

**Severità**: 🔴 CRITICA — causa BUG-1 propagation

**Repro empirico**:
1. Sessions registered:
   - `jc4tbb` projectPath `/p1-wp-translator/`
   - `km343j` projectPath `/p1-wp-translator/analisi/`
2. ESC-A invoca da cwd `/p1-wp-translator/analisi/wp-content/` (subdir nested)
3. `get-session-id.sh` cerca exact match → no match
4. **Fallback parent path** → scans up directory tree
5. Match prima `/p1-wp-translator/` parent (jc4tbb VAL) PRIMA di `/p1-wp-translator/analisi/` (km343j ESC-A)
6. Ritorna `jc4tbb` (wrong) invece di `km343j` (correct)

**Code path bug**: `get-session-id.sh` algoritmo fallback ascende uno step per volta cercando match — ma se SO + PARENT entrambi hanno session, sceglie il primo found (deterministico ma sbagliato per nested setup).

**Fix proposto**:
```bash
# get-session-id.sh — LONGEST prefix match priority (deeper wins)
BEST_MATCH=""
BEST_LEN=0
for manifest in "$SESSIONS_DIR"/*/manifest.json; do
  PATH_IN_MANIFEST=$(jq -r '.projectPath' "$manifest")
  if [[ "$PWD" == "$PATH_IN_MANIFEST" || "$PWD" == "$PATH_IN_MANIFEST"/* ]]; then
    LEN=${#PATH_IN_MANIFEST}
    if [ $LEN -gt $BEST_LEN ]; then
      BEST_MATCH=$(jq -r '.sessionId' "$manifest")
      BEST_LEN=$LEN
    fi
  fi
done

if [ -z "$BEST_MATCH" ]; then
  echo "Error: no session matches cwd $PWD or its parents" >&2
  exit 1
fi
echo "$BEST_MATCH"
```

Logic: tra tutti i match, scegli quello con projectPath più lungo (= più specifico). `/analisi/` (lungo) batte `/p1-wp-translator/` (corto).

### BUG-3 — `bridge-listen.sh` non aggiorna `lastHeartbeat` in polling loop

**Severità**: 🟡 ALTA — falsi positivi "stale" su session attive

**Repro empirico**:
1. ESC-B `l3xi3u` running active, polling inbox ogni 1 sec
2. Idle 10 min senza message ricevuti
3. `lastHeartbeat` nel manifest resta a `startedAt` (NON aggiornato dal polling loop)
4. `list-peers.sh` con `STALE_SECONDS=300` marca ESC-B stale
5. `connect-peer.sh` warning "stale (last active 618s ago)"

**Workaround locale** applicato in `list-peers.sh` linea 15: `STALE_SECONDS=3600` (1h). MA `connect-peer.sh` ha hardcoded 300s separato → inconsistenza.

**Fix proposto**:
```bash
# bridge-listen.sh — heartbeat update ogni iteration
while true; do
  # ... existing message processing ...

  # Update heartbeat
  NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
  jq --arg ts "$NOW" '.lastHeartbeat = $ts' "$MANIFEST" > "$MANIFEST.tmp" && mv "$MANIFEST.tmp" "$MANIFEST"

  sleep "$POLL_INTERVAL"
done
```

E unificare `STALE_SECONDS` in env var globale o config file:
```bash
# bridge-config.sh (NEW)
export BRIDGE_STALE_SECONDS=300        # 5 min default
export BRIDGE_POLL_INTERVAL=2          # 2 sec default
export BRIDGE_MAX_INBOX_SIZE=100       # cap messages
export BRIDGE_MAX_MESSAGE_BYTES=65536  # 64KB max per message
```

Tutti gli script source `bridge-config.sh` come prima cosa.

### BUG-4 — `bridge-receive.sh` polling timeout falso positivo

**Severità**: 🟡 ALTA — VAL pensa peer morto quando in realtà ha risposto

**Repro empirico**:
1. VAL invia ping a ESC-B `msg-o9cchq82lbae` a `10:54:36Z`
2. ESC-B risponde `msg-cmu90rt6ci2y` a `10:55:32Z` (43 sec dopo)
3. `bridge-receive.sh` timeout 90s passa, NON vede la risposta
4. Ritorna "No response received after 90s. The peer may be inactive."

**Diagnosi**: il polling guarda `inbox/` di VAL per file `inReplyTo=$MSG_ID`. ESC-B la mette in `outbox/` proprio. Boh — il delivery da outbox ESC-B → inbox VAL avviene via cron-like sync? Oppure è bug timing window.

**Fix proposto**:
```bash
# bridge-receive.sh — verifica MULTI-LOCATION + filesystem sync
TIMEOUT=${3:-30}
START=$(date +%s)

while true; do
  # Check VAL inbox
  for f in "$SESSIONS_DIR/$MY_SESSION/inbox/"*.json; do
    [ -e "$f" ] || continue
    REPLY_TO=$(jq -r '.inReplyTo // empty' "$f")
    if [ "$REPLY_TO" = "$MSG_ID" ]; then
      cat "$f"; exit 0
    fi
  done

  # FALLBACK: check ALL peer outboxes for our reply (race condition workaround)
  for peer_outbox in "$SESSIONS_DIR"/*/outbox; do
    [ -d "$peer_outbox" ] || continue
    for f in "$peer_outbox"/*.json; do
      [ -e "$f" ] || continue
      REPLY_TO=$(jq -r '.inReplyTo // empty' "$f")
      TO_SESSION=$(jq -r '.to' "$f")
      if [ "$REPLY_TO" = "$MSG_ID" ] && [ "$TO_SESSION" = "$MY_SESSION" ]; then
        cat "$f"; exit 0
      fi
    done
  done

  # Timeout check
  NOW=$(date +%s)
  if [ $((NOW - START)) -ge $TIMEOUT ]; then
    echo "No response received after ${TIMEOUT}s." >&2
    exit 2
  fi

  sleep 1
done
```

Approach: doppio poll (VAL inbox primary + peer outbox fallback) per coprire race window dove message non è ancora trasferito.

### BUG-5 — `register.sh` non valida cwd unicity

**Severità**: 🟠 MEDIA — può causare session collision silenziosa

**Repro empirico**: due Claude Code stessa cwd → due register diversi possibili con stessi parametri → ID sovrapposti.

**Fix proposto**:
```bash
# register.sh — check existing session in cwd
if [ -f "$PWD/.claude/bridge-session" ]; then
  EXISTING=$(cat "$PWD/.claude/bridge-session")
  if [ -d "$SESSIONS_DIR/$EXISTING" ]; then
    echo "Warning: session $EXISTING already exists for this cwd." >&2
    echo "Either resume (echo \$BRIDGE_SESSION_ID=$EXISTING) or run cleanup first." >&2
    exit 1
  fi
fi
```

## 4. Frizioni operative (limitazioni UX)

### FRIC-1 — Session ID auto-generated non-mnemonic

**Problema**: ID come `r2iqgs`, `8f3zbc`, `jc4tbb`, `km343j`, `l3xi3u`, `8yc337` sono random. Alan/VAL/ESC confusi su chi è chi. **Empiricamente**: Alan ha passato VAL session ID come ESC-A ID (sessione 2026-05-24).

**Fix proposto**: ID auto-naming based on (project + role + index):
```
<project-slug>-<role>-<incr>
es. p1-wp-translator-val-1
    p1-wp-translator-esc-analisi-1
    p1-wp-translator-esc-src-1
```

Role autodetect via cwd naming convention (`/src/` → role=esc, parent root → role=val, `/analisi/` → role=esc-analisi). Override via env:
```
BRIDGE_SESSION_NAME=val-translator bridge listen
```

### FRIC-2 — File-based briefing pattern obbligatorio

**Problema**: heredoc inline con apici si rompe per brief >500 chars. Pattern obbligatorio: Write tempfile + `cat` env var (skill `bridge-val-esc` documenta).

**Fix proposto**: `send-message.sh` accetta `--file <path>`:
```bash
bash send-message.sh <peer> query --file /tmp/brief.md
```

Internamente legge il file e fa escape JSON corretto. Elimina friction quoting.

### FRIC-3 — No re-attach a session interrupted

**Problema**: Se Claude Code session crash o si killa, `/bridge listen` di nuovo = NUOVO ID. Bridge channel perso. Identità VAL/ESC non preservata.

**Fix proposto**: stable session ID derivato da `<project>+<cwd>+<machine>` hash. Resume da last message processed:
```bash
bridge listen --resume  # reuse last known session for this cwd
```

`.claude/bridge-session` ricorda ultimo ID. Se manifest dir mancante → ricrea con stesso ID. Se inbox ha msg pending → processa.

### FRIC-4 — Single-attention VAL FIFO queue

**Problema**: VAL processa un msg alla volta. Multi-ESC concurrent requests si accumulano. ESC-A può attendere mentre VAL processa ESC-B.

**Fix proposto**: brief iniziali completi (skill `bridge-val-esc` già consiglia) mitigano. Per fork: queue priority + concurrent handling Claude assistant non triviale (richiede modifica Claude Code core o multi-threading nel listen process — fuori scope plugin bash).

**Alternative fork strategy**: dedicated dispatcher process per VAL che riceve N messaggi, fa rendezvous, presenta a VAL come "bulk inbox" per processing batch. Più complesso.

### FRIC-5 — VAL working tree HEAD migration

**Problema**: ESC-B fa `git checkout -b feature/x` su working tree shared. HEAD del VAL working tree migra automaticamente. VAL commit accidentalmente su branch ESC-B.

**Repro empirico**: 2 fix necessari oggi (commit `bce0553` + `b0f1eea` accidentalmente su feature branch invece di main).

**Fix proposto**: lato plugin, `register.sh` rileva working tree shared + warn:
```bash
if git rev-parse --git-dir > /dev/null 2>&1; then
  GIT_TOPLEVEL=$(git rev-parse --show-toplevel)
  for other_session_path in $(jq -r '.projectPath' "$SESSIONS_DIR"/*/manifest.json 2>/dev/null); do
    OTHER_TOPLEVEL=$(cd "$other_session_path" && git rev-parse --show-toplevel 2>/dev/null)
    if [ "$GIT_TOPLEVEL" = "$OTHER_TOPLEVEL" ] && [ "$other_session_path" != "$PWD" ]; then
      echo "Warning: sharing git working tree with session at $other_session_path." >&2
      echo "Consider using 'git worktree add' for isolation: each session in dedicated worktree." >&2
    fi
  done
fi
```

Skill bridge-val-esc già menziona pattern, ma warning runtime del plugin lo fa visibile a tutti gli utenti.

### FRIC-6 — Polling-based introduce latenza

**Problema**: filesystem polling 1-5s interval. Round-trip ping ~8s anche per "connected ack" semplice.

**Fix proposto**: opzione `BRIDGE_TRANSPORT=socket`:
- Default: filesystem polling (backward compat)
- Alternative: Unix domain sockets per real-time push
- Plugin detection: se socket disponibile, usalo; altrimenti fallback file

Implementation:
- `bridge-listen.sh` apre socket `~/.claude/session-bridge/sockets/<session>.sock`
- `send-message.sh` write to socket + fallback file se sock missing
- Latenza ridotta da ~1s polling a ~10ms socket

### FRIC-7 — No structured message types / schema

**Problema**: `type: query|response|ping|notify` come string. No schema validation. Errori silenziosi su malformed JSON.

**Fix proposto**: JSON Schema per ogni type:
```json
{
  "$schema": "https://json-schema.org/draft-07/schema#",
  "type": "object",
  "required": ["id", "from", "to", "type", "timestamp", "content"],
  "properties": {
    "id": {"type": "string", "pattern": "^msg-[a-z0-9]{12}$"},
    "from": {"type": "string", "minLength": 1},
    "to": {"type": "string", "minLength": 1},
    "type": {"enum": ["query", "response", "ping", "notify", "event"]},
    "timestamp": {"type": "string", "format": "date-time"},
    "content": {"type": "string", "maxLength": 65536},
    "inReplyTo": {"type": ["string", "null"], "pattern": "^msg-[a-z0-9]{12}$"},
    "threadId": {"type": "string", "description": "Conversation thread ID for multi-turn"},
    "metadata": {"type": "object"}
  }
}
```

Validation in `send-message.sh` (via `jq` o `ajv`) prima di write.

### FRIC-8 — No transcript persistente

**Problema**: cleanup elimina inbox/outbox. Audit cross-session impossibile. Lessons learned multi-sprint persi senza copy manuale.

**Fix proposto**: append-only log:
```
~/.claude/session-bridge/transcripts/<date>/<session-id>.log
```

Ogni message ricevuto/inviato append in log. Survive cleanup. Queryable per retro:
```bash
bridge transcript --session p1-wp-translator-val-1 --since 2026-05-24
bridge transcript --grep "BLOCKER" --since 1week
```

### FRIC-9 — No security / ACL between peers

**Problema**: qualsiasi session su host può inviare a qualsiasi altra. OK dev locale, ma rischio cross-project (sessione `chatterence-bi-template` può tecnicamente parlare a `p1-wp-translator` peer).

**Fix proposto**: optional ACL via project naming:
```bash
# Default: allow same project only
ALLOW_PROJECT="p1-wp-translator"

# Permissive (current behavior):
ALLOW_PROJECT="*"
```

Config in `bridge-config.sh` per session. `connect-peer.sh` verify project match prima di permettere connection.

### FRIC-10 — No diagnostic dashboard

**Problema**: per debug bridge issue serve `cat manifest.json` + `ls inbox/outbox` manuale.

**Fix proposto**: comando `bridge status` complete dashboard text:
```
$ bridge status
SESSIONS (3 active)
==================
ID                         PROJECT             ROLE   CWD                            UP     INBOX OUT MSG/MIN
val-translator-1           p1-wp-translator    val    /p1-wp-translator              2h     0     3   1.5
esc-analisi-1              p1-wp-translator    esc    /p1-wp-translator/analisi     1h     0     1   0.5
esc-src-1                  p1-wp-translator    esc    /p1-wp-translator/src         1h     0     2   0.9

CONNECTIONS
===========
val-translator-1 ↔ esc-analisi-1 (last activity: 5min ago, total msg: 4)
val-translator-1 ↔ esc-src-1     (last activity: 2min ago, total msg: 6)

RECENT WARNINGS
===============
[12:03] esc-src-1 stale (last active 618s ago) — heartbeat bug? check bridge-listen
[11:55] cleanup attempt by esc-analisi-1 with PROJECT_DIR=/p1-wp-translator (parent) — DENIED (mismatch)
```

### FRIC-11 — `.claude/bridge-session` legacy persiste

**Problema**: dopo cleanup, file `.claude/bridge-session` rimane in cwd. Next run riusa ID ma global store non ha manifest → register fa silent recreate con stesso ID, ma context conversation è perso.

**Fix proposto**: `cleanup.sh` rimuove anche `$PWD/.claude/bridge-session`:
```bash
# At end of cleanup.sh
if [ "$KEEP_LOCAL_REF" != "1" ]; then
  rm -f "$PROJECT_PATH/.claude/bridge-session"
fi
```

Opt-out via env var per chi vuole resume-on-restart pattern.

### FRIC-12 — STALE_SECONDS hardcoded inconsistente

**Problema**: `connect-peer.sh` ha 300s hardcoded, `list-peers.sh` ha 3600s patched localmente. Inconsistenza fonte di confusion.

**Fix proposto**: unify via `bridge-config.sh` env var. Default 300s, opt-in 3600s per long-run sessions. Documentato in `--help`.

### FRIC-13 — No conversation thread ID

**Problema**: message correlation via `inReplyTo` solo single hop. Multi-turn conversation (PHASE 1 + clarification + PHASE 2) non ha thread ID. Audit messy.

**Fix proposto**: `threadId` field opzionale:
```json
{
  "id": "msg-abc123",
  "threadId": "thread-s1-setup-2026-05-24",
  "type": "query",
  "content": "..."
}
```

`bridge transcript --thread thread-s1-setup` mostra conversation in order.

### FRIC-14 — No backpressure / max queue

**Problema**: ESC può flood VAL inbox. Nessun limit. Disk fills.

**Fix proposto**: `bridge-listen.sh` enforce max inbox size:
```bash
INBOX_COUNT=$(ls "$INBOX" | wc -l)
if [ "$INBOX_COUNT" -ge "$BRIDGE_MAX_INBOX_SIZE" ]; then
  echo "Inbox full ($INBOX_COUNT/$BRIDGE_MAX_INBOX_SIZE). Slow down or process pending." >&2
  # Reject incoming, return error to sender via outbox
fi
```

### FRIC-15 — No "ESC-B confused VAL with another ESC" prevention

**Problema osservato OGGI**: ESC ha messaggiato l'altro ESC credendo fosse VAL (Alan's exact quote). Friction operativa importante.

**Diagnosi**: lookup peer via session ID random non-mnemonic + bug fallback parent → confusion identity.

**Fix proposto multi-livello**:
1. Friendly naming (FRIC-1) elimina random IDs
2. `send-message.sh` warn se destinatario è cross-role inaspettato:
   ```
   Warning: you (esc-analisi-1) are messaging esc-src-1 directly.
   Standard pattern is val-* → esc-* or esc-* → val-*.
   Override with --force if intentional.
   ```
3. Optional ACL DENY esc-to-esc by default (force val-mediated routing)

## 5. Architecture improvements (high-level)

### A. Transport layer plugabile

Current: filesystem polling JSON files (only)
Proposed: layered API
- `BridgeTransport` interface
- `FilesystemTransport` (current, default, backward compat)
- `UnixSocketTransport` (real-time, opt-in)
- `TcpTransport` (multi-machine, opt-in production)

Implementation: shell scripts call `bridge-transport.sh` abstraction. Strategy pattern.

### B. Configuration centralizzata

Current: env vars sparse + hardcoded
Proposed: `~/.claude/session-bridge/config.json`
```json
{
  "version": "0.2.0",
  "stale_seconds": 300,
  "poll_interval_ms": 1000,
  "max_inbox_size": 100,
  "max_message_bytes": 65536,
  "transport": "filesystem",
  "transcript_enabled": true,
  "transcript_path": "~/.claude/session-bridge/transcripts/",
  "session_naming": "friendly",  // "friendly" | "random"
  "default_role_per_cwd": {
    "/src/$": "esc-src",
    "/analisi/$": "esc-analisi"
  }
}
```

Per-session override via `.claude/bridge-config.json` in cwd.

### C. Role-based architecture

Current: session è "session", no role distinction
Proposed: explicit `role` field nel manifest:
```json
{
  "sessionId": "p1-wp-translator-val-1",
  "role": "val",  // "val" | "esc" | "observer" | "architetto"
  "permissions": {
    "can_cleanup_others": false,
    "can_broadcast": true,
    "can_observe_transcript": true
  }
}
```

Role-specific safeguards: `esc` cannot run `cleanup.sh` on `val` session (require admin role). Prevent BUG-1 + BUG-2 propagation.

### D. Message bus pattern (vs point-to-point)

Current: explicit `to` field per message (1-to-1)
Proposed: topic-based routing optional:
```json
{
  "to": null,
  "topic": "s1-setup",
  "type": "query",
  "content": "..."
}
```

VAL subscribe a `s1-setup` topic, ESC pubblica a topic. Multi-subscriber (es. Architetto observer riceve copia). Broadcast support nativo.

### E. Session lifecycle managed

Current: lifecycle è "register + listen + cleanup"
Proposed: stati esplicit:
- `registering` → `active` → `idle` → `pausing` → `paused` → `resuming` → `terminating` → `terminated`

Each transition has events broadcast to peers. Peers possono react (es. VAL pause auto-pause connected ESC).

### F. Diagnostic + observability

Current: cat files manuale
Proposed:
- `bridge status` (dashboard)
- `bridge logs <session>` (transcript history)
- `bridge debug <session>` (verbose internals)
- `bridge metrics` (counts msg/min, latency p50/p99, queue depths)
- Optional Prometheus metrics endpoint per production use

## 6. Roadmap fork proposta (priorità decrescente)

### 🔴 P0 — Critical bugs (week 1-2)

1. **BUG-1** Fix `cleanup.sh` honor `BRIDGE_SESSION_ID`
2. **BUG-2** Fix `get-session-id.sh` longest prefix match (no parent fallback)
3. **BUG-3** Fix `bridge-listen.sh` heartbeat update in polling loop
4. **BUG-4** Fix `bridge-receive.sh` dual-source polling (inbox + peer outboxes)
5. **BUG-5** Fix `register.sh` cwd unicity validation

**Deliverable**: v0.2.0 bug-fix release, drop-in replacement for v0.1.1.

### 🟡 P1 — Major UX improvements (week 3-4)

6. **FRIC-1** Friendly session naming
7. **FRIC-2** `--file` flag su `send-message.sh`
8. **FRIC-7** JSON Schema validation
9. **FRIC-8** Persistent transcript log
10. **FRIC-10** `bridge status` dashboard
11. **FRIC-11** Cleanup remove local ref
12. **FRIC-12** Unified STALE config

**Deliverable**: v0.3.0 quality of life release.

### 🟢 P2 — Architecture refactor (week 5-8)

13. **B. Config centralizzata** (`config.json` + `.claude/bridge-config.json`)
14. **C. Role-based architecture** (role field + permissions)
15. **FRIC-15** Cross-role messaging warnings
16. **FRIC-3** Re-attach a session interrupted
17. **FRIC-5** Working tree shared warning
18. **A. Transport pluggable** (filesystem + Unix socket)

**Deliverable**: v0.4.0 architecture overhaul.

### 🔵 P3 — Advanced features (week 9+)

19. **D. Message bus / topic routing**
20. **E. Lifecycle states**
21. **FRIC-13** Conversation thread ID
22. **FRIC-14** Backpressure
23. **FRIC-9** ACL/security
24. **F. Diagnostic + metrics**

**Deliverable**: v1.0.0 production-ready.

## 7. Testing strategy fork

### Unit test bash scripts
- BATS (Bash Automated Testing System) per ogni script
- Mock filesystem + jq calls

### Integration test
- Spawn 1 VAL + 2 ESC actual processes in test mode
- Verify message flow + edge cases
- Race condition tests (concurrent registers, cleanups)

### Regression test
- Suite di repro empiriche BUG-1..BUG-5
- Run before every release

### Performance benchmark
- Message round-trip latency (filesystem vs socket)
- Concurrent sessions scaling test
- Queue throughput

## 8. Backward compatibility

Fork dovrebbe essere drop-in replacement v0.1.1:
- Stessi slash commands (`/bridge listen`, `/bridge stop`, `/bridge peers`, etc.)
- Stesso layout filesystem (`~/.claude/session-bridge/sessions/`)
- Stessi script entry points (cleanup.sh, register.sh, etc.)
- Behavior identico per single VAL + single ESC use case (default)

Nuove feature opt-in via env vars o config flag. Esistenti user upgrade transparente.

## 9. Naming proposta fork

Opzioni:
1. `claude-bridge-pro` (positioning premium)
2. `claude-multi-bridge` (positioning multi-peer)
3. `claude-session-bridge-plus` (positioning incremental upgrade)
4. `ac-bridge` (vendor scope AC Agency)

Raccomandazione: **`ac-bridge`** se Alan vuole vendor-scoped (consistent con `ac-translator`, `ac-agency` brand) + reflect ownership fork.

License: MIT (compat upstream Patil).

## 10. Lessons learned cross-actor (catalog G/AP fork-specific)

Pattern emergenti da questa sessione + cross-project:

### G-fork-1 — Multi-cwd cleanup safety
ESC in subdir non deve mai eseguire cleanup senza esplicito `BRIDGE_SESSION_ID` + `PROJECT_DIR` matching manifest. Fallback parent path è bug critico.

### G-fork-2 — Friendly naming previene confusion
ID random 6-char non-mnemonic causa errori identità (Alan ha passato VAL ID come ESC ID). Friendly naming + role tag elimina.

### G-fork-3 — Heartbeat in polling loop obbligatorio
Senza heartbeat update, stale detection è broken. Plugin v0.1.1 affected.

### G-fork-4 — Working tree disgiunti per multi-ESC
ESC che fanno git operations su working tree shared causano HEAD migration. Pattern safe: `git worktree add` per ogni ESC, oppure VAL+ESC su cwd disgiunti con sub-cwd specifica.

### G-fork-5 — Transcript persistente abilita retrospective
Senza transcript, ogni cleanup distrugge audit history. Retrospettiva G29 velocity calibration richiede log persistent.

### AP-fork-1 — ESC sostituisce VAL
Quando bridge cracks, ESC che intraprende VAL responsibility (es. orchestration) viola pattern. Boundary HARD VAL/ESC documentata.

### AP-fork-2 — Cleanup-as-side-effect
Script che fa cleanup come side-effect di altre azioni (es. register che cleanup stale) è insidioso. Cleanup deve essere esplicito e safeguarded.

## 11. References

- Plugin upstream: https://github.com/PatilShreyas/claude-code-session-bridge
- Blog autore: https://blog.shreyaspatil.dev/session-bridge-i-made-two-claude-code-sessions-talk-to-each-other
- Skill VAL-side: `~/.claude/skills/bridge-val-esc/SKILL.md`
- Skill ESC-side: `~/.claude/skills/esc-bridge-mode/SKILL.md`
- Versione testata: 0.1.1
- Bash version target: ≥4.0 (per associative array support eventual)
- Dependencies: bash + jq (current). Fork può aggiungere socat/nc (Unix socket).

## 12. Suggested fork repo structure

```
ac-bridge/                          # Fork repo
├── README.md                       # Quickstart + features
├── LICENSE                         # MIT
├── CHANGELOG.md                    # v0.2.0+ history
├── plugin/
│   ├── plugin.json                 # Claude Code plugin manifest
│   ├── commands/
│   │   ├── listen.md
│   │   ├── stop.md
│   │   ├── ask.md
│   │   ├── peers.md
│   │   ├── status.md               # NEW
│   │   ├── logs.md                 # NEW
│   │   └── ...
│   └── scripts/
│       ├── bridge-config.sh        # NEW config loader
│       ├── bridge-transport.sh     # NEW transport abstraction
│       ├── register.sh             # FIXED BUG-5
│       ├── get-session-id.sh       # FIXED BUG-2
│       ├── cleanup.sh              # FIXED BUG-1
│       ├── bridge-listen.sh        # FIXED BUG-3
│       ├── bridge-receive.sh       # FIXED BUG-4
│       ├── send-message.sh         # NEW --file flag
│       ├── list-peers.sh           # FIXED inconsistency STALE
│       ├── connect-peer.sh         # FIXED STALE consistency
│       ├── status.sh               # NEW
│       ├── transcript.sh           # NEW
│       └── ...
├── tests/
│   ├── unit/                       # BATS tests
│   ├── integration/                # Multi-process tests
│   ├── regression/                 # BUG-1..BUG-5 repro
│   └── perf/                       # Benchmark
├── docs/
│   ├── architecture.md
│   ├── migration-from-v0.1.x.md
│   ├── multi-esc-patterns.md       # Multi-ESC best practices
│   └── troubleshooting.md
└── examples/
    ├── single-val-single-esc/
    ├── dual-esc-research-mode/     # F0.6 pattern G33 candidato
    └── triadic-architetto-val-esc/  # F0.4 pattern
```

## 13. Effort stima fork

| Fase | Settimane | Effort person-day |
|---|---|---|
| P0 critical bugs | 1-2 | 5-8 |
| P1 UX improvements | 3-4 | 8-12 |
| P2 architecture refactor | 5-8 | 15-20 |
| P3 advanced features | 9+ | 20+ |
| **Total v1.0.0 target** | **9-12** | **48-60 person-day** |

Con Claude AI assistance + Alan part-time supervision: **~3-4 settimane reali calendar** ottimistico (analogo speedup ratio 4-9x come Fase 1 ACTR).

## 14. Onestà finale

Plugin v0.1.1 di Patil è **ottimo MVP**: zero deps, MIT, semplice da capire. Per use case 1 VAL + 1 ESC funziona OK.

Le criticità identificate emergono quando si sale di complessità:
- Multi-ESC simultanei
- Long-run sessions (>1h)
- Sub-cwd setup (analisi/ + src/ disgiunte)
- Recovery da crash

Il fork non vuole essere "migliore in tutto" ma "robusto per use case avanzati". Backward compat preserva utenti current. Power user features sono opt-in.

**Stima ROI fork**: alto. Pattern triadico Architetto+VAL+ESC su long-run è core workflow AC Agency. Bridge robusto = velocity moltiplicata Fase 1+. Investimento 3-4 settimane si ripaga in 1-2 progetti enterprise.

**Mia raccomandazione strong**: GO fork. Inizia con P0 (bug fix critici, 1-2 settimane) per validare effort + verificare benefit. Se P0 va liscio, scaling P1+P2 successivi naturali.
