# DESIGN v0.8 — KISS per AI

> Stato: **PROPOSTA VAL rev.2, in design-gate (CRI + CRI2)**
> Autore: VAL · Data: 2026-08-08
> Ratificato da Alan: rottura netta · ACK eliminati · **"il non far pensare vince"**

---

## 0. Il principio ordinatore

> *"Per usare il bridge l'ideale è che gli agent pensino il meno possibile: tutto predisposto su binari fissi. Se gli dai troppe opzioni, l'agent spreca token di thinking inutili."* — Alan

Questa è la regola che governa ogni scelta del documento, e ha **precedenza su tutto il resto**: eleganza dell'API, ortogonalità dei comandi, potenza espressiva. Un'opzione in più non costa solo superficie: costa **thinking a ogni ciclo**, su ogni agente, per sempre.

Ne discendono tre criteri operativi:

1. **Se l'agente deve chiedersi "quale uso?", il design è sbagliato.** Un comando per situazione, con un nome che dice da solo quando usarlo.
2. **Se l'agente deve chiedersi "in che stato sono?", il design è sbagliato.** Il comando deve funzionare uguale in ogni stato.
3. **Ogni flag sul percorso caldo è un difetto** finché non dimostra di essere indispensabile. Il default deve essere l'unica strada.

Il criterio 3 ha già una conferma sul campo: lasciato libero di scegliere, CRI ha impostato `--max-deadline=120`. Due minuti. Non per incapacità — semplicemente non aveva modo di sapere quale fosse il valore giusto, e nessun default lo guidava.

---

## 1. Diagnosi

Tre sintomi riportati da Alan dopo l'uso reale, tutti verificati sul codice.

### S1 — Il listener si dimentica di essere rilanciato

`listen` ha finestra di default **540s** (`cmd/cab-bridge/listen.go:49`), motivata in commento dal timeout subprocess di 10 minuti dell'harness. `receive` ha `--max-deadline` a 1800s. Su una giornata significa **~160 rilanci** (o 48). Ogni rilancio è un'occasione per dimenticarsene, e chi se ne dimentica perde i messaggi dalla vista.

### S2 — Gli ACK sono rumore che l'agente scambia per contenuto

`ask` invia `--type=query` di default (`ask.go:20`), quindi **ogni messaggio genera un auto-ack** (`send.go:125`). Da lì un'asimmetria: `listen` (accept=nil) **consuma ed emette gli ack** come JSON pieni, indistinguibili da contenuto reale; `receive --any` e `scanForReply` li **saltano e li lasciano in inbox**, dove si accumulano finché un `listen` non li sputa fuori tutti insieme.

### S3 — I comandi di lettura nascondono messaggi

`scanForReply` (`internal/transport/fs/receive.go:105`) ritorna al **primo** match di `inReplyTo`, lo archivia e basta: tutto il resto arrivato nel frattempo resta in `inbox/` **senza alcun segnale**. `listen --wait-one` drena il batch ed **esce**: ciò che arriva un secondo dopo è invisibile fino al rilancio.

### Radice comune

**Il bridge accoppia WAKE e CONSUME**, e in più **fa scegliere l'agente** a ogni passo. Dall'accoppiamento: chi sveglia deve *scegliere* cosa consumare (→ S3), consumare genera *ricevute* (→ S2), chi consuma va *fenced* e la finestra tenuta corta per limitare il danno di un orfano (→ S1). Dalla scelta: cinque modi di aspettare, tre di leggere, deadline configurabili — tutto thinking sprecato.

---

## 2. Il modello

### 2.1 Due superfici separate

Il difetto di fondo dell'API attuale è che **tutto è allo stesso livello**: `migrate-from-patil` e `listen` sono due voci dello stesso help. L'agente vede venti comandi e deve capire quali lo riguardano.

v0.8 separa nettamente:

- **Superficie LOOP** — quello che un agente usa lavorando. **Quattro comandi, zero flag.** È tutto ciò che deve sapere.
- **Superficie SERVIZIO** — ispezione, manutenzione, amministrazione. Esiste, è documentata, ma **non serve conoscerla** per lavorare.

### 2.2 La superficie LOOP

