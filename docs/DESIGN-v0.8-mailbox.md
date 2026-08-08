# DESIGN v0.8 — Mailbox model

> Stato: **PROPOSTA VAL, da sottoporre a design-gate (CRI + CRI2)**
> Autore: VAL · Data: 2026-08-08 · Decisioni ratificate da Alan: rottura netta, ACK eliminati.

---

## 1. Diagnosi

Tre sintomi riportati da Alan dopo l'uso reale, tutti verificati sul codice.

### S1 — Il listener si dimentica di essere rilanciato

`listen` ha finestra di default **540s** (`cmd/cab-bridge/listen.go:49`), motivata in commento dal timeout subprocess di 10 minuti dell'harness Claude Code. `receive` ha `--max-deadline` a 1800s. Su una giornata di lavoro significa **~160 rilanci** (o 48). Ogni rilancio è un'occasione per dimenticarsene, e chi se ne dimentica perde i messaggi dalla vista.

Osservazione sul campo: ESC e CRI2 reggono, VAL no. Non è una differenza di modello — è che l'orchestratore ha più cose per la testa e più interruzioni.

### S2 — Gli ACK sono rumore che l'agente scambia per contenuto

`ask` invia `--type=query` di default (`ask.go:20`), quindi **ogni messaggio genera un auto-ack** (`send.go:125`). Da lì un'asimmetria:

- `listen` (entrambe le path, `accept=nil`) **consuma ed emette gli ack** all'agente come JSON pieni, indistinguibili da contenuto reale;
- `receive --any` e `scanForReply` li **saltano e li lasciano in inbox**, dove si accumulano — finché un `listen` non li sputa fuori tutti insieme.

L'agente si ferma a leggere ricevute di consegna credendole briefing.

### S3 — I comandi di lettura nascondono messaggi

`scanForReply` (`internal/transport/fs/receive.go:105`) ritorna al **primo** match di `inReplyTo`, lo archivia e basta. Tutto il resto arrivato nel frattempo resta in `inbox/` **senza alcun segnale**. Due reply allo stesso id → ne vede una. `listen --wait-one` drena il batch ed **esce**: ciò che arriva un secondo dopo è invisibile fino al rilancio successivo.

### Radice comune

**Il bridge accoppia WAKE e CONSUME.** Il processo che sveglia l'agente ha già mangiato i messaggi e glieli passa via stdout. Da questo accoppiamento discendono tutti e tre i sintomi:

- chi sveglia deve **scegliere** cosa consumare → S3 (nasconde il resto);
- consumare genera **ricevute** → S2 (ACK come messaggi);
- chi consuma va **fenced** contro il doppio consumo → tutta la macchineria B-2 (token, generazioni, reclaim), e la finestra corta per limitare il danno di un orfano → S1.

Un solo difetto architetturale, tre facce.

---

## 2. Il modello nuovo

**`inbox/` = non letto. `processed/` = letto. Sposta solo l'agente, mai un comando in automatico.**

Tre conseguenze diritte:

1. **Il wake non consuma.** Il waiter esce quando c'è qualcosa (deve: su Claude Code l'unico modo di svegliare un agente è che un processo in background termini), ma emette solo un **sommario** e non tocca un file. Rilanciarlo è sempre sicuro e idempotente.
2. **La lettura è totale.** Un comando solo mostra *tutto* il non letto. Nascondere messaggi diventa strutturalmente impossibile.
3. **Un waiter orfano è innocuo.** Non consuma, quindi non può far sparire nulla. Cade la ragione principale del fencing sul consumo.

### 2.1 Superficie comandi

| Oggi | v0.8 | Nota |
|---|---|---|
| `listen`, `listen --wait-one`, `receive --msg-id`, `receive --any`, `receive --unseen` | **`wait`** | quattro modi di aspettare → uno |
| `inbox --list` | **`inbox`** | mostra il non letto, contenuto intero |
| `inbox --tidy` | **`handled <id>` / `handled --all`** | l'unica cosa che sposta |
| `read <id>` | `read <id>` | invariato (lettura puntuale non distruttiva) |
| auto-ack, `--no-auto-ack` | *rimossi* | sostituiti da `sent` con stato |

**`wait`** — finestra **fissa 24h**, nessun flag di durata. Il valore vive in config (rispetta "no hardcoded"), ma **non è esposto alla CLI**: l'agente non può sceglierlo, quindi non può sbagliarlo né perderci tempo. Esce con exit 0 emettendo `{count, messages:[{id, from, fromAgentName, fromRole, type, timestamp, preview, fromScope}]}`. Su scadenza: `{status:"timeout", count:0}`.

