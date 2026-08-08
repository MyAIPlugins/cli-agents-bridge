# Spike Tier-0 v0.8 — Codex `app-server`: push nativo senza `screen -X stuff`

> **Arco**: v0.8
> **Data**: 2026-08-08
> **Time-box**: 2h hard (impiegate ~1h20)
> **Eseguito da**: ESC-bridge (worktree `esc-v08`)
> **Goal**: verificare empiricamente se Codex CLI 0.146 può essere svegliato da un processo esterno SENZA iniezione di tastiera nel terminale (`screen -X stuff`, workaround LL-16/F-66).
> **Decision tree**: SÌ → Codex diventa pushabile come Claude Code, cambia il modo in cui integriamo i peer Codex. NO → si documenta chiuso e si resta su `notify-watch`/F-66.

---

## 1. Verdetto

**SÌ — verificato empiricamente.** Un secondo client JSON-RPC, indipendente dalla TUI e su una connessione separata, può connettersi allo stesso `codex app-server`, fare `thread/resume` + `turn/start` sul thread che la TUI sta osservando, e **il messaggio compare nella TUI in tempo reale** — renderizzato come un normale messaggio utente, con il modello che elabora e risponde.

Nessuna iniezione di tastiera. Nessun `screen`. Nessuna modifica a Codex.

**MA il risultato ha un vincolo di setup non negoziabile** (§5.1): il peer Codex deve essere avviato come client remoto (`codex --remote ws://…`) contro un `app-server` in ascolto. Un Codex avviato normalmente **non è raggiungibile** — verificato, non dedotto (§4.5).

---

## 2. Setup riproducibile

Tre processi, come da metodo richiesto:

```bash
# (1) app-server — websocket, loopback only
codex app-server --listen ws://127.0.0.1:4500
#     -> listening on: ws://127.0.0.1:4500
#     -> readyz: http://127.0.0.1:4500/readyz | healthz: /healthz
#     -> note: binds localhost only (use SSH port-forwarding for remote access)

# (2) TUI come client remoto (richiede un PTY vero — vedi trappola T6)
codex --remote ws://127.0.0.1:4500 "Rispondi con una sola parola: PRONTO"

# (3) client iniettore — Node 24, WebSocket nativo, zero dipendenze
node cdx.mjs <holdMs> 'metodo' '<paramsJson>' ...
```

Il client di prova (`cdx.mjs`, ~60 righe) fa `initialize` → notifica `initialized` → esegue i metodi passati da argv, stampando ogni frame con timestamp. Copia in `scratchpad/cdx.mjs`; non è codice di produzione e non è stato committato.

Ambiente: macOS 26.6.1 arm64, `codex-cli 0.146.0`, auth ChatGPT, Node v24.15.0.

---

## 3. Superficie del protocollo (ground-truth, non doc)

Lo schema reale si estrae dal binario, senza fidarsi della documentazione:

```bash
codex app-server generate-json-schema --out <DIR>   # 41 file JSON Schema
```

**I nomi dei metodi sono lowercase** (`thread/inject_items`), NON PascalCase come i `title` dello schema (`ThreadInjectItemsParams`) — vedi trappola T5. Il modo più rapido per ottenere l'elenco autoritativo dei metodi è mandare un metodo inesistente: l'errore `-32600` li enumera tutti.

Metodi rilevanti per l'integrazione:

| Metodo | Ruolo nello scenario |
|---|---|
| `thread/loaded/list` | **scoperta id-free** del thread che la TUI sta osservando |
| `thread/resume` | rejoin di un thread *running* da un secondo client |
| `turn/start` | inietta un messaggio utente e avvia un turno |
| `turn/steer` | inietta in un turno attivo; richiede `expectedTurnId` |
| `thread/inject_items` | append alla history model-visible **senza** avviare un turno |
| `thread/read`, `thread/list` | lettura stato/storico |
| `turn/interrupt`, `thread/unsubscribe` | controllo ciclo di vita |

