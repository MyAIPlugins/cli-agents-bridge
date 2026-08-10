# HANDOFF — VAL, 10 agosto: da v0.8 mergiata al pre-tag, secondo giorno

> Scritto a contesto quasi pieno. **Punto di ripresa**: questo, poi `ROADMAP.md` (F-104…F-116), poi `CLAUDE.md` (LL-18, LL-19 e i corollari aggiunti oggi).
> L'handoff di ieri — `docs/SESSION-2026-08-09-v0.8-handoff.md` — resta valido per l'arco precedente.

---

## Stato in una tabella

| | |
|---|---|
| `main` | **`fb15b17`**, pushato, working tree pulito |
| Binario in PATH e nel plugin | **`0.7.0-169-g720262a`**, stesso sha256 |
| Tag | **NON fatto** |
| CHANGELOG | **pronto**: la voce `[0.8.0]` ha la sezione sui due giorni fra merge e tag |
| Manifest | **`0.5.1`** in `plugin.json` e `marketplace.json` → da portare a `0.8.0` **col tag** |
| Agenti vivi (bridge) | VAL `679b7060` · ESC `b3e07991` · CRI `8b452bc7` (Codex, in `screen -S codexcribridge`) · CRI2 `ace5692b` |
| Agenti (payload) | VAL-payload `04a843bd` + ESC/CRI/CRI2/BRO-payload — squadra di Alan, altro repo |

---

## Cosa manca per il tag: **tre cose, due non mie**

1. **L'incrocio F-109 osservato sul campo** — vedi sotto, il criterio l'ho corretto.
2. **F-116** (cross-scope) in volo con ESC: decidere se entra nel tag o e' post-tag.
3. **Bump manifest a `0.8.0` + tag + GoReleaser** — venti minuti, miei, contestuali.

### Il criterio per il tag l'ho sbagliato una volta: non rifarlo

Avevo posto come condizione l'osservazione **spontanea** di un `requeued`. VAL-payload ha dimostrato con **70 messaggi** (30 `archived`, 36 `notified`, **0 `requeued`**) che nel flusso di un orchestratore la condizione e' **strutturalmente rara**: `ask` uno alla volta, `tell` per tutto il resto, e i `tell` non aprono ask. **Un criterio che puo' non verificarsi per settimane e' un rinvio travestito.**

La prova va **provocata**, e per un percorso raro e' l'unica possibile. La finestra: il secondo `ask` deve arrivare **dopo che la pagina di `next` e' stata emessa** (il `sent` del mittente lo dice: il primo passa a `notified`) e **prima della reply**. Il loro primo tentativo l'ha mancata perche' i due ask erano finiti nella **stessa pagina** — e chiuderli insieme li' **e' corretto**.

Tre cose da catturare, la terza e' quella che conta: *(1)* `sent` dopo il reply → primo `archived`, secondo **`requeued`**; *(2)* le righe sotto la risposta; *(3)* **che il secondo ask torni davvero**, `redelivered`, al `next` successivo.

---

## I finding di oggi, in ordine di quanto insegnano

**F-109 (chiuso, 3 commit)** — `reply` archiviava **tutti** gli ask aperti di un mittente, compresi quelli arrivati **mentre l'agente scriveva**. Un *"fermati, NON fare A"* risultava risposto da un *"fatto A come chiesto"*. Radice: **`NOTIFIED` significa "il processo `next` l'ha stampato", non "l'agente l'ha letto"** — e la regola del riarmo-prima-di-lavorare (mia) rende quella finestra normale invece che stretta. Ora chiude **una consegna** (gli id con lo stesso `notifiedAt`, che e' l'identita' della pagina); cio' che resta aperto **torna in coda** (`requeued`, ri-consegnato `redelivered`). **Limite dichiarato, non arrotondato**: senza read-ACK non e' impossibile per costruzione — la pagina piu' vecchia puo' essere quella non letta. Garantito: *al massimo una consegna per risposta*, *mai in silenzio*.