**Rifiuto di partire con non letti pendenti.** Se al lancio ci sono già messaggi non letti, `wait` **non parte**: esce con codice dedicato e il messaggio "hai N non letti — leggili con `inbox` e chiudili con `handled` prima di rimetterti in ascolto". Questo elimina il busy-loop (un `wait` che esce subito perché il pending c'è ancora, all'infinito) e rende il protocollo deterministico: **`inbox` → `handled` → `wait`**, sempre lo stesso giro, insegnabile in una riga.

**`handled`** — nome scelto per non collidere con `state done` (F-23) e perché dice la semantica giusta: "l'ho preso in carico, non mostrarmelo più". Non "visto".

### 2.2 ACK: sostituiti da stato interrogabile

Eliminati `maybeAutoAck`, `autoAckTypes`, `--no-auto-ack`. Il tipo `ack` esce dall'enum in **scrittura**; in **lettura** resta tollerato (decoder lenient) per i file già su disco.

Al loro posto, `sent` guadagna una colonna **STATUS**, derivata da dove il messaggio si trova nella inbox del destinatario:

- `pending` → ancora in `inbox/` del destinatario (consegnato, non gestito)
- `handled` → in `processed/` (gestito)
- `gone` → non trovato

Il mittente **interroga** invece di ricevere. Zero traffico, zero rumore, e l'informazione è più ricca di prima (un ACK diceva "consegnato", questo dice anche "gestito").

Chi vuole dire "ricevuto, mi metto al lavoro" ha già `state working` (F-23).

### 2.3 Cross-repo VAL↔VAL

Oggi il flusso è: `peers --all-scopes` → leggere l'id → **trascriverlo** in `ask --to=<id>` → tenerlo a mente per ore. `ask --to` accetta solo un session-id validato (`ask.go:19`), non esiste risoluzione per nome. È l'unico id che un agente deve ricordare a lungo, ed è il primo che confabula dopo un compact (LL-13/LL-14).

In più il messaggio **non porta la provenienza** (`internal/message/schema.go`: `from`, `fromRole`, `fromAgentName`, ma nessuno scope): un VAL che legge un brief da `val-bi` non può sapere se viene dal proprio ESC o da un altro progetto. Con un repo solo è irrilevante; con la federazione è il rischio di agire sulle istruzioni del progetto sbagliato.

Proposta:

- **`link`** — contatto persistente su disco che punta a `(repo, ruolo)`, **non a un id**: `link add chatterence --repo=<path> --role=val`, poi `ask --to=@chatterence` per sempre. Risolto a runtime al peer vivo di quel repo, quindi sopravvive al compact **e** a una ri-registrazione del peer. L'id smette di essere una cosa da ricordare.
- **`fromScope`** nel messaggio, evidenziato in lettura quando è cross-scope. Schema bump (coerente con la rottura netta).
- **Federazione** via il `--team` già esistente (globale per teamId) invece di `--all-scopes`, se regge in design — meglio riusare una primitiva che aggiungerne una.

---

## 3. Cosa si semplifica

Il fencing B-2 (`internal/session/listener.go`, 181 righe, più `DrainInboxOnceOwned`/`PollInboxOwned`/`ownerOK` in `consumeInboxEntry` e i test di fencing) esiste perché due listener potrebbero consumare lo stesso messaggio. **Senza consumo automatico quel rischio sparisce.**

**Attenzione — non eliminare alla cieca.** L'ownership serve ancora per l'**heartbeat**: un orfano che continua a battere fa apparire viva una sessione morta. Quindi B-2 va **ridotto** (via il fencing sul consumo, resta l'ownership dell'heartbeat), non rimosso. *Quali garanzie esatte si perdono togliendo cosa* è la domanda numero uno per il design-gate.

---

## 4. Finding emersi oggi