Il contratto di `thread/resume` dichiara esplicitamente il rejoin multi-client:

> *"If thread_id identifies a **running thread**, app-server **rejoins** that thread and treats a non-empty path as a consistency check against the active rollout path."*

Il campo `canAcceptDirectInput` del thread — che vale `true` sul thread osservato dalla TUI — è il segnale nativo di iniettabilità.

---

## 4. Evidenze

### 4.1 Il thread della TUI è scopribile dall'esterno (id-free)

Prima dell'avvio della TUI, `thread/loaded/list` → `{"data":[]}`. Dopo:

```
--> {"jsonrpc":"2.0","id":2,"method":"thread/loaded/list","params":{}}
<-- {"id":2,"result":{"data":["019fe27f-a64a-71d2-89f1-6dfd22270c15"],"nextCursor":null}}
```

Il client esterno **non deve conoscere né trascrivere** l'id a priori: lo ottiene dal server. Allineato al principio LL-13/F-39 (niente identificatori opachi da ricordare a mano).

### 4.2 `thread/resume` da un client indipendente funziona

Risposta (estratto): l'intera history del thread, `"status":{"type":"idle"}`, `"cliVersion":"0.146.0"`, `"source":"vscode"`, e:

```json
"canAcceptDirectInput": true,
"turns":[{"items":[
  {"type":"userMessage","content":[{"type":"text","text":"Rispondi con una sola parola: PRONTO"}]},
  {"type":"agentMessage","text":"PRONTO","phase":"final_answer"}
], "status":"completed","durationMs":4197}]
```

### 4.3 L'iniezione compare nella TUI — la prova centrale

Dopo `turn/start` da una connessione nuova, il log del PTY della TUI cresce di ~7.3 KB (era idle). Contenuto, ripulito dagli escape ANSI (la TUI renderizza carattere per carattere):

```
› MESSAGGIO ESTERNO DAL BRIDGE: rispondi con la sola parola SVEGLIATO
  ⠧⠇⠏⠋⠙⠹  (spinner: il modello elabora)
• SVEGLIATO
```

Il marker `›` è quello dei messaggi utente: dal punto di vista della TUI il messaggio è indistinguibile da uno digitato. Il turno è reale (il modello risponde), non un semplice append di testo.

### 4.4 Thread occupato: il messaggio viene ACCODATO, non perso né interrotto

Caso d'uso reale (il peer sta lavorando quando arriva il messaggio del bridge). Iniettato un turno lungo ("conta da 1 a 30"), poi un secondo `turn/start` a turno in corso:

```
<-- {"id":3,"result":{"turn":{"id":"019fe281-…","status":"inProgress"}}}   # accettato, non rifiutato
```

Nella TUI: il primo turno **arriva a termine** (…28, 29, 30), poi compare `› SECONDO MESSAGGIO durante turno attivo` e la risposta `• Ricevuto.`

Semantica ideale per il bridge: nessuna perdita, nessuna interruzione del lavoro in corso. (`turn/steer` esiste per il caso opposto — interrompere/dirigere il turno attivo — e richiede `expectedTurnId`.)

### 4.5 Un Codex NUDO non è raggiungibile — verificato, non dedotto

Durante tutto lo spike era vivo sulla macchina un Codex avviato normalmente (PID 41394, il CRI del progetto in `.worktrees/cri-v08`). **Non è mai comparso in `thread/loaded/list`**, che ha sempre mostrato solo il thread `--remote`. Il suo thread compare in `thread/list` (storico su disco) con `"status":{"type":"notLoaded"}`, cioè come rollout persistito, non come sessione live.

Conseguenza diretta: il push funziona **solo** con peer avviati come client remoti. Non è retrofittabile su una sessione Codex già in corso.

### 4.6 `thread/inject_items` — append senza turno