```
cab-bridge join           una volta, all'inizio
cab-bridge next           all'infinito: l'unico comando del ciclo
cab-bridge reply "..."    risponde a chi ha scritto per ultimo
cab-bridge tell <chi> "..."   scrive a qualcuno
```

**`join`** — registra la sessione, deriva ruolo e nome dal contesto, e **stampa chi c'è** (tutti i peer vivi nello scope, non "il peer"). Sostituisce `register`/`bootstrap`/`whoami`/`overview` iniziali. Idempotente: rilanciarlo dopo un compact riaggancia la stessa sessione invece di crearne una seconda (chiude F-90).

**`next`** — l'unico comando del ciclo, e **funziona uguale in ogni stato**:

- ci sono messaggi non letti → li stampa **tutti** e li archivia;
- non ce n'è nessuno → aspetta fino a **24h**, poi stampa quelli arrivati e li archivia;
- la finestra scade a vuoto → esce dicendo "niente, rilanciami".

L'agente non deve mai capire in quale situazione si trova: **la risposta è sempre `next`**. Nessun flag: né durata, né formato, né filtro, né session-id (risolto da cwd).

**`reply` / `tell`** — due comandi invece di uno con destinatario opzionale, perché "opzionale" è una decisione. Se stai rispondendo usi `reply` e non nomini nessuno; se apri tu usi `tell`. Nessun `--type` (si deduce), nessun `--in-reply-to` (`reply` lo mette da sé), nessun id da trascrivere.

**`tell` accetta il NOME dell'agente, non l'id** — `tell ESC-bridge "..."`, risolto in-scope, con errore e lista se ambiguo (mai una scelta silenziosa). Senza questo LL-14 resterebbe incompiuto proprio nel caso più frequente: `link`/@alias copre il cross-repo, ma l'id che si ricopia *ogni giorno* è quello del proprio ESC. (CRI2, P1-4.)

**Il sommario di `next` include una riga outbound**: `outbound: 2 pending presso i destinatari da >30m`. Ricuce a costo zero l'unica cosa che si perde eliminando gli ACK — prima la ricevuta ti *raggiungeva*, ora "ESC ha preso il brief?" richiederebbe di ricordarsi di lanciare `sent`, cioè disciplina, cioè la risorsa scarsa. Così il segnale torna a raggiungerti senza traffico e senza un comando in più. (CRI2, P2-7.)

### 2.3 L'invariante che rende sicuro l'automatismo

Alan aveva inizialmente proposto un mark-read esplicito ("solo l'agent marca, per non perdere messaggi"), poi ratificato che *il non far pensare vince* — e il mark esplicito è un'azione da ricordare a ogni ciclo.

La tensione si scioglie osservando **da dove nasce davvero la perdita**. Non dall'automatismo: nasce dal fatto che `receive --msg-id` **archivia uno e ne nasconde altri**. Il difetto è la *parzialità*, non l'automatismo.

Quindi la garanzia da scolpire nel codice non è "solo l'agente marca", ma:

> **Si archivia solo ciò che si è mostrato, e si mostra sempre tutto ciò che c'è.**

Con questa invariante l'archiviazione automatica è sicura e l'agente non ricorda nulla. Va enunciata nel package doc ed **esercitata da un test dedicato** (mai un percorso che archivia più di quanto emette).

**Caso residuo accettato consapevolmente**: `next` consegna tre messaggi, li archivia, e l'agente va in compact prima di agire. Restano in `processed/`, recuperabili da `history`, ma lui non sa di doverli cercare. Prezzo giusto: quel caso è raro e recuperabile, mentre "ricordarsi di marcare" è un costo su **ogni** ciclo. Se dovesse rivelarsi frequente, si copre senza far pensare l'agente — `next` ristampa in testa ciò che ha consegnato e su cui non ha visto seguito.

### 2.4 ACK: sostituiti da stato interrogabile

Eliminati `maybeAutoAck`, `autoAckTypes`, `--no-auto-ack`. Il tipo `ack` esce dall'enum in **scrittura**; in **lettura** resta tollerato (decoder lenient) per i file già su disco.

