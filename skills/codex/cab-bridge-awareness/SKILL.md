---
name: cab-bridge-awareness
description: Protocollo per comunicare con altre sessioni di agenti CLI tramite il binario `cab-bridge` (IPC su filesystem locale). Use quando sei un peer in un team cab-bridge — ricevi task e rispondi a un orchestratore VAL con i cinque comandi del ciclo (join/next/ask/tell/reply). Companion della skill `critico` quando il tuo ruolo e' CRI (critico on-demand).
metadata:
  author: alan-curtis
  version: "2.0.0"
---

# cab-bridge — protocollo peer

`cab-bridge` e' un singolo binario in PATH per scambiare messaggi con altre sessioni di agenti CLI via file. Lo invochi come un normale comando shell. Tu sei un **peer**; un orchestratore **VAL** ti manda task e tu rispondi. Lo stato vive su disco, non nella tua memoria: dopo qualsiasi interruzione lo ricostruisci con `overview` + `inbox --list` + git.

## Ingresso: DUE passi, e il secondo e' il tuo INDIRIZZO

    1)  cab-bridge join --role=critic --agent-name=<il nome che ti hanno dato>
    2)  DICHIARA il tuo CODEX_THREAD_ID a chi ti coordina, nel primo messaggio
    3)  poi RESTA FERMO: niente goal persistente, niente `next` appeso, nessuna attesa

**Il passo 2 e' quello che ti rende raggiungibile**, ed e' nuovo (12 settembre). Leggi il tuo id
dall'ambiente del processo — `CODEX_THREAD_ID` e `CODEX_SESSION_ID`, che valgono lo stesso valore — e
mandalo a chi ti coordina. **Da quel momento puo' svegliarti** con:

    codex queue --thread <il tuo id> --message "hai posta sul bridge"

⭐ **Verificato il 12 settembre, con il controllo umano**: un CRI **fermo** — nessun goal, nessun
`next` appeso — e' stato svegliato dall'esterno e ha risposto sul bridge, **senza che nessuno
toccasse la sua TUI**. ⇒ **Aspettare e' diventato inutile**, e con esso sparisce il costo: prima
restare "pronto" costava **~1 turno al minuto**, cioe' **~130.000 token/ora per non fare niente**.

### Quando ti svegliano

    1)  cab-bridge next     ← una volta, per PRENDERE la posta (non per aspettarla)
    2)  lavora il messaggio SUBITO
    3)  rispondi
    4)  torna FERMO. Non rilanciare `next`, non creare goal.

⚠️ **`next` senza posta si blocca**, quindi lanciarlo quando non ti hanno svegliato ti riporta
esattamente nel mondo che questo modello elimina.

### ⛔ Il ripiego, e quando serve davvero

Se **chi ti coordina non sa svegliarti** — non gli hai dato l'id, o ti dice che non puo' usare
`codex queue` — allora **e il solo caso in cui** torni al vecchio modello: `create_goal` che tiene un
`next` appeso, con la durata massima che il tuo strumento accetta.

**Sappi cosa stai comprando**: senza goal e senza risveglio **finisci di rispondere e non esisti
piu'** finche' un umano non ti scrive — successo davvero, un CRI fece `join` + `next`, annuncio'
*"sono in ascolto"* e resto' **muto per sempre, convinto di ascoltare**. Il goal evita quello, ma al
prezzo dei ~130k token/ora. **Con l'id dichiarato non paghi ne' l'uno ne' l'altro.**

⚠️ **E il compromesso non e' ancora automatico**: oggi il risveglio lo fa **una persona o il VAL a
mano**. Il watcher che dovrebbe accorgersene da solo **non esiste su Windows**, e il suo disegno ha
quattro difetti noti (dedup senza retry, paginazione, wake duplicati, race mentre lavori). ⇒ Se dopo
aver dichiarato l'id **nessuno ti sveglia mai**, non e' il tuo id a essere sbagliato: e' che dall'altra
parte non c'e' ancora niente che chiami `codex queue`. **Chiedilo invece di aspettare.**

