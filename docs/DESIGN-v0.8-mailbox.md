# DESIGN v0.8 — KISS per AI

> Stato: **rev.3 — post design-gate primo giro (CRI + CRI2), in attesa del secondo giro**
> Autore: VAL · Data: 2026-08-08
> Ratificato da Alan: rottura netta · ACK eliminati · **"il non far pensare vince"** · `next` pure-read con cursore di wake

---

## 0. Il principio ordinatore

> *"Per usare il bridge l'ideale è che gli agent pensino il meno possibile: tutto predisposto su binari fissi. Se gli dai troppe opzioni, l'agent spreca token di thinking inutili."* — Alan

Regola che governa ogni scelta, con **precedenza su tutto il resto**: eleganza dell'API, ortogonalità, potenza espressiva. Un'opzione in più non costa solo superficie: costa **thinking a ogni ciclo**, su ogni agente, per sempre.

Tre criteri operativi:

1. **Se l'agente deve chiedersi "quale uso?", il design è sbagliato.** Un comando per situazione, con un nome che dice da solo quando usarlo.
2. **Se l'agente deve chiedersi "in che stato sono?", il design è sbagliato.** Il comando funziona uguale in ogni stato.
3. **Ogni flag sul percorso caldo è un difetto** finché non dimostra di essere indispensabile.

Conferma sul campo del criterio 3: lasciato libero di scegliere, CRI ha impostato `--max-deadline=120`. Due minuti. Non per incapacità — non aveva modo di sapere quale fosse il valore giusto, e nessun default lo guidava.

**Corollario emerso dal gate**: "non far pensare l'agente" non significa "meno stato nel tool". Significa **spostare lo stato dentro il tool**, dove è ispezionabile e testabile, invece di lasciarlo nella testa dell'agente, dove non lo è. La state machine di §2.3 aggiunge stato interno *proprio per* togliere pensiero all'agente.

---

## 1. Diagnosi

Tre sintomi riportati da Alan dopo l'uso reale, tutti verificati sul codice (e ri-verificati indipendentemente da CRI2).

### S1 — Il listener si dimentica di essere rilanciato

Finestra di default **540s** (`internal/config/config.go:122`, usata in `cmd/cab-bridge/listen.go:49`), motivata dal timeout subprocess di 10 minuti dell'harness. `receive` ha `--max-deadline` a 1800s. Su una giornata: **~160 rilanci** (o 48). Ogni rilancio è un'occasione per dimenticarsene.

### S2 — Gli ACK sono rumore che l'agente scambia per contenuto

`ask` invia `--type=query` di default (`ask.go:20`) → **ogni messaggio genera un auto-ack** (`send.go:96-126`). Da lì un'asimmetria: `listen` (accept=nil) **consuma ed emette gli ack** come JSON pieni; `receive --any` e `scanForReply` li **saltano e li lasciano in inbox**, dove si accumulano finché un `listen` non li sputa fuori tutti insieme.

### S3 — I comandi di lettura nascondono messaggi

`scanForReply` (`internal/transport/fs/receive.go:105-165`) ritorna al **primo** match, lo archivia e basta: il resto resta in `inbox/` **senza segnale**. `listen --wait-one` drena il batch ed **esce**.

### Radice comune

**Il bridge accoppia WAKE e CONSUME**, e **fa scegliere l'agente** a ogni passo. Dall'accoppiamento: chi sveglia deve *scegliere* cosa consumare (→ S3), consumare genera *ricevute* (→ S2), chi consuma va *fenced* e la finestra tenuta corta (→ S1). Dalla scelta: cinque modi di aspettare, tre di leggere, deadline configurabili — thinking sprecato.

**Prova che S1 e S2 sono lo stesso difetto**: il listener del VAL (`listen --wait-one`) si è svegliato ed è **uscito** per due ACK vuoti arrivati da ESC e CRI2. Il VAL è rimasto fuori dall'ascolto senza saperlo. Con `--wait-one`, la prima ricevuta di consegna chiude la finestra aperta per il messaggio vero.

---

## 2. Il modello

### 2.1 Due superfici separate

Oggi tutto è allo stesso livello: `migrate-from-patil` e `listen` sono due voci dello stesso help. L'agente vede venti comandi e deve capire quali lo riguardano.

- **Superficie LOOP** — quello che un agente usa lavorando. **Quattro comandi, zero flag.** È tutto ciò che deve sapere.
- **Superficie SERVIZIO** — ispezione, manutenzione, amministrazione. Esiste, è documentata, **non serve conoscerla** per lavorare.