Al loro posto `sent` deriva lo stato da dove il messaggio si trova nella inbox del destinatario: `pending` (consegnato, non gestito) · `handled` (archiviato) · `gone`. Il mittente **interroga** invece di ricevere: zero traffico, zero rumore, e l'informazione è più ricca di prima.

Chi vuole dire "ricevuto, mi metto al lavoro" ha già `state working` (F-23).

### 2.5 Il gruppo, non la coppia

`overview`, il guardrail shared-scope e `bootstrap` sono tarati sulla **coppia** VAL/ESC. Con la quadriade — ormai il setup normale — degradano in due modi opposti e ugualmente dannosi: `overview` dice "nessun peer" con tre peer vivi (F-92), il guardrail stampa sei righe di warning e consiglia di trascrivere un id (F-91). **Nessuno dei due dice semplicemente chi c'è.**

Sotto il criterio §0 la correzione è obbligata: `join` e `who` **dicono chi c'è**, sempre, senza che l'agente ricostruisca il quadro da solo. Osservato dal vivo: CRI si è orientato con `overview`, ha letto "nessun peer", e si è messo in attesa passiva mentre il suo brief era già in viaggio.

**Il fix di F-91 è chirurgico** (CRI2, P1-3): il guardrail oggi warna su `len(ScopeSiblings) > 0` (`common.go:197`), cioè sull'**esistenza** di altri agenti — non sulla **debolezza** della risoluzione. Ma i due gradi di certezza sono nettamente diversi:

- **match esatto** (`cwd == projectPath`): questa sessione è emessa da questo worktree, e la presenza di siblings altrove non la rende meno certa. È il caso **normale** — ogni agente al root del proprio worktree. Qui il warning è puro rumore.
- **match per prefisso** (cwd sotto il projectPath selezionato): qui l'agente potrebbe davvero essere "un altro", mai registrato. È il caso dello stress-test LL-14, e il warning è giustificato.

Warnare solo su `match-per-prefisso AND siblings>0`, silenzio a match esatto. `HardAmbiguous` resta invariato. Nel setup a quattro il rumore va a **zero** senza trascrivere alcun id, e la rete resta dove il rischio è reale. `LookupByCWDDetails` ha già `matchLen` in mano: è una condizione, non un redesign.

E la remediation va invertita: prima quella id-free ("lancia i comandi dal root del tuo worktree"), l'id come ultima spiaggia — non come prima risposta.

### 2.6 Cross-repo VAL↔VAL

Oggi: `peers --all-scopes` → leggere l'id → **trascriverlo** in `ask --to=<id>` → ricordarlo per ore. `ask --to` accetta solo un session-id (`ask.go:19`). È l'unico id che un agente deve tenere a mente a lungo, ed è il primo che confabula dopo un compact (LL-13/LL-14). In più il messaggio **non porta la provenienza** (`schema.go`): chi legge un brief da `val-bi` non sa se viene dal proprio ESC o da un altro progetto.

- **`link add <alias> --repo=<path> --role=<role>`** — contatto persistente che punta a `(repo, ruolo)`, **non a un id**. Poi `tell @chatterence "..."` per sempre: risolto a runtime al peer vivo, sopravvive al compact e a una ri-registrazione.
- **`fromScope`** nel messaggio, evidenziato in lettura quando è cross-scope (schema bump, coerente con la rottura netta).

---

## 3. Cosa si semplifica

Il fencing B-2 (`internal/session/listener.go`, 181 righe, più `DrainInboxOnceOwned`/`PollInboxOwned`/`ownerOK`) esiste perché due listener potrebbero consumare lo stesso messaggio.

**Attenzione — non eliminare alla cieca.** Con `next` il consumo resta (archivia ciò che consegna), quindi **il doppio-consumo resta possibile** se due `next` girano insieme sulla stessa sessione. Rispetto alla rev.1 di questo documento — che assumeva zero consumo e quindi zero fencing — **questo è un cambio sostanziale: il fencing serve ancora.** In compenso l'ownership dell'heartbeat resta necessaria comunque (un orfano che batte fa apparire viva una sessione morta).

*Quanto esattamente si può togliere, e cosa si perde togliendolo,* è la domanda numero uno del gate.