**F-110 (chiuso, breaking)** — due sessioni vive con lo stesso nome, perche' i tre guardrail guardano tutti **altrove** per costruzione e il caso "stesso path, stesso nome" era delegato a `findSessionHere`, dove il ruolo entrava nell'identita'. Ora: **una directory = una postazione**, il ruolo si **aggiorna** (`same session, same inbox`). *La mia diagnosi era sbagliata*: avevo citato `join.go:202` deducendo il comportamento **dalla firma** — il parametro `role` era **morto**. Una citazione esatta che punta al posto sbagliato compra fiducia senza pagarla.

**F-112 (chiuso)** — `peers` diceva `ok` di sessioni col listener morto. Ora colonna **`LISTENING`** (`yes`/`no`/`-`) da `Listening()`, la stessa funzione di `overview`: due comandi non possono piu' divergere perche' **non c'e' piu' un secondo modo di rispondere**. `STALE` **non** toccata — ha cinque lettori, e il quinto e' l'esenzione `orchestrating` (F-23a): fonderli marcherebbe abbandonato il VAL che fa un gate.

**F-113 (chiuso, P1)** — `reply <nome> < file` mandava **il nome** e buttava il file. **Complemento del nostro fix di F-105**: due letture mutuamente esclusive dello stesso input, sbagliate entrambe in direzioni opposte. **Il difetto non era quale sceglievamo, era sceglierne una in silenzio.** Ora un argomento **con** stdin rediretto viene rifiutato. *La mia nota tecnica (`ModeCharDevice`) era sbagliata*: nell'harness stdin e' un **socket**.

**F-114 (chiuso)** — `ask`/`tell` avvisano se nessun `next` ascolta da questa parte. Nessuna esenzione per `orchestrating`: **il VAL che ha prodotto il finding ero io, orchestrating, col waiter morto.** Avevo raccomandato l'esenzione, avevo torto.

**F-115 (chiuso)** — `inbox --tidy` archiviava **gli ask aperti** e **i messaggi mai mostrati**. Un `tell` non letto spariva **senza traccia da nessuna parte**. Ora un predicato solo: *si archivia cio' che e' stato mostrato e non e' un ask aperto*. **Terzo ramo, il piu' istruttivo**: il commento della funzione **dichiarava gia' la regola corretta** e non era mai stata vera — il difetto era **documentato come risolto**, ed e' per questo che nessuno lo cercava.

**F-116 (aperto)** — due VAL in repo diversi **non possono parlarsi**. v0.8 ha tolto gli id dal percorso caldo (giusto) e con essi l'unico modo di raggiungere un altro scope, senza che nessuno se ne accorgesse. Proposta mia: **nome qualificato** `VAL-x@progetto`, non un flag. Meta' piccola indipendente: `peers --all-scopes` deve **dichiarare** quali righe sono irraggiungibili.

**F-111 (metodo)** — un goal sperimentale senza attesa ha prodotto **~450 turni/ora** (uno ogni 8 s) e quasi esaurito la quota settimanale. Tre regimi misurati: 450/ora senza attesa, **12/ora** con fette da 5 min, **~1/ora** con fette da un'ora. Errore mio: non l'esperimento, ma **averlo lasciato acceso dopo che la risposta era arrivata**, e aver scritto lo stop come **nota in fondo a un messaggio lungo**.

---

## Gli agenti: cosa serve sapere di ognuno

**Il bridge e' identico per tutti; cambia solo il runtime, e con esso la porta.** Latenze misurate (dal file sul disco al riarmo del listener, cioe' **il modello**):

    Claude Code    risveglio nativo         zero costo a vuoto
    Codex          NESSUN risveglio         goal OBBLIGATORIO + attesa sul processo
                                            3m32s pollando · 5-9 s aspettando · ~1 turno/ora
    Antigravity    Reactive Wakeup nativo   3,1 s · zero costo · nessun goal
    Claude Desktop nessuna shell            irraggiungibile finche' non c'e' F-72 (MCP)