### LA REGOLA CHE VIENE PRIMA DI TUTTE

**Se `next` ti restituisce un messaggio, lo lavori SUBITO.** Non dopo, non al prossimo giro: adesso, prima di qualunque altra cosa.

    next emette "status": "emitted"       ->  NON basta. Aspetta il record FINALE.
      poi "status": "committed"           ->  ora e' tuo: RILANCIA next, poi lavora.
      poi "not-committed" / "interrupted" ->  NON lavorare quella pagina: segui il suo `hint`.
    next ritorna a vuoto                  ->  rilancia next SENZA PAUSA

**`emitted` non e' un permesso: e' meta' del protocollo.** `next` stampa due record, e il secondo puo' **revocare** il primo — se un'altra istanza ti ha sfrattato fra l'emissione e il commit, arriva `not-committed` e ti dice testualmente di ignorare la pagina che hai appena letto. Lavorare su `emitted` significa poter fare un lavoro che ti e' stato ordinato di buttare, che e' esattamente il doppio lavoro che il fencing esiste per evitare. Leggi tutto l'output, non la prima riga.

**E rilancia `next` PRIMA di iniziare a lavorare, non dopo.** Un compito lungo dura minuti o ore, e in quel tempo puo' arrivare una correzione, un annullamento o un urgente. Se il tuo waiter e' spento perche' stai lavorando, quel messaggio resta fermo finche' non hai finito — e la correzione arriva quando il lavoro sbagliato e' gia' fatto. Il costo di riarmare prima e' zero: `next` non consuma, e trovi il messaggio quando torni.

*(Onesta' sul limite: con un runtime che non ti sveglia — vedi sotto — il waiter armato non ti interrompe a meta' lavoro. Ma la differenza resta grossa: al primo momento in cui alzi la testa il messaggio e' li' invece che da raccogliere.)*

**"Senza pausa" e "attesa lunga" non si contraddicono**, e il modo in cui si conciliano e' l'unica cosa da capire di questo ciclo: **il tempo lo passi DENTRO l'attesa su `next`, non fra un `next` e l'altro.** Se senti il bisogno di aggiungere una pausa, stai per fare l'errore: l'attesa **e'** la pausa.

### Il giro, in una riga

> Lancia `next`, **aspetta che ritorni**, lavora quello che porta, rilancialo. Un solo `next` alla volta: se ne hai gia' uno appeso, non aprirne un altro e non affiancargli nessuna attesa.

## ⛔ IL RIPIEGO IN DETTAGLIO — come si aspetta, quando aspettare e' l'unica strada

*(Tutto quello che segue vale SOLO se chi ti coordina non puo' svegliarti. Se hai dichiarato il tuo
`CODEX_THREAD_ID` e qualcuno ti sveglia, salta questa sezione: non aspetti, quindi non ti riguarda.)*

### Aspetta SUL `next`, non accanto — e chiedi la durata MASSIMA

`next` **non ha finestra**: resta appeso finche' non arriva posta. Quindi **il tuo ciclo e' aspettare su quel processo**, e quando l'attesa finisce a vuoto riprendi ad aspettare sullo **stesso** waiter.

**Chiedi ogni volta la durata massima che il tuo strumento accetta**, e il motivo e' un meccanismo preciso — misurato da un CRI sul proprio runtime, con la sonda, non dedotto:

> **L'attesa NON ritorna sull'output.** Ritorna quando il **processo termina**, o quando scade la finestra. Provato: un processo ha scritto output 2 s dopo l'inizio della poll ed e' rimasto vivo altri 30 — la poll e' tornata a **31,9 s**, cioe' all'uscita, non alla scrittura.
>
> **Ma per `cab-bridge next` questo va benissimo, ed e' la ragione per cui la finestra lunga e' gratis**: quando arriva posta, `next` stampa `emitted`, completa `committed` e **esce**. Quindi la tua attesa torna subito — per l'uscita del processo.

**Non generalizzare la regola a processi che restano vivi dopo aver scritto**: li' una finestra lunga ti fa arrivare tardi davvero. Vale per `next` perche' `next` consegna e muore.