### 2.2 La superficie LOOP

```
cab-bridge join            una volta, all'inizio
cab-bridge next            all'infinito: l'unico comando del ciclo
cab-bridge reply "..."     risponde a chi ha scritto per ultimo
cab-bridge tell <chi> "..."    scrive a qualcuno
```

**`join`** — registra, deriva ruolo e nome dal contesto, e **stampa chi c'è** (tutti i peer vivi nello scope, non "il peer"). Sostituisce `register`/`bootstrap`/`whoami`/`overview` iniziali. Idempotente: rilanciarlo dopo un compact riaggancia la stessa sessione. Su mismatch di nome con una sessione esistente stesso `(ruolo, scope, projectPath)` **si ferma e chiede**, mai crea in silenzio la seconda sessione che blocca tutto (chiude F-90).

**`next`** — l'unico comando del ciclo, **funziona uguale in ogni stato** e non rifiuta mai di partire:

- ci sono messaggi mai notificati → li stampa e ritorna subito;
- non ce n'è nessuno → aspetta fino a **24h**, poi stampa quelli arrivati;
- la finestra scade a vuoto → esce dicendo "niente, rilanciami".

Nessun flag: né durata, né formato, né filtro, né session-id (risolto da cwd).

**`reply` / `tell`** — due comandi invece di uno con destinatario opzionale, perché "opzionale" è una decisione. Nessun `--type` (si deduce), nessun `--in-reply-to` (`reply` lo mette da sé), nessun id da trascrivere.

**`tell` accetta il NOME dell'agente, non l'id** — `tell ESC-bridge "..."`, risolto in-scope, **fail-closed**: zero match → errore; più di un match vivo → errore con i candidati, mai una scelta silenziosa. Senza questo LL-14 resterebbe incompiuto proprio nel caso più frequente: `link` copre il cross-repo, ma l'id che si ricopia *ogni giorno* è quello del proprio ESC. (CRI2 P1-4, CRI F6.)

**Riga outbound nel sommario di `next`**: `outbound: 2 non ancora letti dai destinatari da >30m`. Ricuce a costo zero l'unica cosa che si perde eliminando gli ACK — prima la ricevuta ti *raggiungeva*, ora "ESC ha preso il brief?" richiederebbe di ricordarsi di lanciare `sent`, cioè disciplina, cioè la risorsa scarsa. (CRI2 P2-7.)

### 2.3 La state machine — il cuore del design

CRI ha individuato il difetto che né io né CRI2 avevamo visto: **mailbox e wake sono due assi distinti**, e finché l'eleggibilità al wake dipende da un'azione di consumo, il difetto originale è solo spostato.

Lo scenario che lo dimostra: arriva A, l'agente lo legge, ci lavora 40 minuti. Nel frattempo arriva B — una correzione, un annullamento, un urgente. Se A resta unread per garantire il recupero dopo crash, il wake non riparte e **B non sveglia nessuno**. Se l'agente archivia A per riarmarsi, un crash prima del risultato lascia A in `processed/`, fuori dalla coda azionabile: al resume nulla lo risveglia.

**Tre stati espliciti, persistenti, crash-safe:**

| Stato | Dove vive | Significato |
|---|---|---|
| `UNREAD` | file in `inbox/`, non nel cursore | arrivato, mai notificato |
| `WAKE-NOTIFIED` | file in `inbox/`, id nel cursore di wake | consegnato a un `next`, non ancora confermato |
| `ARCHIVED` | file in `processed/` | consegnato **e** confermato |

**Il cursore di wake è separato da inbox/processed** e persistito per message-id, sul modello di quello che `notify-watch` ha già (`notify_watch_state.go:20-42`, `notify_watch.go:240-318`): un id ci entra **solo dopo** che `next` ha emesso con successo.

**Transizioni:**

- `next` consegna gli `UNREAD` → li segna `WAKE-NOTIFIED`. **Non sposta nessun file: è pure-read.**
- `next`, al giro **successivo**, archivia i `WAKE-NOTIFIED` del giro precedente → `ARCHIVED`.

L'archiviazione avviene **con un giro di ritardo**, e la conferma è implicita: *il fatto stesso che l'agente stia richiamando `next` prova che il wake precedente è andato a segno*. È un ACK implicito, senza messaggi e senza nulla da ricordare.