---

## 4. Finding emersi sul campo

Nessuno cercato: tutti emersi **usando** il bridge per coordinare il lavoro su se stesso.

- **F-90** — `register --resume` con un `--agent-name` diverso **crea una seconda sessione** sullo stesso projectPath invece di rinominare → hard-ambiguity immediata che blocca ogni comando id-free. Il comando nato per il recovery post-compact (F-27) rompe la risoluzione id-free (LL-14) se l'agente si ripresenta con un nome anche solo leggermente diverso. Riprodotto su me stesso in trenta secondi. Chiuso da `join` idempotente.
- **F-91** — Il guardrail shared-scope **non scala oltre 2 peer**: con 4 sessioni stampa 6 righe di warning prima di *ogni* comando id-free, e consiglia `--session-id`, cioè spinge verso la trascrizione di id che LL-14 vuole eliminare. Il guardrail lavora contro il proprio scopo.
- **F-92** — `overview` risponde **"peer: (none paired in this scope yet)"** con **tre** peer vivi e visibili in `peers`. Stesso difetto di S3 — nascondere informazione disponibile — applicato alla topologia, e colpisce proprio il comando dell'orientamento (F-42).
- **F-89** (noto, esteso) — `read <id> --session-id=X` fallisce: i flag devono precedere il positional. Vale anche per `state`.

### La prova decisiva sugli ACK

Il listener del VAL (`listen --wait-one`) si è svegliato ed è **uscito** per due ACK vuoti — `"ACK msg-...: received"`, zero contenuto — arrivati da ESC e CRI2. Costo: un ciclo di attenzione, i token della re-invocazione, e soprattutto **il VAL è rimasto fuori dall'ascolto senza saperlo**.

Quindi S1 (il VAL perde messaggi) è **causato** da S2 (gli ACK): con `--wait-one` la prima ricevuta di consegna chiude la finestra aperta per il messaggio vero. I due sintomi che Alan aveva riportato come distinti sono lo stesso difetto.

---

## 5. Piano

| Tier | Contenuto | Chi |
|---|---|---|
| **0** | Spike Codex app-server: push senza `screen`? Time-box 2h | ESC |
| **1** | Superficie LOOP: `join` / `next` / `reply` / `tell`, ACK rimossi, invariante §2.3 + test | ESC |
| **2** | Gruppo non coppia: F-91/F-92, `who`; superficie SERVIZIO riorganizzata | ESC |
| **3** | Cross-repo: `link`, `fromScope`; reminder non-letti; skill riallineate | ESC + VAL |

Nessuna migrazione dati: zero sessioni attive da oltre due giorni (confermato da Alan). `cleanup --scope=global` e si riparte puliti.

---

## 6. Domande per il gate

1. **§3 — il fencing B-2**: quanto si può togliere, e cosa si perde? (La rev.1 sbagliava: con `next` che archivia, il doppio-consumo resta possibile.)
2. **§2.3 — l'invariante "si archivia solo ciò che si mostra"**: regge in tutti i percorsi? Come la si esercita in test in modo che non possa driftare?
3. **Il caso residuo di §2.3** (compact tra consegna e azione): davvero raro, o l'ho liquidato troppo in fretta?
4. **Quattro comandi bastano davvero?** Cosa fa un agente quando ha bisogno di qualcosa che non c'è nella superficie LOOP — e come fa a scoprirlo senza pensare?
5. **`next` senza flag**: quale scenario reale rompe, che oggi è coperto da `--unseen`/`--msg-id`/`--emit`?
6. **La finestra di 24h** regge sull'harness? (§7: il limite di 10 minuti è già caduto.)

---

## 7. Verifiche empiriche

### Durata dei job in background — il limite di 10 minuti non si applica (test CHIUSO)

Test dalle 17:24:25 alle 17:43:55 UTC: processo in background con un tick ogni 30s. **40 tick su 40, terminato regolarmente — 19,5 minuti di vita continuata.**

Il timeout subprocess di 10 minuti citato in `listen.go:103` come motivazione della finestra a 540s **vale per il foreground, non per i job in background**. La premessa su cui è tarata la finestra corta è, per il caso d'uso reale, **falsa**.