```
--> {"method":"thread/inject_items","params":{"threadId":"…","items":[{"type":"message","role":"user","content":[{"type":"input_text","text":"[bridge] contesto iniettato senza turno"}]}]}}
<-- {"id":2,"result":{}}
```

Accettato. Appende alla history model-visible senza far partire un turno: utile per iniettare contesto che il peer vedrà al turno successivo, **non** per svegliarlo.

---

## 5. Vincoli, rischi e trappole

### 5.1 Vincoli (determinano se e come integrare)

- **Il peer va avviato come client remoto** (`codex --remote ws://…`) contro un app-server attivo. Non è zero-config: cambia il modo in cui si lancia Codex (§4.5).
- **Il transport `ws://` è dichiarato dal vendor "experimental and unsupported"**, e l'intero comando `app-server` è marcato `[experimental]`. Costruirci sopra è un rischio di rottura ad ogni release di Codex.
- **`--listen ws://` binda solo loopback**; per uso remoto serve SSH port-forwarding, e per listener non-loopback c'è `--ws-auth {capability-token,signed-bearer-token}` (non testato).
- **Superficie di sicurezza ampia**: un client connesso all'app-server ha accesso a `fs/readFile`, `fs/writeFile`, `thread/shellCommand`, `command/exec` e a `thread/list`, che **espone tutti i thread dell'utente** (durante lo spike ho visto il thread del CRI di un altro worktree, con preview, cwd, branch e SHA git). Chi apre la porta 4500 apre molto più del wake.

### 5.2 Trappole incontrate (costo reale, ~35 min)

> **RETTIFICA (follow-up §9)**: T1 e T4 qui sotto sono **sbagliate** — corrette in §9.2. `unix://PATH` vuole il file socket (non la directory), e il transport unix funziona: parla WebSocket, non JSON-RPC raw. Lasciate qui per audit trail.

| # | Trappola | Sintomo | Causa/rimedio |
|---|---|---|---|
| T1 | ~~`unix://PATH` vuole una **directory**, non un file socket~~ **ERRATA, vedi §9.2** | `socket directory path exists and is not a directory` | il messaggio si riferisce al **parent**, che deve essere una dir reale |
| T2 | `/tmp` su macOS è un **symlink** a `/private/tmp` | stesso errore anche con dir valida | usare il path reale `/private/tmp/...` |
| T3 | `/private/tmp` è world-writable (sticky) | `Operation not permitted (os error 1)` | il bind non è bloccato dal sandbox (verificato con un bind Python riuscito nello stesso path): è un rifiuto di Codex. Il default `unix://` (in `~/.codex/`) parte correttamente |
| T4 | Il control socket **non parla JSON-RPC diretto** | connessione OK, zero risposta su NDJSON, `Content-Length` e CRLF; `app-server proxy` esce 0 senza output | canale di *management* (`daemon version` ci risponde correttamente), non l'endpoint client. ~~Il transport verificato funzionante è solo `ws://`~~ **SUPERATA: mancava l'handshake WebSocket, vedi §9.2** |
| T5 | Metodi **lowercase**, non PascalCase | `unknown variant 'Thread/loaded/list'` | l'errore `-32600` enumera tutti i metodi validi: usarlo come strumento di scoperta |
| T6 | La TUI muore se stdout non è un TTY | `Error: stdout is not a terminal` | niente `\| tee`; `script -q <log> codex …` funziona, `screen -dmS bash -lc '… codex'` no |

### 5.3 Rischio segnalato, non verificato

`thread/resume` su un thread `notLoaded` (rollout su disco di una sessione Codex viva ma non-remote) potrebbe caricare una **seconda istanza logica** sullo stesso file di rollout. Non l'ho testato — avrebbe richiesto di toccare il thread del CRI reale di Alan, fuori dal mandato dello spike. Da chiarire prima di qualunque integrazione che risolva thread per id da `thread/list`.

---

## 6. Cosa NON è stato verificato