**Codex**: `background_terminal_max_timeout = 3600000` **impostato oggi** nel `config.toml` (default 300000, si carica all'avvio). Ma **alzare il tetto non basta**: l'agente deve *chiedere* di piu' — ottieni quello che chiedi, non quello che il tetto permette. Gira in `screen -S codexcribridge`, quindi la porta per `notify-watch` + iniezione e' aperta.

**Antigravity (BRO)**: skill in `~/.gemini/config/skills/`, versionate in `skills/antigravity/`. Playwright MCP configurato `--isolated --headless`. **La skill `browser-tester` e' stata ridimensionata**: dopo il primo lavoro reale (valore netto negativo — una misura sbagliata con sopra una matrice a tre ambienti) il mandato e' ora stretto, con **quattro regole di stop** e l'ordine esplicito di **non indagare**. Alan valutera' l'abbonamento sui prossimi compiti.

**Il VAL (io)**: nel mio harness il **waiter bloccante viene ucciso** ripetutamente — provato sei volte in due giorni. Uso un **monitor persistente** che sorveglia la inbox e poi lancio un `next` one-shot. Non e' un ripiego: e' la mia porta, come il goal per Codex. **Conseguenza da ricordare**: il mio pattern one-shot e' diverso da quello di chi tiene il waiter appeso, quindi quando ratifico un piano sul ciclo devo chiedermi se regge per entrambi — e' successo con F-109, dove il mio caso ha demolito una regola che sarebbe passata.

---

## Il metodo: cosa e' cambiato oggi in `CLAUDE.md`

Tredici istanze in due giorni della stessa classe — **il testo accanto al codice non ha modo di fallire, quindi l'unica cosa che lo tiene onesto e' qualcuno che lo ri-esegue**. Le aggiunte:

- **anche il markup e' un'affermazione**: un backtick che dice "questo si esegue" e' un claim;
- **la tredicesima variante, la peggiore**: il testo che descrive una **difesa inesistente** e per questo **impedisce di cercarla** (F-115). Corollario: quando un commento dichiara una garanzia, **quella e' la frase da verificare per prima**;
- **il criterio che rende sostenibile tutto** (ESC): *"misuro quando l'affermazione decide una forma del codice"*. Non "verifica tutto": verifica cio' su cui stai per costruire;
- **il rosso spiegato via**: un fallimento che sparisce al secondo tentativo dice *non deterministico*, non *ambiente*. Distinguere guardando se il processo **progrediva**;
- **un'azione da fare adesso non si mette in fondo a un messaggio lungo** (F-111, costo: una quota settimanale).

**Oggi ho sbagliato quattro volte in modo utile**: i "40 secondi" del gate (erano **10,6**, e su quel numero avevo chiesto un lock), `ModeCharDevice`, la diagnosi di F-110 dedotta da una firma, e il criterio del tag. **Tutte e quattro trovate da ESC o da VAL-payload.** La forma comune: *un'affermazione plausibile su cui qualcun altro stava per costruire*.

---

## Contesto operativo

- **Macchina**: 8 core logici (6 perf + 2 eff), carico a riposo gia' ~8 con gli agenti al lavoro. Il gate e' `-p 4` (misurato: `-p 8` 9.09s, `-p 4` 9.26s, `-p 2` 14.55s) e ora ha `-count=1`, che **mancava** — il target poteva rispondere dalla cache, che e' letteralmente LL-11.
- **Un lock per i gate e' stato PROPOSTO E ACCANTONATO**, con argomento di ESC migliore del mio: serializzare i gate serializza la cosa sbagliata, a saturare la macchina sono le **build** (minuti, non secondi). Se torna, serve la durata reale di una `next build` nel repo payload, e il lock va preso **dalle build**.
- **Recupero archivio (ieri)**: 13 sessioni cancellate, snapshot `com.apple.TimeMachine.2026-08-09-154921.local`. **Non tentato**, serve `sudo`. Valore solo forense.
