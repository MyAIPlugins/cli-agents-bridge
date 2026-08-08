# BRIEF ESC — v0.8 Tier 1

> Da VAL. Consegnato. Fasi 1a e 1b **chiuse e gated verdi** (`f32c8c9` + `7f107b2`); la 1c è in corso.
> Contratto: `docs/DESIGN-v0.8-mailbox.md` — leggi sempre l'ultima revisione su `main`, non una copia. Leggilo prima: §0, §1, §2, §3. Le sezioni §8/§9 sono **narrative, non normative** — contengono affermazioni superate, in conflitto vince §2.

---

## Il lavoro in una riga

Sostituire il modello di consegna del bridge: da **wake accoppiato al consumo** a **mailbox con stati espliciti**, dove `next` è pure-read e solo `reply` sposta file.

## Tre fasi, tre gate

Il Tier 1 è troppo grande per un giro solo. Ogni fase è mergiabile da sola e ha un gate VAL indipendente. **Non iniziare la fase successiva prima del gate verde.**

### Fase 1a — Fondamenta: state machine + `next`

Il cuore. Tutto il resto ci si appoggia.

- Cursore di wake in **file separato** nella session dir (`wake-cursor.json`), schema-versionato, atomic-write 0600, ogni RMW sotto **session lock cross-process**. NON nel manifest: `manifestMu` serializza solo in-process (`manager.go:39`), e `listener.json` è separato esattamente per questa ragione (`listener.go:17-28`). Deve contenere gli **id esatti**, non un "last id": gli id non sono ordinati e il paging produce insiemi non contigui.
- `next`: consegna gli `UNREAD`, li segna `NOTIFIED`, **non sposta un file in nessuna circostanza**.
- **Ordine obbligato: prima la stampa, poi il cursore.** Un crash in mezzo duplica (innocuo, at-least-once); l'ordine inverso perde in silenzio.
- Finestra **24h fissa**, valore in config, **nessun flag CLI**.
- `next` eredita **l'intero envelope lifecycle** di `listen` (§3 e F-95): `AdoptPID`, claim `WaitOwner`, pubblicazione deadline, context con segnali, heartbeat owner-fenced, watcher di reclaim con cancel, e **re-check dell'owner sotto lock prima del commit del cursore**. Non solo il ticker: F-95 nasce proprio dal fatto che `receive --any` ne aveva metà.
- Paging: due limiti (`maxPageMessages` **e** `maxSerializedBytes`), ordinamento per **timestamp decodificato** con id come tie-break (`os.ReadDir` non è ordine di arrivo), `oversize: true` per il singolo messaggio oltre budget, e nel payload la riga che dichiara l'azione: `hasMore: true — i prossimi arrivano col prossimo next`.
- File corrotti: `corruptCount` esplicito nell'output, mai skip silenzioso, mai blocco irrisolvibile.
- Recovery **at-least-once**: cursore assente o corrotto → replay con warning, **mai** interpretato come "già notificato".

**Test che voglio vedere**, oltre ai soliti: l'invariante di §2.3 (si segna solo ciò che si emette, si emette tutto ciò che è `UNREAD` entro i limiti dichiarati); il crash tra stampa e cursore che produce duplicato e non perdita; due `next` concorrenti che non si sovrappongono.

### Fase 1b — Invio: `ask` / `tell` / `reply`

- Tre verbi, **il verbo porta il tipo** (§2.2). Nessun `--type`, nessun `--in-reply-to`.
- Destinatario **per nome agente**, risolto in-scope, **fail-closed**: zero match → errore; più match vivi → errore con i candidati. Mai una scelta silenziosa.
- Payload: **argomento presente → è il messaggio; argomento assente → stdin fino a EOF**. Con l'argomento presente **stdin non viene letto affatto** — non tentare di rilevare "entrambi presenti", richiederebbe di leggere stdin. Messaggio vuoto → rifiuto esplicito.
- **La transazione di `reply` è la parte difficile** (§2.3): set congelato sotto lock, **un** journal con `closeIDs`, **un** response-id, idempotency key deterministica su `(responder, anchor)`, consegna create-if-absent, `SENT` → archiviazione di tutti e soli i `closeIDs` con indice di avanzamento → rimozione del journal. Retry: completa **senza rispedire**. `cleanup` deve rispettare lo stesso fencing.
- `inReplyTo` porta l'anchor, `closes: [...]` l'elenco completo.
- `reply` nudo ambiguo **solo** se due o più mittenti hanno `ask` aperti → `reply <chi>`.

**Test**: crash dopo `SENT` e prima dell'archiviazione → il retry completa e **non duplica la risposta**; crash tra l'archiviazione di A e quella di A2 → riprende dall'indice; un `ask` arrivato dopo lo scatto resta aperto.

### Fase 1c — Onboarding e pulizia