- Usabilità quotidiana della TUI `--remote` da parte di un utente umano (l'ho pilotata headless in PTY; il rendering è corretto, l'interazione da tastiera non è stata provata).
- Tenuta su lunga durata: riconnessione del client, `thread/unsubscribe`, la finestra dei 30 min citata dalla doc, comportamento alla morte dell'app-server.
- Auth non-loopback (`--ws-auth`, `CODEX_REMOTE_TOKEN`) e `codex remote-control` (pairing con app first-party: `start`/`stop`/`pair`).
- Transport `unix://` come canale client (bloccato da T4 — non escluso, solo non raggiunto).

---

## 7. Implicazioni per il bridge (input al VAL, non decisione ESC)

Il fatto tecnico è netto: **Codex è pushabile senza `screen`**. Cosa farne è decisione VAL. Le opzioni che vedo, con l'onestà dei vincoli:

1. **Non buttare via `notify-watch`/F-66.** Copre il caso "Codex già avviato normalmente", che il path app-server **non** copre (§4.5). I due meccanismi sono complementari, non alternativi: `--remote` è migliore dove applicabile, `notify-watch` resta il fallback universale.
2. Il nuovo path si integra bene col principio LL-16 — *non aggiungere logica al bridge per compensare il runtime di un vendor, aggiungi una porta*: qui la porta esiste già ed è del vendor. Un eventuale comando bridge sarebbe un client JSON-RPC sottile, non logica nel core.
3. La scoperta id-free via `thread/loaded/list` è allineata a LL-13/F-39: nessun identificatore opaco da far trascrivere a un agente.
4. Il freno serio è la **stabilità del contratto** (`[experimental] and unsupported`) unita alla **superficie di sicurezza** (§5.1). Un'integrazione va progettata sapendo che può rompersi a una minor release di Codex e che la porta espone fs+exec, non solo il wake.

---

## 8. Pulizia

Processi e artefatti dello spike (app-server ws, TUI di test, thread `019fe27f…`, `/private/tmp/cdx-tui-test`, `/private/tmp/cdxs`) sono temporanei e non lasciano stato nel repo. Il thread di test è persistito in `~/.codex/sessions/2026/08/08/` come qualunque sessione Codex.

---

# 9. Follow-up — transport sicuro (unix socket): RIUSCITO

> **Data**: 2026-08-08 · **Time-box**: 2h (impiegate ~1h05)
> **Mandato**: il gate VAL ha posto un NO-GO su `ws://127.0.0.1:4500` (porta senza auth che espone `command/exec`, `fs/*`, `thread/list`). Obiettivo: un transport con lo stesso modello di sicurezza del bridge, oppure la dimostrazione documentata che non è ottenibile.

## 9.1 Esito

**Pista A riuscita.** Il push funziona su **unix socket con permessi 0600**, senza alcuna porta di rete. Il criterio di successo è soddisfatto.

```bash
mkdir -p ~/.codex/cab-spike-sock && chmod 700 ~/.codex/cab-spike-sock
codex app-server --listen unix:///Users/alan/.codex/cab-spike-sock/as.sock
codex --remote  unix:///Users/alan/.codex/cab-spike-sock/as.sock
```

Verifica end-to-end: da un client indipendente connesso direttamente al socket, `thread/loaded/list` → `thread/resume` → `turn/start`, e nella TUI compare

```
> PUSH VIA UNIX SOCKET: rispondi con la sola parola SICURO
  (spinner)
. SICURO
```

## 9.2 Perché il primo tentativo era fallito (due errori miei)

1. **Sintassi** — `unix://PATH` vuole il **file socket**, non la directory. Il messaggio `socket directory path exists and is not a directory: /tmp` si riferisce al **parent**: fallivo perché su macOS `/tmp` è un symlink a `/private/tmp`. La forma del mio primo tentativo era giusta, il path no. Passando a una dir reale in `$HOME` il socket nasce senza attriti. (Trappola T1 in §5.2 → **rettificata**.)
2. **Framing** — il transport unix **non parla JSON-RPC raw**: parla **WebSocket sopra unix socket**. Nessun framing testuale poteva funzionare, mancava l'handshake. Ho intercettato il client ufficiale con un proxy di logging e il traffico lo mostra senza ambiguità:

```
C->S  GET /rpc HTTP/1.1
      Connection: Upgrade
      Upgrade: websocket
      Sec-WebSocket-Version: 13
      Sec-WebSocket-Key: Q7U87L/XhuNzM6COu+jaWA==
S->C  HTTP/1.1 101 Switching Protocols
S->C  <frame> {"id":"initialize","result":{"userAgent":"codex-tui/0.146.0 …"}}
```

(Trappola T4 in §5.2 → **superata**. Il metodo che ha sbloccato: non indovinare il protocollo, intercettare il client ufficiale.)

Il client di verifica (`cdxws.mjs`, ~120 righe) implementa HTTP Upgrade + RFC 6455 (masking, frame testuali) su `net.Socket`, zero dipendenze.

## 9.3 Proprietà di sicurezza misurate

| Proprietà | `ws://127.0.0.1:4500` | `unix://…/as.sock` |
|---|---|---|
| Permessi | nessuno (chiunque si connette) | **`srw-------` = 0600** |
| Porta TCP aperta | sì, 4500 | **nessuna** (`lsof` sul processo: zero socket IP) |
| Raggiungibile da una pagina/estensione browser | **sì** (una WebSocket verso localhost è consentita) | **no** — nessuna API web apre AF_UNIX |
| Raggiungibile da altro UID | sì | no (permessi filesystem) |
| Dipende dall'umask dell'utente | — | **no**: con `umask 000` il socket nasce comunque 0600 |

Le ultime due righe sono il delta che conta. Il threat model del progetto è same-UID, quindi un processo malevolo che gira **come Alan** può comunque connettersi al socket — la parità col bridge è raggiunta (`drwx------`, SC-2), non superata. Ma la superficie *remota* sparisce: la porta TCP era attaccabile da JavaScript in una qualunque scheda del browser, lo unix socket no. Questo, più il fatto che i permessi non dipendano dall'umask, è quanto di meglio si ottiene senza modificare Codex.

Difesa in profondità: la dir `0700` non è necessaria (il socket è già 0600) ma non costa nulla.

## 9.4 Costo di implementazione per il bridge — da valutare

Il transport è WebSocket. Il bridge ha una policy **zero dipendenze runtime** (CLAUDE.md). Le opzioni:

- implementare in Go stdlib puro l'handshake + il framing RFC 6455 (subset: text frame, masking client, close). Il mio client Node è ~120 righe; in Go stimo 150-250 con i test. Fattibile, ma è codice di protocollo da mantenere;
- accettare `gorilla/websocket` → **viola la policy zero-deps**;
- delegare a `codex app-server proxy` come sidecar → da capire cosa faccia davvero (esce 0 muto, §5.2 T4), non l'ho chiarito.

Non è una decisione ESC: la porto al VAL come costo reale della Pista A.

## 9.5 Note metodologiche e limiti

- Durante il test la TUI passava da un proxy di logging mentre il client iniettore era connesso **direttamente** al socket: due connessioni distinte, quindi l'indipendenza dei client è dimostrata (il proxy è trasparente e non altera il risultato).
- **Pista B non è stata necessaria** e non l'ho esplorata: forzare `CODEX_REMOTE_TOKEN` su localhost era il ripiego per il caso in cui il socket non si chiudesse. Il socket si è chiuso.
- Non verificato: comportamento con socket stale dopo un crash del server (l'errore `ENOTSOCK` osservato suggerisce che un path occupato da non-socket viene rilevato, ma non ho testato il caso socket-orfano); tenuta su lunga durata; `--remote unix://` nudo senza path.
- La sessione del CRI reale non è stata toccata: TUI di prova dedicata, come da vincolo.