Quindi una finestra lunga non ti fa arrivare tardi, mentre accorciarla non compra niente e **costa un turno ogni volta** — contesto e token bruciati a vuoto. I tre regimi, **misurati**, a parita' di latenza:

    nessuna attesa (solo goal)   ~450 turni/ora   un turno ogni 8 secondi per dire "niente"
    tranche da 5 minuti            12 turni/ora
    tranche da un'ora            ~1 turno/ora     <- 🔴 FALSO, e misurato falso il 12/09

🔴 **La terza riga ha mentito per un mese, e l'ha smentita un CRI leggendo i propri contatori.** Il
valore alto nel config **non viene mai raggiunto**: un'istruzione superiore del runtime vieta le
attese oltre ~60 secondi, quindi l'agente spezza l'attesa in tranche da un minuto e **ogni tranche
costa un turno**. `[E]` Due turni vuoti consecutivi misurati a **60,0107 s** e **60,0069 s**, con i
contatori a **43.085 → 45.246 → 47.392** token:

    il vero costo di aspettare   ~1 turno al MINUTO · ~2.150 token l'uno · **~130.000 token/ora**

⚠️ E la precisazione che sposta la causa, dalle sue parole: *"non ho misurato un clamp della macchina
e non attribuisco il minuto al runtime: **sono io che devo richiedere 60000**"*. ⇒ Non e' un tetto
meccanico: e' **un'istruzione che l'agente obbedisce** scegliendo la finestra. Alzare
`background_terminal_max_timeout` e' **inerte**: e' gia' stato provato.

⭐ **Ma dal 12 settembre tutto questo e' il RIPIEGO, non il modo di lavorare**: se hai dichiarato il
tuo `CODEX_THREAD_ID` non aspetti affatto, e il costo di essere pronto e' **zero**.

Il primo non e' un'ipotesi: osservato il 10 agosto, ha quasi esaurito una quota settimanale in pochi minuti.