- `join`: registra, **idempotente**, stampa **chi c'è** (la lista completa dei vivi, mai un peer scelto). Su mismatch di nome con una sessione `(ruolo, scope, projectPath)` esistente → **si ferma e chiede**, mai crea la seconda sessione (F-90).
- Rimozione ACK: via `maybeAutoAck`, `autoAckTypes`, `--no-auto-ack`. `ack` esce dall'enum **in scrittura**, resta tollerato **in lettura** (decoder lenient) per i file su disco.
- `sent` con gli stati onesti di §2.4 (`unread`/`notified`/`archived`/`unknown`/`expired`; un I/O error è un **errore**, mai uno stato) e un indice per destinatario costruito in **una** scansione — non O(sent × mailbox).
- Rimozione di `listen`, `receive` e del vecchio `runAsk` (già codice morto compilato). Rottura netta ratificata: nessuna compatibilità, nessun alias.
- **F-91** — il guardrail warna solo su match-per-prefisso, silenzio a match esatto (§2.5), con la remediation invertita (prima quella id-free, l'id come ultima spiaggia). **Promosso a bloccante**: la 1b richiede scope condiviso, lo scope condiviso attiva il guardrail, e il guardrail consiglia `--session-id` che i comandi LOOP rifiutano — un consiglio ineseguibile su ogni comando è un vicolo cieco, non rumore.
- **`CAB_SESSION_ID` letto in INPUT** — oggi esiste solo come *output* (`notify_watch.go:398`) e nessun comando lo legge. Precedenza `--session-id` > `CAB_SESSION_ID` > cwd; id inesistente o malformato → errore esplicito, **mai** fallback silenzioso alla cwd. È il prerequisito del setup a cartelle separate: F-97 copre il caso in cui il comando fallisce, ma lo scenario pericoloso è il **successo silenzioso** — un agente che lancia un comando dalla root prende la sessione di chi vive lì e ne legge la posta *riuscendo*, quindi senza che nulla scatti. Smoke richiesto: una sessione registrata in una sottocartella lancia un comando dalla root e **non riesce** a diventare l'altra.
- **F-43** (dedup) riportata su `ask` e `tell`, che non sono idempotenti. **F-37 no**: è assorbita dal design — serviva a rifiutare un id inventato, e con `reply` che risolve l'ancoraggio da sé non c'è più un id da passare. **F-34 da verificare**: `reply` chiude *tutti* gli `ask` aperti del mittente, quindi potrebbe chiuderne uno mai emesso da un `next`; se il buco c'è, il posto è l'echo di `reply`.
- Riduzione B-2 secondo §3: via `PollInboxOwned`, `DrainInboxOnceOwned`, `ownerOK` in `consumeInboxEntry`. **Resta tutto il resto** — token/generation, claim/reclaim sotto lock, `tryReuse`, `StartHeartbeatOwned`, watcher di reclaim. L'ownership cambia oggetto, non sparisce.

---

## Cosa NON fare

- **Non toccare i docs.** ROADMAP, CHANGELOG, CLAUDE.md, skill e il design doc sono miei. Segnala e vado io.
- **Non implementare il Tier 2/3**: cross-repo `link`, `fromScope`, **F-92** (`overview` che sceglie un peer arbitrario), reminder dei non-letti. Un'altra volta. *(F-91 e `CAB_SESSION_ID` sono stati promossi nella 1c — vedi sopra: erano Tier 2, non lo sono più.)*
- **Non aggiungere flag** sul percorso caldo perché "torna comodo". Se ne serve uno, fermati e chiedi: è una decisione di design, e §0 dice che ogni flag lì è un difetto finché non dimostra il contrario.
- **Non introdurre dipendenze.** Il binario di produzione oggi ha zero dipendenze (`testify` è solo di test) e resta così.

## Cosa mi aspetto da te

Il pushback motivato vale più dell'obbedienza (LL-4): se una parte del contratto è sbagliata o inimplementabile, **dillo con il riferimento al codice** invece di aggirarla. Questo design ha già preso sei P0 ai gate, quattro dei quali in sintesi mie — la probabilità che ne resti uno non è bassa.

Se durante l'implementazione scopri che una regola di §2 non regge, **fermati e segnalala**: non decidere tu al posto del contratto. È esattamente ciò che il gate ha passato quattro giri a evitare.

## Il gate

Per ogni fase, indipendente da quello che dichiari tu (LL-11):

- `go vet` + `go test -race -count=1 ./...` **completo**, e verifico che nessun package critico sia `(cached)`;
- lettura del diff riga per riga;
- **smoke reale con processi separati** — obbligatorio, non facoltativo: questa roba è liveness, processi e concorrenza, e LL-10/LL-12 dicono che i test verdi provano la logica, non il modello d'uso. Per la 1b lo smoke deve includere un crash indotto tra `SENT` e archiviazione.

Per le fasi 1a e 1b previsto anche il **diff-gate CRI** (LL-17: concorrenza, persistenza, process lifecycle — la soglia è ampiamente superata).
