# DESIGN v0.8 — KISS per AI

> Stato: **rev.7 — post QUARTO giro. Entrambi i critici: chiusi questi punti, il brief può partire.**
> Autore: VAL · Data: 2026-08-08
> Ratificato da Alan: rottura netta · ACK eliminati · **"il non far pensare vince"** · opzione A (`reply` è la boundary) · una regola sola per payload brevi e lunghi

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

- **Superficie LOOP** — quello che un agente usa lavorando. **Cinque comandi, zero flag.** È tutto ciò che deve sapere.
- **Superficie SERVIZIO** — ispezione e manutenzione. Va **elencata** nel doc e nella skill, non solo promessa: per un LLM un comando non documentato non è "assente", è **da inventare** (LL-13), e l'agente che al primo intoppo non trova il comando fruga in `sessions/` a mano e confabula id. È la P2 che CRI2 indica come la più dannosa se lasciata fuori.

| Servizio | A cosa serve |
|---|---|
| `read <id>` | rileggere un messaggio, anche già archiviato (scansiona `inbox/` poi `processed/`) |
| `sent` | cosa ho mandato e in che stato è |
| `who` | chi c'è adesso — **esiste come comando**, non è "`join` rilanciabile": un agente a metà sessione che vuole solo guardare non deve invocare qualcosa che ha effetti di registrazione |
| `state` | **resta** (F-23, `working`/`done`/`orchestrating`): dice cosa sta *facendo* un agente, informazione ortogonale alla mailbox e che il modello nuovo non assorbe |
| `cleanup`, `inspect`, `migrate-*`, `notify-watch` | amministrazione, mai nel ciclo di lavoro |

### 2.2 La superficie LOOP

```
cab-bridge join              una volta, all'inizio
cab-bridge next              all'infinito: l'unico comando del ciclo
cab-bridge ask <chi> "..."   chiedo qualcosa — aspetta una risposta
cab-bridge tell <chi> "..."  informo — non aspetta risposta
cab-bridge reply "..."       rispondo a chi ha chiesto — chiude
```

**Cinque comandi, non quattro — e il quinto ripaga.** La rev.5 ne aveva quattro e liquidava il tipo del messaggio con "si deduce", senza dire come. CRI2 ha mostrato che **il tipo è load-bearing**: da esso dipendono cosa conta la riga outbound, cosa il `join` rimette in coda, cosa pota la retention. Dedurlo da `tell→query` avrebbe lasciato il **fire-and-forget senza un verbo**, e ogni `tell` non risposto sarebbe diventato un falso-pendente che inquina outbound e replay.

La distinzione vive nel **verbo**, non in un flag né in una deduzione: *sto chiedendo* o *sto informando* è una cosa che chi scrive sa già — è linguaggio naturale, non configurazione. Il criterio §0 chiede che l'agente non debba domandarsi *quale uso*; qui non se lo domanda, perché la risposta è già nella sua intenzione.

| Verbo | Tipo | Outbound | Replay al `join` | Chiude |
|---|---|---|---|---|
| `ask` | query | **sì**, finché non risposto | **sì**, resta azionabile | — |
| `tell` | notify | no | no, one-shot | — |
| `reply` | response | no | no | **sì**: archivia gli `ask` aperti di quel mittente |

Tre nodi sciolti da una scelta sola: la riga outbound conta solo ciò che aspetta davvero (niente assuefazione), il `join` rimette in coda solo ciò che è ancora azionabile (niente notifiche di tre giorni fa), e l'**aggiornamento di metà lavoro è un `tell`, non un `reply`** — quindi non chiude niente. Quest'ultimo punto chiude il P1 di CRI2 sul reply intermedio senza affidarlo a una frase da ricordare in una skill: il metodo VAL/ESC insegna gli aggiornamenti a checkpoint, e con verbi distinti l'agente comunicativo non archivia più per sbaglio un brief su cui sta ancora lavorando.

**`reply` archivia tutti gli `ask` aperti di quel mittente** (CRI2 P1-1b), elencandoli nell'echo: `→ VAL-bridge (chiusi: msg-A, msg-A2)`. Il caso quotidiano è brief + correzione coperti da una sola risposta; archiviarne uno solo lascerebbe l'altro `NOTIFIED` per sempre, falso-pendente. I `tell` non sono mai "aperti", quindi non entrano.

**Chiusura multipla — una risposta, una transazione, un set congelato** (CRI, quarto giro). Al **primo** tentativo `reply` fotografa **sotto lock** il set ordinato degli `ask` `NOTIFIED` aperti di quel mittente e persiste **un solo journal** con `closeIDs: [A, A2]` e **un solo response-id**. Ogni retry riusa quel set, anche se nel frattempo è arrivato A3 — che resta aperto e verrà chiuso dalla risposta successiva. Dopo `SENT` si archiviano **tutti e soli** i `closeIDs`; un recovery parziale riprende dall'indice senza rispedire.