**Onestà sui limiti**: 20 minuti dimostrano il superamento della soglia con margine, non le 24 ore. Un test da 24h non è praticabile in sessione.

**Piano-B, deciso PRIMA e non dopo** (CRI2, P3): se sul campo la finestra da 24h non regge, si scende al massimo che l'harness sostiene e `next` si rilancia da sé alla scadenza. S1 si attenua invece di sparire — accettabile, perché il rilancio non è più a carico dell'agente. Il fallimento degrada comunque in modo benigno: alla morte del processo l'harness notifica, l'agente rilancia, cioè il comportamento di oggi ma molto più raro.

### Vincolo di runtime: `next` presuppone un harness con wake-on-exit

La finestra lunga è calibrata su Claude Code, dove la fine di un job in background genera una notifica. **Su un runtime senza push (Codex, LL-16) un bloccante da 24h in foreground è inutilizzabile**: lì il pattern resta `notify-watch` (F-66) con iniezione esterna. Non è un difetto del design, ma va scritto — altrimenti qualcuno proverà a far girare `next` dentro Codex e concluderà che il bridge è rotto. (CRI2, Q2.)

---

## 8. Esito design-gate — CRI2 (lente ergonomia-agente)

CRI2 ha verificato **tutti** i claim S1/S2/S3 sul codice prima di criticare (F-16), confermandoli e aggiungendo riferimenti che non avevo: `internal/config/config.go:122` per i 540s, `internal/session/reconnect.go:12,25` per F-90 (identity match stretto: nome diverso → fall-through a register fresco), `common.go:194-208` per F-91.

**Verdetto: la diagnosi radice regge, il modello è la direzione giusta, la superficie aveva tre difetti di ergonomia.**

Fatto notevole: CRI2 ha valutato la **rev.1** (`inbox → handled → wait`), e le sue due obiezioni P1 più forti — il sommario con preview che crea *due porte di lettura*, e `handled --all` che reintroduce S3 dalla porta di servizio — **sono esattamente ciò che la rev.2 elimina**, per una via indipendente (il principio §0 dettato da Alan). Due percorsi diversi, stessa conclusione: segnale forte che la direzione è giusta.

| Punto CRI2 | Esito in rev.2 |
|---|---|
| P1-1 `wait` con preview = due porte di lettura | **superato**: `next` non è un sommario, è la lettura completa |
| P1-2 `handled --all` reintroduce S3 | **superato**: `handled` non esiste più |
| P1-3 F-91 warning solo su match-per-prefisso | **adottato** (§2.5) |
| P1-4 `--to` by-agent-name in-scope | **adottato** (§2.2) |
| P2-5 F-90 resume zero-arg + stop-and-ask | **adottato**: `join` idempotente; su mismatch di nome con una sessione stesso (ruolo, scope, path) si **ferma e chiede**, mai crea in silenzio la seconda sessione che blocca tutto |
| P2-6 rifiuto di `wait` con payload actionable | **superato**: `next` non rifiuta mai |
| P2-7 riga outbound nel sommario | **adottato** (§2.2) |
| P2-9 bootstrap da ridisegnare | **superato**: `join` lo sostituisce |
| P3-10 `sent` STATUS `gone` ambiguo | **adottato**: distinguere "sessione destinataria assente" da "messaggio assente" — la prima si rileva già con lo stat della dir |
| Q2 vincolo runtime no-push · piano-B 24h | **adottati** (§7) |

**Il regalo che CRI2 ha visto e io no**: con un waiter che non richiede attenzione, il flusso *armare `next` in background → lavorare* diventa la norma per l'executor, e questo chiude **F-14** (l'ESC sordo durante l'implementazione) senza scrivere una riga in più — uno STOP del VAL lo raggiunge a metà task. Va insegnato nella skill come flusso raccomandato: è un argomento a favore del modello, non un effetto collaterale.

**Punto ancora aperto**, da non perdere: la semantica di `processed/` va enunciata una volta sola e senza ambiguità — "da gestire" vs "preso in carico" vs "letto" sono tre cose diverse, e la rev.1 le usava tutte e tre.