- **F-90** — `register --resume` con un `--agent-name` diverso **crea una seconda sessione** sullo stesso projectPath invece di rinominare. Risultato immediato: hard-ambiguity che blocca *ogni* comando id-free. Il comando nato per il recovery post-compact (F-27) rompe la risoluzione id-free (LL-14) se l'agente si ripresenta con un nome anche solo leggermente diverso — cosa che un LLM fa con naturalezza. Riprodotto in sessione.
- **F-91** — Il guardrail shared-scope B-1 **non scala oltre 2 peer**: con 4 sessioni nello stesso repo stampa 6 righe di warning prima di *ogni* comando id-free, e consiglia `--session-id`, cioè spinge verso la trascrizione di id che LL-14 vuole eliminare. Il guardrail lavora contro il proprio scopo. Con la triade/quadriade come setup normale, va ripensato: il caso "più agenti nello stesso repo in worktree diversi" è la **norma**, non l'anomalia.
- **F-92** — `overview` risponde **"peer: (none paired in this scope yet)"** quando nello scope ci sono **tre** peer vivi e visibili in `peers`. È modellato sulla coppia VAL/ESC: con N>1 non sa quale mostrare e degrada a "nessuno" invece che a "eccone tre". Osservato dal vivo: CRI si è orientato con `overview` (che è *il* comando di orientamento, F-42), ha concluso di essere solo, e si è messo in attesa passiva mentre il suo brief era già in viaggio. Stesso difetto di S3 — nascondere informazione disponibile — applicato alla topologia.
- **F-89** (già noto, esteso) — `read <id> --session-id=X` fallisce: i flag devono precedere il positional. Vale anche per `state`.

### Tre prove raccolte sul campo durante la stesura di questo documento

Non cercate: emerse usando il bridge per coordinare il lavoro su se stesso.

1. **Gli ACK non sono solo rumore: bruciano il wake destinato al contenuto.** Il listener del VAL (`listen --wait-one`) si è svegliato ed è **uscito** per due ACK vuoti — `"ACK msg-...: received"`, zero contenuto — arrivati da ESC e CRI2. Costo: un ciclo di attenzione, i token della re-invocazione, e soprattutto **il VAL è rimasto fuori dall'ascolto senza saperlo**. Se la risposta vera fosse arrivata subito dopo, sarebbe caduta nel vuoto. Il sintomo S1 (il VAL perde messaggi) è dunque *causato* da S2 (gli ACK): con `--wait-one`, la prima ricevuta di consegna chiude la finestra aperta per il messaggio vero. Argomento decisivo per l'eliminazione.
2. **Dare la scelta della deadline produce scelte pessime.** CRI, lasciato libero, ha scelto `--max-deadline=120`: due minuti. Non per incapacità — semplicemente non ha modo di sapere quale sia il valore giusto, e nessun default lo guida. Conferma diretta della finestra fissa senza flag (§2.1).
3. **`register --resume` con un nome diverso duplica la sessione** (F-90): riprodotto su me stesso in trenta secondi, con blocco immediato di ogni comando id-free.

---

## 5. Piano

| Tier | Contenuto | Chi |
|---|---|---|
| **0** | Spike Codex app-server: push senza `screen`? Time-box 2h, esito documentato | CRI |
| **1** | Core mailbox: `wait` / `inbox` / `handled`, ACK rimossi, finestra fissa | ESC |
| **2** | Cross-repo: `link`, `fromScope`, provenienza in lettura | ESC |
| **3** | F-90/F-91, riduzione B-2, reminder non-letti, skill riallineate | ESC + VAL |

Nessuna migrazione dati: Alan conferma zero sessioni attive da oltre due giorni. `cleanup --scope=global` e si riparte puliti.

---

## 6. Domande per il gate

1. `wait` che **rifiuta di partire** con non letti pendenti: rete di sicurezza giusta, o rigidità che si ritorcerà contro in uno scenario che non ho previsto?
2. Riduzione B-2: quali garanzie si perdono esattamente, togliendo quale pezzo?
3. La finestra di 24h regge davvero sull'harness? (test empirico in corso, vedi §7)
4. `link` che risolve a `(repo, ruolo)`: cosa succede se in quel repo ci sono due peer con lo stesso ruolo?
5. Sto sostituendo cinque comandi con tre. È **semplificazione reale** o sto solo spostando la complessità dentro `wait`?

## 7. Verifiche empiriche

### Durata dei job in background — il limite di 10 minuti non si applica

Test lanciato alle 17:24:25 UTC: processo in background che marca un tick ogni 30s. **Alle 17:37:25 era al tick 27 — 13 minuti di vita continuata, ancora attivo.**

Quindi il timeout subprocess di 10 minuti citato in `listen.go:103` come motivazione della finestra a 540s **vale per i comandi in foreground, non per i job lanciati in background**. La premessa su cui è tarata la finestra corta è, per il caso d'uso reale (listener in background), **falsa**.

**Onestà sui limiti della prova**: 13 minuti dimostrano il superamento della soglia, non le 24 ore. Un test da 24h non è praticabile in sessione. Il rischio residuo però è basso e degrada in modo benigno: se il processo muore prima della scadenza, l'harness notifica comunque l'agente, che rilancia — cioè il comportamento di oggi, solo più raro. *(LL-12: dichiarato per quello che è, senza promuoverlo a garanzia.)*