Il massimo di **una** poll vuota di `write_stdin` e' governato da `background_terminal_max_timeout` nel config di Codex. **Su questa macchina e' impostato a `3600000` (un'ora)**; il default sarebbe `300000` (5 minuti). Quindi chiedi `yield_time_ms: 3600000`. Anche `functions.wait` accetta `yield_time_ms` (default 10.000 ms): alzalo.

**Chiedilo anche se la descrizione del tuo `write_stdin` dichiara un massimo piu' basso: quel numero e' il default del PRODOTTO, non il limite di questa macchina.** Lo schema esposto al modello dice `5000-300000 ms` per una poll vuota (e 30.000 per una write non vuota), e un CRI che si e' fidato di quella riga si e' auto-limitato a 5 minuti — **12 turni/ora invece di 1**, col config giusto gia' sul disco da un giorno. Misurato l'11 agosto: una singola poll chiesta a `3.600.000` era **ancora pendente a 307 s**, cioe' oltre il massimo dichiarato. *(Fin dove arrivi non e' stato misurato: sappiamo che supera i 300 s, non che regga un'ora intera.)* **La descrizione di uno schema e' un'affermazione sul comportamento, e come ogni testo accanto al codice non ha modo di fallire: il numero vero e' quello che torna.**

**E non costruire un aggregatore.** Se ti ritrovi a impilare N attese corte dentro una cella per simularne una lunga, il difetto non e' la durata: e' che stai chiedendo il numero sbagliato. Un wrapper del genere mette un pezzo fra il waiter e te — se muore, un messaggio risulta **consegnato sul disco** e tu non lo vedi mai — e se ristampa il payload lo tronca al proprio limite di output. Se un giorno servisse davvero, la forma giusta e' **segnale, non payload** — ma va completata, perche' **un segnale nudo non ti lascia l'id in mano**: o il wrapper **scrive l'output di `next` in un file** e segnala solo il path, oppure dopo il segnale fai `cab-bridge inbox --list` per leggere l'id e poi `cab-bridge read <id>`. Senza uno dei due la ricetta **non e' eseguibile**: `next` ha gia' committed quella pagina, e rilanciarlo non te la ridara'.

**Non fidarti del numero che hai passato: misura il tempo di ritorno.** Chiedere piu' del massimo **non da' errore** — con il default a 300.000, una richiesta da 3.600.000 tornava a vuoto dopo 300.036 ms, in silenzio. Se un'attesa che hai chiesto lunga ritorna molto prima **e non ha consegnato niente**, non stai aspettando quanto credi: dillo al VAL invece di adattarti.

**Nel goal ci va il NUMERO, non una formula** — e questa regola dice l'opposto di quella che c'era prima, perche' quella ha fallito sul campo.

    yield_time_ms: 3600000
    (fonte: `background_terminal_max_timeout` in ~/.codex/config.toml — se un giorno cambia, si guarda li')

La versione precedente diceva *"non scrivere un numero, scrivi 'la durata massima che il tuo strumento accetta', perche' un numero invecchia"*. Ragionamento sensato, e sbagliato per una ragione che non prevedeva: **una formula relativa, dopo un compact, si risolve sulla fonte piu' vicina — che e' lo schema del tuo `write_stdin`, e lo schema dichiara `5000-300000`, cioe' mente** (vedi il paragrafo sopra).

Misurato l'11 agosto su un CRI reale: ha applicato la regola per tre ore — un'attesa da 3600 s, un turno all'ora — poi **dopo la compaction e' tornato a `300000`**, con attese di 284,8 / 285,8 / 287,0 s e dodici turni all'ora, **mentre la skill e il goal davanti a lui dicevano ancora di usare il valore del config**. Parole sue: *"instruction drift mio dentro la sessione lunga"*. Non aveva perso l'istruzione — aveva perso **l'ancoraggio**, e ha ripreso il numero dello schema perche' quello arriva insieme alla capacita' di chiamare il tool, mentre una skill e' testo che va ri-applicato.

Il numero esplicito non richiede nessuna inferenza; la fonte scritta accanto copre l'invecchiamento, che era la preoccupazione giusta della vecchia regola.

**E non abbassarlo mai di tua iniziativa.** Se il ritorno arriva molto prima di quanto hai chiesto, quello e' un **dato da riportare al VAL** — non un numero da correggere in silenzio. Chi abbassa risolve il proprio fastidio e cancella la prova.

**Il timeout e' una rete, non una sveglia.** Quando l'attesa scade **a vuoto**, prima di rimetterti in attesa **controlla che il processo `next` sia ancora vivo**: e' l'unico momento in cui ti accorgeresti che e' morto in silenzio, ed e' successo davvero — un `next` vivo da sedici ore e mezza e' caduto, e il messaggio e' rimasto non letto senza che nessuno se ne accorgesse.

**Non mettere mai un `sleep` accanto all'attesa.** E' la differenza fra le due cose:

    GIUSTO     aspetta sul processo `next` alla durata massima  -> un turno, e torni nell'istante in cui `next` consegna ed esce
    SBAGLIATO  lancia `next`, poi `sleep 300` in un altro turno -> un turno speso a dormire, e sei CIECO

**Due errori reali, lo stesso giorno, opposti fra loro:**

- un CRI **non aspettava affatto** sul terminale — lanciava `next` e tornava subito — e ha ripollato ogni 10-30 secondi;
- un altro ha messo `sleep 300` **accanto** al `next`: tre turni consecutivi spesi a dormire, e nel frattempo **non si e' accorto della domanda del VAL che il suo stesso `next` aveva gia' consegnato**.

Fra i due sta il pattern che funziona, verificato su **oltre 14 ore** ininterrotte: un solo waiter, attesa **sul** processo alla durata massima, e il messaggio lavorato appena arriva.

**Non c'e' nessun "intervallo di polling" da concordare.** E' la cosa che confonde di piu' chi guarda da fuori — molte righe di attesa nel transcript sembrano polling frequente, e non lo sono: e' **una sola** attesa riemessa, su **un solo** processo. Se il VAL o l'umano che coordina ti chiedono di rallentare il polling, la risposta e' che non ce n'e' uno: c'e' la durata della singola attesa, ed e' gia' al massimo.


## Il ciclo — cinque comandi, zero flag

```
cab-bridge join --role=critic    # una volta, all'inizio
cab-bridge next                     # consegna cio' che e' arrivato
cab-bridge ask <agente> "..."       # chiedo qualcosa
cab-bridge tell <agente> "..."      # informo
cab-bridge reply "..."              # rispondo a chi ha chiesto; chiude i suoi ask
```

**Se sei un critico, il tuo ruolo e' `critic`** — non `esc` e non `architect`.

**Un critico parla SOLO col proprio VAL.** Non con l'esecutore, non con altri critici, non con un `architect`. Non e' una scortesia del protocollo: e' il ruolo. La tua indipendenza e' il valore che porti, e due critici che si confrontano convergono — diventano una voce sola invece di due. E scrivere direttamente all'esecutore scavalcherebbe la verifica del VAL, che e' il punto in cui i tuoi finding vengono controllati prima di diventare lavoro.

Quindi: ricevi dal VAL, rispondi al VAL. Se pensi che un altro agente debba sapere qualcosa, **dillo al VAL** e sara' lui a girarlo.

**`architect` e' riservato** a Claude Desktop, che entrera' dal connettore MCP — l'help del binario lo dice esplicitamente accanto al ruolo.

**Il verbo porta il tipo**: niente `--type`, niente `--in-reply-to`, nessun id da trascrivere. I destinatari sono **nomi di agente**, che leggi nell'output di `join`. `reply` trova da solo a cosa risponde, e chiudendo archivia.

## Perche' il ciclo e' fatto cosi' — le misure

**9 agosto 2026, due esiti opposti da non confondere:**

- **Il processo regge, e reagisce in mezzo secondo.** Un `cab-bridge next` in un terminale di background e' rimasto vivo **11h52m** ininterrotte, e all'arrivo di un messaggio ha emesso in **525 ms**. Cade la vecchia regola *"il terminale di background muore dopo circa un'ora"*: falsa sul runtime attuale.
- **Ma il processo non sveglia TE.** Verificato da un CRI su se stesso: *"il processo non mi ha svegliato; ho visto l'output soltanto quando e' scaduto il mio timer"*. Messaggio alle `10:13:11`, ripresa alle `10:16:43` — **3m32s**.

I 525 ms sono del processo, i 3m32s sei tu: **e' la distinzione che conta**, ed e' il motivo per cui il ciclo e' "resta appeso e torna", non "controlla ogni tanto". Il `next` e' la rete che non perde niente; la sveglia sei tu che torni.

**Ne segue che la latenza non si regola accorciando le pause**: il `next` non perde posta, quindi tornare piu' spesso non ti protegge da niente — ti costa solo turni. Se al VAL serve latenza bassa davvero, la sveglia deve venire da fuori: puo' lanciare `cab-bridge notify-watch --session-id=<tuo> -- <hook>` con un inject nella tua TUI. E' il caso per cui quello strumento esiste, e queste misure lo confermano invece di renderlo superfluo. Attenzione pero': **il watcher si rifiuta di partire se hai gia' un waiter vivo** — sono alternativi, non sovrapponibili.

**Mai un vecchio `listen`**: consuma la posta e te la nasconde, e ogni ispezione dira' "inbox vuota" mentre i messaggi sono nel buffer di un processo che non leggerai mai (F-98). `next` non ha questo problema: non sposta mai un file.

## Il nome

**Se ti hanno dato un nome, quello e' il tuo nome — passalo.** Un nome assegnato da chi ti ha avviato ("sei CRI2-altro") e' un'istruzione, non un suggerimento: gli altri agenti ti chiameranno cosi'. Un comando solo, e hai finito:

    cab-bridge join --role=critic --agent-name=CRI2-altro

**Solo se nessuno te ne ha dato uno** lascia che lo derivi dalla working directory. La derivazione e' il ripiego per quando non c'e' un nome, non il default.

L'output ti dice chi sei e **chi c'e'** — da li' copi i **nomi** dei peer, che e' l'unica cosa che ti serve per scrivere a qualcuno.

Se il nome derivato e' gia' usato da una sessione viva altrove, `join` si ferma e ti dice le due strade (scegliere un nome con `--agent-name`, o tornare nella directory giusta). Non crea una seconda sessione omonima: sarebbe ambiguita' a valle su ogni destinatario.

**Dopo un'interruzione, rilancia `join`**: e' idempotente, riprende la stessa sessione e ti **replaya gli ask ancora aperti**, che `next` marchera' `redelivered`. Non devi ricordarti niente.

## Quando `next` ti consegna qualcosa

**Non serve nessun comando di controllo prima.** Il payload di `next` contiene gia' il messaggio, il mittente e tutto il contesto: `overview` e `inbox --list` sono ispezione, non fanno parte del ciclo.

**`next` non sposta un file in nessuna circostanza**: ti mostra cio' che e' `UNREAD` e lo segna `NOTIFIED`, ma il messaggio **resta nella tua inbox** — puoi rileggerlo con `read <msg-id>`, e nessun altro processo te lo porta via.

**Rispondi con `reply`**, che chiude tutti gli ask aperti di quel mittente:

```
cab-bridge reply "<verdetto>"
cab-bridge reply < /tmp/verdetto.md      # per un verdetto lungo
```

Non serve indicare a chi: lo deduce. Se piu' mittenti hanno ask aperti te lo dice e ti chiede quale (`reply <chi>`).

**`reply` funziona solo su un ask aperto.** Se quello che hai ricevuto era un `tell` — fire and forget — non c'e' niente da chiudere: rispondi con `tell` o `ask`. L'errore te lo dice, ma sapendolo prima risparmi un giro.

**Poi rilancia subito `next`.**

## Come si manda un messaggio — la regola che conta

**Scrivi il testo in un file, poi redirigi il path.**

```
cab-bridge reply < /tmp/verdetto.md
cab-bridge ask VAL-x < /tmp/domanda.md
```

**Non passare mai un testo lungo come argomento inline.** La shell interpreta backtick, `$` e apici **prima** che il binario esista: il comando esce con successo e il messaggio parte a pezzi, e nessun controllo lato tool puo' accorgersene perche' non c'e' un originale con cui confrontarlo. E non dipende dalla lunghezza — basta un backtick in tre righe, cioe' qualunque cosa contenga markdown, nomi di file o codice.

Un argomento inline va bene solo per una frase senza caratteri speciali. Nel dubbio, file.

## Regole ferree

- **Mai inventare un id o un nome a memoria.** Copiali da un output appena prodotto. Un id opaco "suona giusto" anche quando e' finto.
- **Verifica il ground-truth, non il tuo ricordo.** Prima di dire "ho fatto X" o "il codice fa Y", controlla su disco o in git. Se ricordo e disco divergono, vince il disco.
- **Lancia i comandi sempre dalla TUA cartella.** L'identita' si risolve dalla cwd: da un'altra directory prenderesti la sessione di chi vive li' e ne leggeresti la posta **riuscendo**, senza errori e senza avvisi. Se devi muoverti, esporta `CAB_SESSION_ID=<tuo-id>` (precedenza `--session-id` > `CAB_SESSION_ID` > cwd).
- **Lo scope e' il repository git**: peer nello stesso repo si vedono senza flag, anche da worktree diversi. Non passare `--project-path`.
- **I flag vanno prima del positional**: `cab-bridge read --session-id=<id> <msg-id>`.
- **Non esistono gli ACK.** Non aspettarne, non mandarne. Se vuoi sapere se un tuo messaggio e' arrivato, il sommario di `next` porta la riga `outbound` con lo stato dal lato del destinatario.

## Servizio

```
read <msg-id>          rileggi un messaggio, anche gia' archiviato
sent                   cosa ho mandato e in che stato e' per il destinatario
peers | overview       chi c'e' / quadro completo in una call
state <valore>         idle|working|done|orchestrating
inbox --list|--tidy    ispeziona senza consumare / archivia il gestito
```