**Cosa garantisce:**

- **Crash-safe.** Crash tra la consegna e il lavoro → i file sono ancora in `inbox/`, e il primo `next` al resume li ri-consegna. La perdita che la rev.2 accettava come "caso residuo raro" **sparisce**, invece di essere pagata.
- **Nessun re-wake infinito.** Il cursore, non l'archiviazione, impedisce di risvegliarsi sugli stessi messaggi. È per questo che `next` può permettersi di non spostare nulla.
- **Il wake non dipende dal consumo.** B sveglia l'agente anche se A è ancora in lavorazione — lo scenario di CRI è chiuso.
- **Zero pensiero.** L'agente conosce un comando solo e non marca niente.

**Invariante da enunciare nel package doc ed esercitare con un test dedicato:**

> Si segna `WAKE-NOTIFIED` solo ciò che si è emesso, e si emette sempre tutto ciò che è `UNREAD` (entro i limiti di §2.7, che sono dichiarati nell'output).
> Si archivia solo per id esplicito dal cursore, **mai** con una nuova scansione della directory.

La seconda riga chiude il P0 di CRI su `handled --all`: l'attuale `tidyInbox` prende un nuovo snapshot con `os.ReadDir` e muove tutto ciò che trova (`inbox.go:158-198`). Un messaggio arrivato tra la consegna e l'archiviazione verrebbe archiviato **senza essere mai stato mostrato**. Con l'archiviazione per id dal cursore, la race non esiste.

### 2.4 ACK: sostituiti da stato interrogabile — e onesto

Eliminati `maybeAutoAck`, `autoAckTypes`, `--no-auto-ack`. Il tipo `ack` esce dall'enum in **scrittura**; in **lettura** resta tollerato (decoder lenient) per i file già su disco.

Al loro posto `sent` deriva lo stato dalla mailbox del destinatario — ma **chiamando gli stati per ciò che provano davvero** (CRI F5): `processed/` prova che un comando ha spostato un file, **non** che il lavoro è finito.

| Stato | Significato preciso |
|---|---|
| `unread` | nella inbox del destinatario, mai notificato |
| `notified` | consegnato a un `next` del destinatario |
| `archived` | confermato dal destinatario |
| `unknown` | sessione destinataria assente |
| `expired` | archivio scaduto per retention |
| *errore* | un I/O failure è un errore, **mai** uno stato |

Nessuno di questi attesta il completamento del lavoro, e va documentato esplicitamente. Il `gone` della rev.2 collassava cinque cause diverse in una parola (CRI: cleanup archivia e rimuove l'intera sessione, `internal/cleanup/scope.go:154-200`; gli archivi scadono per retention, `:204-233`).

**Costo**: calcolare lo stato cercando da zero per ogni riga di outbox è O(sent × mailbox), perché i file in `processed/` hanno nome timestampato e l'id si trova decodificando (`read.go:75-111`). Serve un indice per destinatario costruito in **una** scansione.

### 2.5 Il gruppo, non la coppia

`overview`, il guardrail shared-scope e `bootstrap` sono tarati sulla **coppia** VAL/ESC. Con la quadriade — ormai il setup normale — degradano in due modi opposti: `overview` dice "nessun peer" con tre peer vivi (F-92), il guardrail stampa sei righe di warning e consiglia di trascrivere un id (F-91). **Nessuno dei due dice semplicemente chi c'è.**

Osservato dal vivo: CRI si è orientato con `overview`, ha letto "nessun peer", e si è messo in attesa passiva mentre il suo brief era già in viaggio.

**Il fix di F-91 è chirurgico** (CRI2 P1-3): il guardrail warna su `len(ScopeSiblings) > 0` (`common.go:194-208`), cioè sull'**esistenza** di altri agenti — non sulla **debolezza** della risoluzione. I due gradi di certezza sono diversi:

- **match esatto** (`cwd == projectPath`): la sessione è emessa da questo worktree; siblings altrove non la rendono meno certa. È il caso **normale**. Qui il warning è puro rumore.
- **match per prefisso**: qui l'agente potrebbe davvero essere "un altro", mai registrato — il caso dello stress-test LL-14. Warning giustificato.

Warnare solo su `match-per-prefisso AND siblings>0`, silenzio a match esatto. `HardAmbiguous` invariato. `LookupByCWDDetails` ha già `matchLen`: è una condizione, non un redesign. E la remediation va invertita — prima quella id-free ("lancia i comandi dal root del tuo worktree"), l'id come ultima spiaggia.

### 2.6 Cross-repo VAL↔VAL

Oggi: `peers --all-scopes` → leggere l'id → **trascriverlo** → ricordarlo per ore. `ask --to` accetta solo un session-id (`ask.go:19`). È l'unico id che un agente deve tenere a mente a lungo, ed è il primo che confabula dopo un compact (LL-13/LL-14). In più il messaggio **non porta la provenienza** (`schema.go`): chi legge un brief da `val-bi` non sa se viene dal proprio ESC o da un altro progetto.

- **`link add <alias> …`** — contatto persistente che non è un id. Ma `(repo, ruolo)` **non è un'identità** (CRI F6): con N>2 nello stesso repo la risoluzione "più recente" instraderebbe silenziosamente al peer sbagliato. Serve un endpoint logico registrato — `(canonicalScope, role, endpointName[, team])` con unicità imposta al register — e **fail-closed** su zero o più match.
- **`fromScope`** nel messaggio, evidenziato in lettura quando è cross-scope. È **provenance, non autenticazione**: derivato dal manifest del mittente al gateway, canonico, presentato come label informativa nel threat model same-UID.
- **`--team` non è un confine di sicurezza**: oggi è solo filtro di discovery (`peers.go:136-180`); chi conosce un session-id può inviare cross-team se i ruoli passano (`send.go:22-42`, `internal/routing/role.go:37-55`). Chiamarlo "federazione" sarebbe un over-claim finché `sendMessage` non verifica la policy.
- **Race send/cleanup** (già presente, più rilevante con link dinamici): `sendMessage` carica il manifest target, poi crea la inbox e scrive (`send.go:31-38,73-80`), mentre cleanup può archiviare e `RemoveAll` (`scope.go:174-200`). Se cleanup cade in mezzo, il send può ricreare una directory parziale senza manifest. Va serializzato col session lock, o rivalidare l'identità dopo la scrittura con rollback definito.

### 2.7 Limiti dell'output e file corrotti

`MaxMessageBytes` limita il singolo messaggio, non il **numero** (`internal/message/validate.go:43-46,100-102`). Un `next` che consegna tutto può produrre centinaia di MB e saturare stdout, tool capture e context (CRI F4).

- **Paging bounded**: ogni output dichiara `total`, `returned`, `hasMore`. Il modello esiste già — `notify-watch` limita gli id inline a 100 e segnala il troncamento (`notify_watch.go:375-406`).
- **Mai archiviare su una vista parziale.** Discende direttamente dall'invariante §2.3 (si archivia solo per id dal cursore).
- **File corrotti dichiarati, non saltati.** `collectInbox` oggi li salta in silenzio (`inbox.go:113-155`), mentre `notify-watch` li logga (`notify_watch.go:321-372`). Un JSON illeggibile non può né bloccare `next` per sempre né sparire: serve un `corruptCount` esplicito nell'output, nomi sicuri, e un percorso di quarantena.

---

## 3. B-2: cosa si toglie, cosa resta

La rev.1 sosteneva che senza consumo automatico il fencing diventasse superfluo. **Era sbagliato**, e CRI lo ha smontato: due waiter non consumano due volte, ma **svegliano due istanze** → due risposte, due archiviazioni, due effetti esterni. Dopo un `register --resume` l'orfano va ancora revocato. La singolarità del **wake**, non del consumo, è load-bearing.

**Si può togliere** (solo dopo che i consumer `listen`/`receive` sono davvero rimossi):
- `PollInboxOwned`, `DrainInboxOnceOwned`, e il parametro `ownerOK` di `consumeInboxEntry` (`drain.go:58-71`).

**Va conservato:**
- record separato token/generation, claim/reclaim sotto session lock, `IsListenerCurrent` (`listener.go:17-28,83-100,122-179`);
- il reclaim dentro `tryReuse`, che serializza revoke+adopt (`reconnect.go:55-86`);
- `StartHeartbeatOwned` con re-check sotto lo stesso lock — impedisce al vecchio processo di riscrivere PID/ListenUntil/heartbeat del nuovo (`manager.go:441-496`);
- il watcher che cancella il processo revocato (`listen.go:147-167`), **trasferito a `next`**. L'ownership diventa `WaitOwner`: non sparisce, cambia oggetto.

**Condizione di GO di CRI**: un solo waiter owner per sessione, revocabile da resume; prima di emettere il wake il waiter verifica la generation corrente, e l'output include una generation verificabile, così il completamento tardivo di un orfano non viene scambiato per autorizzazione a lavorare.

---

## 4. Finding

Nessuno cercato: tutti emersi **usando** il bridge per coordinare il lavoro su se stesso, o dal gate.

- **F-90** — `register --resume` con un `--agent-name` diverso **crea una seconda sessione** sullo stesso projectPath (identity match stretto: `internal/session/reconnect.go:12,25` → fall-through a register fresco) → hard-ambiguity che blocca ogni comando id-free. Riprodotto su me stesso in trenta secondi. Chiuso da `join` idempotente + stop-and-ask.
- **F-91** — Il guardrail shared-scope **non scala oltre 2 peer**: 6 righe di warning prima di *ogni* comando id-free, e consiglia `--session-id`, spingendo verso la trascrizione che LL-14 vuole eliminare. Il guardrail lavora contro il proprio scopo.
- **F-92** — `overview` risponde **"peer: (none paired in this scope yet)"** con **tre** peer vivi. Stesso difetto di S3 applicato alla topologia, sul comando dell'orientamento (F-42).
- **F-93** (nuovo, da CRI) — **Il fence sul consumo è più debole di quanto dichiarano commenti e test, e il test non prova ciò che afferma.** `ownerOK()` e `os.Rename` non sono nella stessa sezione critica (`drain.go:58-71`) mentre il reclaim gira sotto session lock: un reclaim può interleavare tra check e move. Il test "reclaim mid-sweep" cambia ownership **tra due entry**, non tra check e rename (`fencing_test.go:67-93`), quindi passa senza esercitare l'invariante. È LL-11/LL-12 nella stessa forma: verde su una premessa sbagliata. Il modello pure-read rimuove la race dalla wake path, ma il finding va registrato — vale per il codice di oggi e per chiunque lo legga come riferimento.
- **F-89** (noto, esteso) — `read <id> --session-id=X` fallisce: i flag devono precedere il positional. Vale anche per `state`.

---

## 5. Piano

| Tier | Contenuto | Chi |
|---|---|---|
| **0** | Spike Codex app-server: push senza `screen`? Time-box 2h | ESC |
| **1** | State machine §2.3 + superficie LOOP; ACK rimossi; invariante con test dedicato; `WaitOwner` (§3) | ESC |
| **2** | Gruppo non coppia: F-91/F-92, `who`; limiti e corrotti (§2.7); `sent` onesto (§2.4) | ESC |
| **3** | Cross-repo: endpoint logico, `fromScope`, race send/cleanup; matrice runtime (§7); skill riallineate | ESC + VAL |

Nessuna migrazione dati: zero sessioni attive da oltre due giorni (confermato da Alan). `cleanup --scope=global` e si riparte puliti.

---

## 6. Domande per il secondo giro di gate

1. **La state machine di §2.3 chiude davvero il Finding 1 di CRI?** In particolare: l'archiviazione a un giro di ritardo con conferma implicita regge, o c'è una sequenza in cui il cursore diverge dai file su disco?
2. **Il cursore di wake va nel manifest o in un file separato?** B-2 insegna che `manifestMu` serializza solo in-process (`manager.go:39`), quindi un file separato sotto session lock è probabilmente obbligatorio — ma è una conclusione mia, non verificata.
3. **`WaitOwner` e generation nell'output**: qual è il minimo che chiude il doppio-wake senza reintrodurre la complessità che stiamo togliendo?
4. **Paging (§2.7)**: qual è il limite giusto, e cosa succede quando `hasMore` è vero — l'agente deve *decidere* qualcosa? (Se sì, viola §0.)
5. **Cosa manca ancora** nella superficie LOOP a quattro comandi, che un agente scoprirà di volere al primo uso reale?

---

## 7. Verifiche empiriche

### Durata dei job in background — il limite di 10 minuti non si applica (test CHIUSO)

Dalle 17:24:25 alle 17:43:55 UTC, un tick ogni 30s: **40 tick su 40, terminato regolarmente — 19,5 minuti di vita continuata.**

Il timeout subprocess di 10 minuti citato in `listen.go:103` come motivazione della finestra a 540s **vale per il foreground, non per i job in background**. La premessa su cui è tarata la finestra corta è, per il caso d'uso reale, **falsa**.

**Onestà sui limiti** (CRI F7): 20 minuti dimostrano il superamento della soglia con margine, **non le 24 ore**, e soprattutto non dimostrano che chiusura della TUI, compact, restart, sleep/wake o teardown del terminale producano sempre un wake osservabile. Che la morte del background job non sia automaticamente benigna lo dice il repo stesso: `notify-watch` esiste perché è "immune to a peer's torn-down background terminal" (`notify_watch.go:64-72`).

**Matrice richiesta prima della release** — per ciascun vendor: arrivo · timeout · SIGTERM · SIGKILL · TUI chiusa · compact/resume · sleep/wake · waiter orfano revocato. Ogni fine prematura deve produrre un wake osservabile o un supervisor esterno che riarma.

**Il valore resta in config** (non esposto alla CLI dell'agente) per testabilità e override dell'operatore: "nessun flag per l'LLM" non deve diventare "nessun modo di collaudare o recuperare".

**Piano-B, deciso in anticipo**: se la finestra da 24h non regge, si scende al massimo sostenibile e `next` si rilancia da sé alla scadenza. S1 si attenua invece di sparire — accettabile, perché il rilancio non è più a carico dell'agente.

### Vincolo di runtime: `next` presuppone un harness con wake-on-exit

La finestra lunga è calibrata su Claude Code, dove la fine di un job in background genera una notifica. **Su un runtime senza push (Codex, LL-16) un bloccante da 24h in foreground è inutilizzabile**: lì il pattern resta `notify-watch` (F-66) con iniezione esterna. Va scritto, altrimenti qualcuno proverà a far girare `next` dentro Codex e concluderà che il bridge è rotto.

---

## 8. Esito design-gate — primo giro

Due critici, due assi, **quasi nessuna sovrapposizione**: CRI2 ha smontato l'ergonomia, CRI il protocollo. LL-15 confermata ancora una volta — e stavolta con un dato in più: il critico cross-vendor ha trovato il difetto strutturale (mailbox ≠ wake) che il critico della stessa famiglia non aveva visto, pur avendo letto lo stesso documento.

### CRI2 — lente ergonomia-agente

Ha verificato **tutti** i claim S1/S2/S3 sul codice prima di criticare (F-16), confermandoli e aggiungendo riferimenti che non avevo (`config.go:122`, `reconnect.go:12,25`, `common.go:194-208`).

Ha valutato la rev.1, e le sue due P1 più forti — il sommario con preview che crea *due porte di lettura*, e `handled --all` che reintroduce S3 — **sono ciò che la rev.2 ha eliminato per una via indipendente** (il principio §0 dettato da Alan). Due percorsi diversi, stessa conclusione.

Adottati: F-91 chirurgico (P1-3), `tell` by-name (P1-4), `join` stop-and-ask (P2-5), riga outbound (P2-7), `gone` disambiguato (P3), vincolo runtime no-push e piano-B (Q2).

**Il regalo che CRI2 ha visto e io no**: con un waiter che non richiede attenzione, il flusso *armare `next` in background → lavorare* diventa la norma per l'executor, e chiude **F-14** (l'ESC sordo durante l'implementazione) senza scrivere una riga in più. Da insegnare nella skill come flusso raccomandato: è un argomento a favore del modello, non un effetto collaterale.

### CRI — lente protocollo/lifecycle (cross-vendor)

**Verdetto: NO-GO alla specifica rev.1, GO alla direzione, con condizioni.** Sette finding, tre P0, ognuno con file:riga, più un `go test -race` di baseline (PASS, stato corrente).

- **P0-1** mailbox ≠ wake → **accolto in pieno**, ha riscritto §2.3 (ed è la ragione della rev.3).
- **P0-2** `handled --all` ricrea il messaggio nascosto → **accolto**: archiviazione solo per id dal cursore, mai nuova scansione.
- **P0-3** l'ownership serve ancora, ma sul **wake** → **accolto**, §3 riscritta sul suo elenco pezzo-per-pezzo.
- **P1-4** output non bounded, corrotti impliciti → **accolto**, §2.7 (nuova).
- **P1-5** `sent STATUS` sovrastima → **accolto**, §2.4 riscritta con stati onesti.
- **P1-6** `(repo,ruolo)` non è identità, `--team` non è authorization → **accolto**, §2.6.
- **P1-7** 24h non dimostrato → **accolto**, §7 con matrice per vendor.

E ha trovato **F-93**, un difetto nel codice di oggi che nessun gate precedente aveva colto.