Sul filo: lo schema ha un solo `inReplyTo`, quindi la response porta `inReplyTo` = il primo del set (l'`ask` più vecchio ancora aperto, cioè quello che ha originato il thread) più un campo **`closes: [...]`** con l'elenco completo. Senza questo, `(responder, inbound-id)` implicherebbe due identità mentre l'interfaccia ne promette una sola.

**Disambiguazione multi-mittente: `reply <chi>`.** L'ambiguità si valuta sui **mittenti con almeno un `ask` aperto**, non sui mittenti del batch: un batch con un `ask` di VAL e un `tell` di CRI **non è ambiguo** — c'è un solo `ask` da chiudere, e rifiutarlo sarebbe rumore. Quando invece due o più mittenti hanno `ask` aperti, `reply` nudo fallisce elencando i candidati e la forma esatta — `reply VAL-bridge "…"`. La rev.5 suggeriva `tell VAL-bridge` come ripiego, ma con i verbi tipizzati **`tell` è un notify e non chiude niente**: sarebbe stato un suggerimento che non fa ciò che promette. E `reply` nudo si ancora al mittente dell'ultimo **`ask` aperto** emesso, non all'ultimo messaggio qualsiasi — che potrebbe essere un `tell` di un terzo.

**`join`** — registra, deriva ruolo e nome dal contesto, e **stampa chi c'è** (tutti i peer vivi nello scope, non "il peer"). Sostituisce `register`/`bootstrap`/`whoami`/`overview` iniziali. Idempotente: rilanciarlo dopo un compact riaggancia la stessa sessione. Su mismatch di nome con una sessione esistente stesso `(ruolo, scope, projectPath)` **si ferma e chiede**, mai crea in silenzio la seconda sessione che blocca tutto (chiude F-90).

**`next`** — l'unico comando del ciclo, **funziona uguale in ogni stato** e non rifiuta mai di partire:

- ci sono messaggi mai notificati → li stampa e ritorna subito;
- non ce n'è nessuno → aspetta fino a **24h**, poi stampa quelli arrivati;
- la finestra scade a vuoto → esce dicendo "niente, rilanciami".

Nessun flag: né durata, né formato, né filtro, né session-id (risolto da cwd).

**`ask` / `tell` / `reply`** — tre verbi invece di uno con destinatario e tipo opzionali, perché "opzionale" è una decisione. Nessun `--type` (**lo dice il verbo**, vedi la tabella sopra: era "si deduce", che non specificava niente), nessun `--in-reply-to` (`reply` lo mette da sé), nessun id da trascrivere.

**`tell` accetta il NOME dell'agente, non l'id** — `tell ESC-bridge "..."`, risolto in-scope, **fail-closed**: zero match → errore; più di un match vivo → errore con i candidati, mai una scelta silenziosa. Senza questo LL-14 resterebbe incompiuto proprio nel caso più frequente: `link` copre il cross-repo, ma l'id che si ricopia *ogni giorno* è quello del proprio ESC. (CRI2 P1-4, CRI F6.)

**`reply` è ancorato all'ultimo `ask` APERTO emesso da un `next`** — non all'ultimo messaggio qualsiasi (che potrebbe essere un `tell` di un terzo: il reply finirebbe alla persona sbagliata), e mai a un `UNREAD` non ancora visto, perché lo stato del tool e quello nella testa dell'agente devono coincidere per definizione. **A un `tell` non si risponde con `reply`**: non c'è nulla da chiudere, si usa `tell` o `ask` di ritorno. E su un batch con **più mittenti** `reply` nudo è ambiguo → **fail-closed con i candidati** (`l'ultimo batch ha 2 mittenti — usa tell VAL-bridge / tell CRI-bridge`), mai una scelta silenziosa. `reply` stampa sempre a chi ha risposto: `→ VAL-bridge (in reply a msg-X)`.

Senza questo, nel gruppo `reply` è una roulette (CRI2 P1-2): il VAL manda il brief, CRI manda una nota mentre lavoro, e il report finisce a CRI. **Errore silenzioso** — il messaggio parte, nessuno segnala niente, il VAL non riceve mai nulla. Peggio di un id trascritto, perché non c'è nemmeno l'occasione di accorgersene.

**Payload di qualsiasi lunghezza — una regola sola** (CRI2 P1-3, CRI Q5, ratificata da Alan: *"una soluzione unica che gestisce messaggi brevi e lunghi senza far pensare all'agent che modalità deve scegliere"*).

> **Argomento presente → è il messaggio. Argomento assente → il messaggio è stdin.**

```
tell ESC-bridge "fatto, gate verde"      breve, naturale
tell ESC-bridge < brief.md               lungo, zero quoting
```

Nessun flag, nessuna modalità, nessuna scelta: l'agente scrive come gli viene naturale. Identica per `reply`.

**NON usare la tty detection** (`stdin non è un terminale → leggilo`), che sarebbe l'idioma Unix classico: **verificato empiricamente che nell'harness stdin NON è un tty** (`test -t 0` → false). Ogni `tell X "breve"` proverebbe a leggere una pipe vuota. La regola presenza/assenza dell'argomento è deterministica e immune all'ambiente. **Con l'argomento presente, stdin non viene letto affatto** — non si tenta di rilevare "entrambi presenti", perché per farlo bisognerebbe leggere stdin, cioè proprio la dipendenza ambientale che questa regola elimina. Senza argomento si legge fino a EOF; se anche stdin è vuoto, **rifiuto esplicito** ("messaggio vuoto"), mai un invio muto.

**Dove i messaggi vengono tagliati davvero** — misurato, non supposto: `ARG_MAX` è 1 MB (macOS), quindi i 14 KiB del verdetto di CRI negli argomenti ci stavano comodamente. Le cause reali sono il **quoting** (un backtick, un `$`, un apice dentro il testo rompono il comando e l'agente "aggiusta" perdendo pezzi) e il **costo di ri-emissione** (un agente che ha appena scritto un file con Write e deve ri-digitarlo in un argomento tende ad accorciarlo). Lo stdin le elimina entrambe.

**Oltre `MaxMessageBytes` (64 KB) si rifiuta esplicitamente**, dichiarando dimensione e tetto. Mai troncare in silenzio: un messaggio tagliato a metà è peggio di un messaggio non partito, perché sembra arrivato.

**Lato consegna — la metà che si dimentica**: un messaggio grande può essere troncato dal capture dell'harness, e lì il bridge non può intervenire… se non evitando di trovarsi in quella posizione. Il corpo **è già un file su disco** in `inbox/`. Quindi sopra una soglia `next` non emette il corpo, emette il **riferimento**:

```
msg-a1b2 da VAL-bridge — 47 KB
  corpo: <dataDir>/sessions/<id>/inbox/msg-a1b2.json
```

L'agente lo legge con `Read` — lo strumento che usa meglio di ogni altro, e che gli permette anche di leggerne una parte per volta. Zero duplicazione, troncamento impossibile, e nessuna scelta: è il comando a decidere quando il corpo sta inline e quando diventa un puntatore.

**Riga outbound nel sommario di `next`**: `outbound: 2 ask aperti da >30m (1 mai consegnato, 1 in attesa di risposta)`.

E il sommario porta la **riga simmetrica per il destinatario**: `aperti: 1 ask da VAL-bridge (3h)`. Senza, un `ask` visto ma non ancora risposto non ricompare mai dentro una vita dell'agente — il replay scatta solo al `join` — quindi il mittente lo vede e il destinatario no. Fuori dall'output è fuori dalla mente, che è la stessa ragione per cui esiste la riga outbound.

Conta **solo gli `ask`** — i `tell` sono fire-and-forget e non entrano, altrimenti il contatore crescerebbe all'infinito finché l'agente non impara a ignorarlo (assuefazione = morte del segnale, CRI2 P1-2). E distingue i due stati invece di collassarli: un `ask` già `NOTIFIED` ma senza risposta **non è "non consegnato"**, e descriverlo così sarebbe falso proprio nel testo che l'agente vede più spesso (CRI, quarto giro). Ricuce a costo zero l'unica cosa che si perde eliminando gli ACK — prima la ricevuta ti *raggiungeva*, ora "ESC ha preso il brief?" richiederebbe di ricordarsi di lanciare `sent`, cioè disciplina, cioè la risorsa scarsa. (CRI2 P2-7.)

### 2.3 La state machine — il cuore del design

CRI ha individuato il difetto che né io né CRI2 avevamo visto: **mailbox e wake sono due assi distinti**, e finché l'eleggibilità al wake dipende da un'azione di consumo, il difetto originale è solo spostato.

Lo scenario che lo dimostra: arriva A, l'agente lo legge, ci lavora 40 minuti. Nel frattempo arriva B — una correzione, un annullamento, un urgente. Se A resta unread per garantire il recupero dopo crash, il wake non riparte e **B non sveglia nessuno**. Se l'agente archivia A per riarmarsi, un crash prima del risultato lascia A in `processed/`, fuori dalla coda azionabile: al resume nulla lo risveglia.

**Tre stati espliciti, persistenti, crash-safe:**

| Stato | Dove vive | Significato |
|---|---|---|
| `UNREAD` | file in `inbox/`, non nel cursore | arrivato, mai notificato |
| `NOTIFIED` | file in `inbox/`, id nel cursore di wake | consegnato a un `next`, non ancora confermato |
| `ARCHIVED` | file in `processed/` | consegnato **e** confermato |

**Il cursore di wake è separato da inbox/processed** e persistito per message-id, sul modello di quello che `notify-watch` ha già (`notify_watch_state.go:20-42`, `notify_watch.go:240-318`): un id ci entra **solo dopo** che `next` ha emesso con successo.

**Transizioni — contratto normativo, nessuna altra è ammessa:**

| Comando | Transizione | Tocca i file? |
|---|---|---|
| `next` | `UNREAD` → `NOTIFIED` | **no, mai** — pure-read, scrive solo il cursore |
| `reply` | `NOTIFIED` → `ARCHIVED` **per tutti gli `ask` aperti del mittente a cui risponde** (il set `closeIDs`, §sotto) | sì, ed è l'unico |
| retention | `NOTIFIED` → `expired/unconfirmed` allo scadere del TTL — **solo `tell` e `response`, mai gli `ask`** | sì |

**Ordine obbligato in `next`: prima la stampa, poi il cursore.** Un crash tra le due produce una consegna doppia — innocua, il modello è at-least-once. L'ordine inverso produrrebbe una **perdita silenziosa**, e per un `tell` (one-shot per cursore) sarebbe definitiva e senza segnale.

**Corollario che la rev.7 aveva mancato** (CRI, diff-gate 1a): con la stampa prima del commit, **nessun singolo record JSON può dichiarare un esito che non è ancora accaduto**. Il primo record deve dire **`emitted`** — un fatto vero nel momento in cui viene scritto — e **mai** `delivered`/`confirmed`. L'esito del commit arriva come **record finale o contratto di uscita**: exit 0 solo a commit riuscito, errore tipizzato su eviction o fallimento del lock (in alternativa JSONL `page` + `commit`).

Senza questo, un reclaim nel varco fra stampa e commit produce uno stdout che dichiara `delivered` mentre il commit rifiuta — e per giunta con exit 0. È lo stesso difetto della conferma implicita (§8-bis), in una forma più piccola: **certificare come avvenuto qualcosa che deve ancora accadere**. La prescrizione "stampa prima" era giusta e resta; era il *contenuto* del record a promettere troppo.

### La matrice completa per tipo

Le quattro proprietà vanno lette insieme, non sezione per sezione — è la composizione che nessuno aveva verificato:

| Tipo | Outbound (mittente) | Replay al `join` | TTL in inbox viva | Chi lo chiude |
|---|---|---|---|---|
| `ask` (query) | **sì**, finché aperto | **sì** (`NOTIFIED`→`UNREAD`) | **nessuno** — resta azionabile | il `reply` del destinatario |
| `tell` (notify) | no | no | corto → `expired/unconfirmed` | il TTL |
| `response` | no | no | corto → `expired/unconfirmed` | il TTL |

**Nessun TTL sugli `ask`**: un `ask` di tre giorni senza risposta è la cosa **più importante da mostrare**, non da potare. L'accumulo è già osservabile dalla riga outbound del mittente, che è il posto giusto — chi ha chiesto è chi deve saperlo.

**`response` è one-shot come i `tell`**: nessun comando chiude una response (sarebbe una catena infinita). Senza questa riga un'implementazione conforme poteva trattarla come query, e il VAL si sarebbe visto **ri-consegnare i vecchi report a ogni `join`**.

**`next` non archivia nulla, in nessuna circostanza.** Questa riga è il contratto: due implementazioni non possono essere entrambe conformi.

### Perché `reply` e non il `next` successivo

La rev.3 faceva archiviare a `next(N+1)` il batch di `next(N)`, prendendo il richiamo come conferma implicita. **Sbagliato per due tracce indipendenti**, trovate da CRI2 e CRI:

- **Traccia A — attraverso le vite.** `next` emette A ed esce (cursore `{A}`); la notifica si perde in un compact o un restart; al resume l'agente esegue `join` → `next`, e quel `next` archivia A **come confermato** senza averlo mai mostrato. Il mittente che interroga `sent` legge `archived`: **il sistema certifica una consegna mai avvenuta** — un messaggio nascosto *con la ricevuta di lettura*. Richiamare `next` non prova che l'agente ha visto qualcosa, prova che sta eseguendo il protocollo.
- **Traccia B — dentro una vita, nel flusso raccomandato.** L'agente riceve A, inizia un lavoro lungo e arma subito `next(N+1)` in background per restare svegliabile su B — *il flusso anti-F-14*. `next(N+1)` archivia A all'avvio, mentre A è in lavorazione; un crash lascia A in `processed/`, fuori dalla coda azionabile. Nessun riavvio: il sistema fa esattamente ciò per cui era progettato.

E il precedente che avevo citato non autorizzava il salto: `notify-watch` marca che **l'hook di wake è riuscito** (`notify_watch.go:296-318`), non archivia il messaggio.

Con `reply` come boundary entrambe cadono: `next(N+1)` non tocca niente, quindi A resta in `inbox/` finché non c'è una risposta, e qualunque crash lo lascia ri-consegnabile. **E la conferma non è un rito**: un agente che risponde ha lavorato, quindi la garanzia si aggancia a un gesto che compie comunque (§0).

### La transazione di `reply` — recupero idempotente, non falsa atomicità

`reply` fa **due** mutazioni su directory diverse (la inbox del destinatario, la propria `inbox/`→`processed/`) e **non esiste transazione filesystem comune**: `atomic.go:38-88` garantisce il singolo rename, non la coppia. L'ordine è obbligato — **send, poi archive**: l'inverso perderebbe la query azionabile se il send fallisse.

Resta il crash gap: risposta consegnata, originale ancora `NOTIFIED`, e un retry oggi **genera un msg-id nuovo** (`send.go:44-47`) → risposta duplicata. Il contratto deve quindi garantire **exactly-once logico**:

1. sotto il lock della sessione locale, **fotografare il set** degli `ask` `NOTIFIED` aperti di quel mittente e scrivere **un solo** journal `reply-txn` con `closeIDs: [A, A2, …]` ordinati e **un solo** response-id. Un `ask` che arriva dopo lo scatto resta aperto: lo chiuderà la risposta successiva;
2. **idempotency key deterministica** ancorata a `(sessione responder, anchor-id)` dove *anchor* è il primo dei `closeIDs` — non un id casuale a ogni tentativo (`send.go:44-47`). **Attenzione a quale garanzia viene da cosa** (precisazione di ESC in fase 1b, la mia formulazione le confondeva): nel percorso di retry normale l'idempotenza poggia sulla **persistenza del journal** — il retry rilegge l'id da lì, e sostituire la chiave con un id casuale non rompe quel caso. Il determinismo copre un caso **diverso**: journal perso ma risposta già consegnata. Servono entrambi, e vanno testati separatamente, perché un test solo lascia passare una mutazione dell'altro;
3. consegna **create-if-absent**, oppure accettare un file esistente solo se byte-identico (il rename sovrascrivente non basta);
4. persistere `SENT` → spostare in `processed/` **tutti e soli i `closeIDs`**, uno alla volta, aggiornando l'indice di avanzamento → `ARCHIVED` → rimuovere il journal;
5. al retry, riprendere dal journal: se `SENT`, **completare le archiviazioni mancanti senza rispedire**, riprendendo dall'indice. Un crash tra l'archiviazione di A e quella di A2 ha così una semantica definita — prima non ce l'aveva.

Sul filo dello schema: `inReplyTo` è singolo, quindi porta l'*anchor*, e l'elenco completo va in un campo **`closes: [...]`**. Senza, la chiave implicherebbe un'identità per inbound mentre l'interfaccia ne promette una sola per risposta.

`--skip-duplicate` non è la soluzione (`ask.go:83-102`): è opzionale, dipende da contenuto e finestra temporale, e si appoggia a un outbox best-effort — non identifica la transazione inbound.

Non tenere due session-lock insieme: serve un ordine globale dei lock oppure consegna idempotente più cleanup coordinato. **Anche `cleanup` deve rispettare lo stesso fencing**, altrimenti può rimuovere una sessione mentre `reply` sta operando (`scope.go:174-200`).

### Redelivery: dipende dal tipo, e il `join`-reset globale è eliminato

Un reset indiscriminato al `join` risveglierebbe una notifica di tre giorni prima, non più azionabile. La policy è per tipo:

- **query** (`ask` — richiede risposta): redelivery **at-least-once fino a `reply`**, anche attraverso una nuova incarnation. Se resta azionabile per sempre è una scelta onesta, ma va dichiarata con backpressure e osservabilità.
- **notify / event / ping** (`tell` — non richiede risposta): **one-shot per cursore**, nessun reset su un `join` normale. Restano osservabili come `NOTIFIED`, non risvegliano più.
- **response** (`reply`): **one-shot come i notify**. Nessun comando chiude una response — sarebbe una catena infinita — quindi segue la stessa policy: nessun replay, e il TTL la porta a `expired/unconfirmed`. Andava assegnata esplicitamente: senza, restava l'unico tipo senza lifecycle dichiarato (CRI, quarto giro).
- **TTL esplicito anche per la inbox viva**, basato su un `notifiedAt` **locale** — mai sul timestamp del mittente, che non è fidato. Allo scadere il messaggio va in archivio come `expired/unconfirmed`, **mai** come `confirmed`. La retention attuale non copre questo caso: pota solo directory datate sotto `archive/` (`scope.go:204-233`), quindi una sessione viva accumulerebbe `NOTIFIED` all'infinito.

L'incarnation resta utile per audit e fencing, ma **non è da sola una policy di redelivery**.

**Ri-consegna**: l'agente non deve *decidere* se è un duplicato — la distinzione dev'essere irrilevante (at-least-once con idempotenza a valle). Il marcatore è testo umano nel sommario, non un campo da interpretare: `(ri-consegnato: già consegnato prima di un riavvio — trattalo normalmente)`. Un `redelivered: true` nudo sarebbe pensiero in più.

### Invariante da enunciare nel package doc ed esercitare con un test dedicato

> Si segna `NOTIFIED` solo ciò che si è emesso, e si emette sempre tutto ciò che è `UNREAD` (entro i limiti di §2.7, dichiarati nell'output).
> Il solo comando che sposta file è `reply`, e sposta **esattamente e solamente** i `closeIDs` congelati nel proprio journal — **mai** per scansione della directory, **mai** un id sopraggiunto dopo lo scatto.

La seconda riga chiude anche il P0 del primo giro su `handled --all`: `tidyInbox` prende un nuovo snapshot con `os.ReadDir` e muove tutto ciò che trova (`inbox.go:158-198`), quindi un messaggio arrivato tra la consegna e l'archiviazione sparirebbe **senza essere mai stato mostrato**. Con lo spostamento per id esatto, la race non esiste.

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
- **Il paging si consuma con lo stesso richiamo, e va scritto nel payload**: `hasMore: true — i prossimi arrivano col prossimo next`. Il `next` successivo ritorna **subito** con la pagina rimanente senza entrare nella finestra di 24h, e marca `NOTIFIED` **solo gli id effettivamente emessi**. Nessun ramo da decidere: il loop è sempre lo stesso. Senza quella frase un agente cercherebbe un `--page` che non esiste — thinking sprecato, difetto §0. Principio generale: **l'output dichiara la propria azione successiva**, come già fa il timeout con "niente, rilanciami".
- **Due limiti, non uno**: `maxPageMessages` **e** `maxSerializedBytes` — il solo conteggio non basta con messaggi fino a 64 KB. Il valore in byte va tarato sul limite reale di capture dei vendor, non su un numero magico. Un singolo messaggio oltre il budget produce una pagina da un elemento con `oversize: true`, mai starvation.
- **Ordinamento deterministico**: per timestamp decodificato, con l'id come tie-break. `os.ReadDir` restituisce ordine lessicale sui nomi `msg-<random>`, che **non è ordine di arrivo**. Timestamp invalido → policy dichiarata (corrotto, non starvation).
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
- **F-94** (osservato sul campo durante il gate) — **Il messaggio consumato-ma-non-ancora-consegnato è invisibile a ogni comando di ispezione.** CRI, con un `listen --until-deadline=6000s` attivo in background, ha lanciato `overview` e letto **"inbox: empty"**, concludendo di non avere messaggi. Il messaggio c'era: il listener lo aveva già spostato in `processed/` e lo teneva nel proprio stdout, in un processo che non sarebbe uscito per 100 minuti. Il messaggio era in un limbo — non più in `inbox/`, non ancora nelle mani dell'agente — e in quella finestra **ogni comando di ispezione mente**, proprio all'agente che sta verificando se ha lavoro.

  È S3 aggravato: S3 nasconde *gli altri* messaggi, questo nasconde **il** messaggio, al comando dell'orientamento. E su un runtime senza push si somma il fatto che il percorso di default di `listen` **non esce** dopo la consegna (`listen.go:232-263`): su Claude Code il processo termina e notifica, su Codex resta vivo col messaggio nel buffer e nessuno avvisa. CRI l'ha trovato solo perché Alan gli ha detto di andare a guardare il terminale.

  **Chiuso dalla rev.3 su entrambe le facce**: `next` è pure-read, quindi il messaggio resta in `inbox/` fino alla conferma e `overview` dice il vero; e `next` ritorna appena ha qualcosa, quindi non esiste un buffer parcheggiato in un processo vivo. Validazione empirica del modello, arrivata per caso mentre era in gate.
- **F-95** (osservato sul VAL stesso) — **Non esiste un modo di essere vivi E non svegliati dagli ACK: le due capability sono disgiunte.** La sessione VAL risultava `STALE` (heartbeat 30 min) mentre era viva e in ascolto da 7 minuti, perché `receive --any` **non fa AdoptPID né heartbeat** — documentato come "one-shot wake, not a long-running listener" (`receive.go:182-183`), ma con `--max-deadline=10800` è di fatto un listener long-running.

  | | heartbeat | ACK |
  |---|---|---|
  | `listen` | lo mantiene | **ti sveglia** (li consuma ed emette come contenuto) |
  | `receive --any` | **non lo mantiene** | li ignora |

  Chi vuole entrambe le cose deve scegliere quale difetto subire. Non è che sia stato scelto lo strumento sbagliato: **non c'è uno strumento giusto.** In v0.8 `next` deve fare entrambe — e non è un requisito aggiuntivo, è la ragione per cui i due comandi vanno fusi in uno.

  Mitigazione col tool attuale: `state orchestrating`, esente dal controllo di staleness (F-23a). Funziona, ma è un workaround che un agente deve *sapere* — cioè pensiero, cioè §0 violato.
- **F-96** — **Falso-negativo di consegna: un `ask` riuscito sembra fallito.** L'esito di un invio andato a buon fine è visivamente sommerso — il warning shared-scope B-1 multi-riga (F-91) più `replying_to` (A-4, su stderr per design), mentre lo stdout col msg-id è una riga sola in mezzo. Il VAL ha **dichiarato rotto F-39** su questa base, e CRI2 ha dimostrato con la ground-truth su disco che risoluzione e consegna erano entrambe corrette. È il **duale di F-24**: là un timeout sembrava un fallimento, qui un successo sembra un errore. Requisito v0.8: **l'esito di un send riuscito dev'essere inequivocabile a colpo d'occhio**, e la conferma non può vivere sommersa da avvisi su stderr.

  Corollario di metodo: **F-16 vale anche per il VAL.** Nello stesso resoconto avevo scritto "12 response" (erano 15) e "9 ACK" (erano 11) — il disco vince sul resoconto anche quando il resoconto è di chi tiene il gate.
- **F-97** — **Gli errori dei comandi id-free non dichiarano chi credevano di essere.** Il VAL ha eseguito `ask --in-reply-to=last` con la cwd rimasta dentro `.worktrees/esc-v08` (il `cd` del gate persiste tra le chiamate). Il lookup-by-cwd ha risolto — con **match esatto**, senza alcun warning — **la sessione di ESC**, e `lastReceivedFrom(sessions/b3e07991, peer=b3e07991)` ha cercato messaggi di ESC nella inbox di ESC: zero per costruzione, nessuno scrive a se stesso. Errore: *"no message received from b3e07991"*.

  L'errore (`ask.go:142`) **non dice da quale sessione stava risolvendo**. Se avesse detto *"in session b3e07991 (ESC-bridge)"*, l'assurdo sarebbe stato autoevidente — chiedo i messaggi di ESC dalla sessione di ESC — e la causa sarebbe emersa in un secondo. Invece ha mandato a sospettare del resolver, con un falso allarme propagato a ESC.

  **Regola v0.8: ogni errore di un comando id-free dichiara chi credeva di essere** (`in session <sid> (<agent-name>)`). Il payload di `next` lo fa già col campo `session`; va esteso agli **errori**. È la rete per l'intera classe *"un agente nella cwd sbagliata diventa qualcun altro in silenzio"*.

  **Interazione col fix di F-91**: qui la cwd produceva un match **esatto**, cioè proprio il caso che il fix renderà silenzioso. È giusto così — il warning non ha salvato nessuno, era già rumore assuefatto — ma significa che la difesa per questo scenario **non è il guardrail**: è l'errore che dichiara il punto di vista, più il `session` sempre presente nei payload.

  Nota: è esattamente il rischio che il VAL aveva *previsto* discutendo l'ipotesi di far girare i CRI da cartelle diverse dello stesso repo — e in cui è caduto lui stesso, al contrario, mezz'ora dopo.
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

## 6. Stato delle domande di gate

Le domande dei giri 1-3 sono chiuse: le risposte sono confluite nel contratto §2 e il percorso è tracciato nelle sezioni storiche §8-§9. Quelle del secondo giro — che chiedevano se reggesse l'archiviazione a un giro di ritardo con conferma implicita — riguardano un meccanismo **eliminato dalla rev.6**, e sono state rimosse per non lasciarle leggere come requisiti.

Nessuna domanda aperta al quarto giro: entrambi i critici hanno dichiarato che, chiusi i punti di specifica recepiti qui, il brief di implementazione può partire.

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

## 8. STORIA — esito design-gate, primo giro

> ⚠️ **Sezione narrativa, NON normativa.** Il contratto è §2.

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

---

## 8-bis. STORIA — il trilemma dell'archiviazione

> ⚠️ **Sezione narrativa, NON normativa.** Racconta come si è arrivati alla decisione. Il contratto è §2.3 e **solo** §2.3: dove questa sezione dice "atomicamente", "nessun quinto verbo" o descrive il `join`-reset e la conferma implicita, sta riportando stati superati del ragionamento. In caso di conflitto vince §2.3.

CRI ha trovato lo stesso P0 di CRI2, indipendentemente, **più una seconda traccia che il fix `join`-reset non copre**:

1. `next(N)` consegna A, l'agente inizia un lavoro lungo;
2. per restare svegliabile su B, arma subito `next(N+1)` in background — **il flusso anti-F-14 che CRI2 aveva lodato come "il regalo"**;
3. `next(N+1)` archivia A **all'avvio**, mentre A è ancora in lavorazione;
4. crash durante A → A è in `processed/`, fuori dalla coda azionabile.

Nessun riavvio: il sistema sta funzionando esattamente come progettato. **Il regalo e il difetto sono la stessa cosa vista da due lati.**

CRI smonta anche l'argomento con cui avevo giustificato il design: `notify-watch`, citato come precedente, marca solo che **l'hook di wake è riuscito** (`notify_watch.go:296-318`) — non usa quel marker per archiviare il messaggio. Il salto l'ho aggiunto io, e il precedente non lo autorizzava.

**Il trilemma** — non si possono avere insieme:

1. `next` si riarma mentre il task precedente è in corso;
2. nessuna azione segnala che il task è durabilmente chiuso;
3. il messaggio è archiviato in modo crash-safe.

**Opzione A (raccomandata dal VAL) — `reply` è la boundary.** `reply` archivia atomicamente il messaggio a cui risponde: un agente che risponde *ha lavorato*, quindi la conferma è l'effetto collaterale di un'azione che compie comunque — zero pensiero aggiuntivo, cioè §0 rispettato. `next` non archivia mai, quindi la traccia B è chiusa; e un crash durante A lascia A in `inbox/`, quindi il primo `next` lo ri-consegna — chiusa anche la traccia A. I messaggi che non generano risposta (notify, event) **restano `notified` e non mentono**: `sent` dice "consegnato, non confermato", che è vero, e la retention li pota dopo N giorni senza fingere che siano stati gestiti. Nessun quinto verbo.

**Opzione B — at-most-once dichiarato.** Si mantiene l'archiviazione al `next` successivo, **si rimuove il claim crash-safe** e si accetta esplicitamente che un crash dopo il commit del cursore perde la riscoperta del task. È però il rischio che il primo giro chiedeva di chiudere.

> **RATIFICATA da Alan: opzione A.** `reply` è la boundary di conferma. `next` non archivia mai. I messaggi senza risposta restano `notified` — onesti — finché la retention non li pota. Nessun quinto verbo.

## 9. STORIA — esito design-gate, secondo giro

> ⚠️ **Sezione narrativa, NON normativa.** Le conclusioni qui riportate sono quelle del secondo giro, molte superate dai giri successivi. Il contratto è §2.

### CRI2 sulla rev.3 — un P0 nella sintesi VAL

**Il P0 è mio, non di CRI**: la conferma implicita è falsa attraverso le vite dell'agente, e nel caso peggiore il sistema **certifica una consegna mai avvenuta**. Accolto e corretto in §2.3 (join-reset + echo dell'archiviazione). CRI2 lo riassume meglio di me: *"l'unica cosa peggiore di un messaggio nascosto è un messaggio nascosto con la ricevuta di lettura"*.

Adottati anche: `reply` ancorato all'ultimo emesso + fail-closed multi-mittente + echo del destinatario (P1-2, §2.2); payload lungo dichiarato nel design (P1-3, §2.2); marcatore di ri-consegna come frase-policy invece che booleano (P3, §2.3); riga outbound corretta da "letti" a "consegnati" (P2).

**Da riportare nelle sezioni relative, non ancora fatto:**

- **§2.7** deve dire che *il paging si consuma col medesimo richiamo*: `hasMore: true — i prossimi arrivano col prossimo next`. Senza quella frase un agente cercherà un `--page` che non esiste — thinking sprecato, difetto §0. Principio generale: **l'output dichiara la propria azione successiva**, non solo i comandi.
- **§2.1** deve **elencare** la superficie SERVIZIO, che oggi è promessa ma non nominata: `read <id>`, `sent`, il chi-c'è, e una decisione su `state working/done` (F-23 vive ancora — visto in `peers` stasera). Un agente fresco deve sapere *cosa esiste*: bastano sei righe.
- **`who` è citato una volta sola** (piano Tier 2) e mai definito. O si dichiara che `join` è anche il chi-c'è — rilanciabile a volontà, e col join-reset la ri-consegna è innocua e marcata — oppure si definisce `who`.
- **§2.4**: la chiosa di `archived` va resa onesta fino in fondo. Con la conferma implicita prova *"il destinatario ha richiamato `next` dopo la consegna"*, non "confermato". È la regola di CRI (F5) applicata al mio stesso testo.
- **§6.2 risolta**: cursore in un **file separato** nella session dir. Oltre al locking, due ragioni operative di CRI2: il cleanup se lo porta via gratis con la directory, e `inspect` può mostrarlo — lo stato resta ispezionabile dove un operatore già guarda. Un cursore nel manifest accoppierebbe due cicli di vita diversi (identità vs progresso di lettura) in un file che altri comandi riscrivono.

### F-92 rafforzato — con misrouting reale osservato

CRI2 ha ricostruito la propria cronologia: al suo arrivo `overview` gli ha mostrato `peer: VAL-v08 (62033f21)` e il suo poke di presenza è andato **lì** — alla sessione che io ho poi cancellato come duplicato di F-90. Il brief vero gli è arrivato da `VAL-bridge (679b7060)`, un'altra sessione. E ora, con quattro sessioni vive, `overview` gli mostra un **terzo** peer ancora.

Quindi F-92 non è "dice nessuno quando ce ne sono tre": è **"ne mostra UNO, arbitrario, e cambia tra invocazioni"** — e il primo contatto di un agente fresco viene instradato da quel risultato. Il fix non è mostrare *il peer giusto*: è **smettere di scegliere**. `join` e il chi-c'è mostrano sempre la lista completa dei vivi.

Nota: questo spiega perché il primo messaggio di CRI2 era finito nella sessione `62033f21`. Due difetti si sono composti — F-90 ha creato il duplicato, F-92 ci ha instradato sopra il primo contatto.
