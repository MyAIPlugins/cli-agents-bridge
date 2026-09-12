---
name: cab-bridge-awareness
description: Uso operativo di cab-bridge per il ruolo VAL (orchestratore) e per chi coordina piu' agent — il ciclo a cinque comandi (join/next/ask/tell/reply), il modello mailbox dove next non consuma mai e solo reply archivia, il metodo unico di invio, la regola di autorita', il riarmo strutturale, e la verifica del ground-truth. Usa quando coordini due o piu' sessioni di agent sulla stessa macchina.
---

# cab-bridge operativo

Il binario e' su `$PATH`. Le sessioni vivono in `~/.claude/cli-agents-bridge/sessions/<id>/`.

## Ingresso: DUE comandi, poi basta

    1)  cab-bridge join --role=val --agent-name=<il nome che ti ha dato chi ti coordina>
    2)  CAB_SESSION_ID=<l'id stampato al passo 1> cab-bridge next     (in background)

Il passo 1 stampa **chi sei e chi c'e'**: non ri-derivare quell'informazione con `overview`, `peers` o `inbox --list`. E' la stessa risposta due volte, e un agente fresco che la ricontrolla brucia turni prima ancora di ricevere un compito.

**Se sei il VAL**: `cab-bridge state orchestrating` (una volta) ti esenta dall'heartbeat, perche' fra un messaggio e l'altro non stai in `next`.

**Se stai ingaggiando un peer Codex**, la prima cosa che gli chiedi e' **il suo `CODEX_THREAD_ID`** — non un goal persistente. Sta fermo, non costa niente, e lo svegli tu: vedi **«SVEGLIARE UN PEER CODEX»** piu' sotto, che e' il modo di lavorare dal 12/09.

⚠️ **Il vecchio modello — goal persistente + `next` appeso — e' il RIPIEGO**, e lo usi solo se non puoi svegliarlo. Sappi cosa compri: senza goal **e** senza risveglio, un peer che fa `join` + `next` e si ferma **crede di essere in ascolto e non lo e'** e resta muto per sempre (successo il 9 agosto); ma col goal paga **~130k token/ora di silenzio**. **Con l'id dichiarato non paghi nessuno dei due.**

📌 E se un peer risponde una volta e poi tace: prima chiedevamo *"hai il goal?"*. Adesso la domanda e' **"mi hai dato il tuo thread id, e io ti ho svegliato?"** — perche' il silenzio di un peer fermo e' **indistinguibile** da quello di un peer rotto, e la differenza sta dalla tua parte.

**La mappa dei vendor, tutta misurata il 9-10 agosto sullo stesso bridge** (latenza = dal file sul disco al riarmo del listener, cioe' **il modello**, non il processo):

    Claude Code    risveglio nativo          run_in_background, zero costo a vuoto
    Codex          🟢 RISVEGLIO ESTERNO      `codex queue --thread <id>` — DIMOSTRATO 12/09, col
                   (dalla 0.154.0)           controllo umano: agente FERMO, zero costo mentre aspetta
                   ⚠️ ma lo sveglia una      se invece POLLA: ~60 turni/ora — UNO AL MINUTO — a ~2.150
                   PERSONA: il watcher non   token l'uno ⇒ ~130k token/ora di silenzio
                   esiste su Windows
    Antigravity    risveglio nativo          3,1s · zero costo · nessun goal, nessuna attesa
                   (Reactive Wakeup su fine task in background)
    Claude Desktop nessuna shell             non raggiungibile finche' non c'e' F-72 (MCP)

🔴 ⭐ **LA RIGA DI CODEX DICEVA «~1 turno/ora col config alzato»: ERA FALSA, e la cura che prometteva
non esiste** (12/09, misurato da CRI su richiesta dell'umano, che l'aveva visto ciclare).

`[E]` I tre dati, dal suo referto:

    chiede a `write_stdin`   `yield_time_ms = 3600000`  (il valore alto, col config gia' corretto)
    ottiene                  la cella torna SUBITO, e lui poi chiama `functions.wait` a **60 000 ms**
    chi lo fa tornare        **lui stesso**: non il processo, non il runtime, non il bridge

⇒ **La causa e' un'istruzione superiore del runtime di Codex** — *«evitare attese bloccanti oltre
60 secondi»* — che `[E]` **non sta in una skill, non in un `AGENTS.md`, e non e' modificabile da noi**
(cercata il 12/09 in entrambi i posti: assente).
⇒ L'agente spezza l'attesa in tranche da 60 s, e **ogni tranche costa un turno** perche' il goal
persistente ne genera uno nuovo. Il valore nel config **non viene mai raggiunto**.

⭐ **IL CONTO VERO, misurato dall'interno da un CRI (gpt-6-astra) il 12/09 su mia richiesta** — ed e'
il primo numero onesto che abbiamo, perche' un agente puo' leggere i propri contatori e noi no:

    due turni vuoti consecutivi   write_stdin → processo vivo, output vuoto, a 60,0107 s e 60,0069 s
    contatori ai tre ingressi     43.085 → 45.246 → 47.392 token
    costo per turno vuoto         **2.161 e 2.146 token**  ⇒  ~4.300 token in due minuti di silenzio

⇒ **~1 turno al minuto, cioe' ~60 l'ora e ~130.000 token l'ora per non fare niente.** La riga
precedente diceva *"~30 turni/ora"*: **sbagliata di un fattore due**, e nella stessa casella dove una
riga prima ancora prometteva una cura inesistente. *Questa casella ha mentito due volte: la prossima
volta che qualcuno ci scrive un numero, lo faccia misurare a chi sta dentro il runtime.*

⚠️ **E la sua precisazione vale piu' del numero**, perche' sposta la causa: *«non ho misurato un clamp
della macchina e non attribuisco il minuto al runtime: **sono io che devo richiedere 60000**»*. ⇒ Non
e' un tetto meccanico che tronca un'attesa lunga — e' **un'istruzione che l'agente obbedisce** quando
sceglie la finestra. La differenza non e' accademica: un clamp si aggira solo dall'esterno, mentre
un'istruzione si puo' nominare, e un giorno forse rilassare, nel posto giusto. *Ed e' la domanda «di
quale oggetto e' proprieta' questo dato?» applicata bene: il costo e' del RUNTIME, non del modello —
un modello piu' capace non tocca quella riga.*

⛔ **Quindi NON servono, e non vanno riprovati**: alzare `background_terminal_max_timeout` (fatto,
inerte), aprire il repo del bridge (il limite e' a monte), o chiedere all'agente di aspettare di piu'
(non puo').

🟢 **La leva che funziona oggi**: **un peer Codex non si tiene in ascolto quando non ha un task.** Lo
si riattiva quando c'e' materiale — riaccenderlo costa molto meno di ~130k token all'ora di silenzio.
⇒ E chi orchestra lo sappia **prima** di ingaggiarlo «così è pronto»: con Codex, *pronto* si paga.
⇒ In pratica, col peer in pausa: **scrivigli con `ask`, non con `tell`.** Un ask che non chiude resta
aperto e il `next` al risveglio lo **riconsegna** (`redelivered`); un `tell` che il suo listener ha
gia' marcato `NOTIFIED` mentre lui dormiva non riappare da solo. *(`[L]`, dedotto dal modello mailbox,
non ancora misurato: l'`ask` e' comunque la forma giusta per un compito, quindi non costa nulla.)*

## 🟢 SVEGLIARE UN PEER CODEX — dimostrato il 12/09, e cambia come si ingaggia

**Non serve piu' scegliere fra «pronto e caro» e «spento e lento».** Il peer sta **fermo** — niente
goal, niente `next` appeso, **zero costo** — e lo svegli tu quando c'e' materiale:

    codex queue --thread <il suo CODEX_THREAD_ID> --message "hai posta sul bridge"

**Come ottieni l'id**: **glielo chiedi, e lui lo legge dal proprio ambiente** — `CODEX_THREAD_ID` e
`CODEX_SESSION_ID`, che coincidono col `thread_id` del suo file di rollout. ⛔ **Non cercarlo tu con
`codex agents`**: risponde `stdin is not a terminal`, perche' interroga un **app-server daemon**, e
`~/.codex/sessions/` contiene rollout salvati (morti compresi), non chi e' vivo adesso.

`[E]` **Il test che lo dimostra, e che va rifatto uguale se un giorno dubiti**: il peer era **fermo**,
l'ho svegliato dall'esterno, **e nessun umano ha toccato la sua TUI**. *Un risveglio che avviene solo
quando qualcuno digita e' indistinguibile da nessun risveglio*: senza quel controllo il test non vale.

### ⚠️ Cosa NON e' ancora risolto — leggilo prima di fidartene

- **Lo sveglia una PERSONA**, non un meccanismo: il watcher (`notify-watch`) **non esiste su Windows**.
  ⇒ Se ingaggi un Codex e poi ti dimentichi di svegliarlo, **resta muto per sempre** — e dal suo lato
  e' indistinguibile da «il VAL non ha ancora finito». **Diglielo quando lo ingaggi.**
- **`queue` mentre lavora**: mai misurato. Entrambi i risvegli osservati sono partiti **da fermo**, e
  la documentazione non dice se sia FIFO, se interrompa, o se si perda.
- **L'id sopravvive a `/clear`, compact, resume?** Dedotto dalla doc, **non provato**. ⭐ *Un UUID
  ancora valido identifica una STORIA, non certifica che qualcuno la stia ESEGUENDO.*
- **Chiunque sulla macchina** conosca l'id puo' svegliare quella sessione.

📌 E i quattro difetti del disegno di un watcher automatico, gia' trovati da un critico: dedup che
tratta `exit 0` come successo definitivo (**niente retry**), paginazione che lascia senza risveglio
tutto cio' che sta oltre la prima pagina, wake duplicati che fanno rinascere un `next` bloccante su
inbox vuota, e la race fra «sto lavorando» e «e' arrivata posta». ⇒ **Principio: notifiche
RIPETIBILI, non un claim exactly-once che non si puo' sostenere.**

⚠️ E la lezione sul documento: quella riga prometteva una cura (`col config alzato`) che **nessuno
aveva verificato sul runtime corrente**. l'umano che coordina l'ha smontata guardando la TUI ciclare, non leggendo.
⭐ Una misura del **9-10 agosto** ha continuato a descrivere il costo di un runtime che nel frattempo
era cambiato: *un numero di ambiente scade come un numero d'inventario, e non fa rumore quando muore.*

**Il bridge e' identico per tutti**: cambia solo il runtime, e con esso la porta. Non aggiungere logica al bridge per compensare il runtime di un vendor — aggiungi una porta esterna. E prima di promuovere un peer nuovo, fai il **test di controllo**: gli mandi un messaggio e **nessun umano tocca la sua TUI**. Un risveglio che avviene solo quando qualcuno digita e' indistinguibile da nessun risveglio, ed e' esattamente cosi' che scoprimmo che Codex non si svegliava.

Dato dal campo (9 ago): tre agenti freschi hanno speso **sei comandi a testa** dove ne bastavano due — `join`, poi `overview`, poi `inbox --list`, poi `peers --all-scopes`, poi due `--help` — cercando conferme di cose che `join` aveva gia' detto. *(Episodio **pre-F-116**: oggi `peers --all-scopes` non e' piu' ridondante — e' l'unico modo di vedere gli altri repository, che `join` non mostra. Gli altri quattro restano duplicati.)*

## Il ciclo — cinque comandi, zero flag

```bash
cab-bridge join --role=val      # una volta, all'inizio
cab-bridge next                 # poi all'infinito: l'unico comando del ciclo
cab-bridge ask <agente> "..."   # chiedo — resta aperto finche' non rispondono
cab-bridge tell <agente> "..."  # informo — non aspetta risposta
cab-bridge reply "..."          # rispondo a chi ha chiesto; chiude QUELLA consegna
```

**Il verbo porta il tipo.** Niente `--type`, niente `--in-reply-to`, nessun id da trascrivere. I destinatari sono **nomi di agente**: quelli del tuo repository li leggi nell'output di `join`, che mostra **solo il tuo scope** (`join.go:111`). `reply` trova da solo a cosa sta rispondendo.

**Per un agente di un ALTRO repository** (F-116, v0.8.0): `<nome>@<progetto>`, dove `<progetto>` e' la colonna `SCOPE` di `peers --all-scopes` — **copiala come stampata, e non aggiungere apici tuoi**:

    cab-bridge tell VAL-altro@altro-progetto < /tmp/msg.md

**La regola precedente diceva "quotalo sempre", ed era insufficiente.** Copre lo spazio e cade sull'apostrofo: un repository chiamato `O'Brien's Tools` chiude l'apice che hai aperto tu, e il resto della riga torna a essere sintassi shell (`zsh: unmatched '`). E' un nome di directory che qualcuno puo' avere davvero.

**Adesso lo fa il tool**: dalla v0.9.0 `peers` rende la colonna `SCOPE` gia' sicura come singolo argomento — **solo dove serve**, quindi il caso ordinario e' identico a prima — e `peers --json` resta grezzo per chi fa parsing. Se aggiungi apici tuoi sopra a un valore gia' reso, lo **rompi**.

🟢 **F-135 È CHIUSO dal 12/09 pomeriggio — `fromAddressShellArg` verbatim funziona anche su Windows,
dal binario `v0.9.0-28-g9298628`.** `[E]` Verificato nei due versi: il VAL del bridge ha mandato un
messaggio **col path** e io gliene ho rimandato uno **col path**, entrambi arrivati.
⇒ **Il workaround qui sotto NON serve più**, ed è conservato solo perché la firma dell'errore resti
riconoscibile se un giorno si lavora su un binario più vecchio.
⛔ **Prima di applicarlo, guarda la versione**: `cab-bridge version`. Sotto `-28-g9298628` vale; sopra
no.

<details>
<summary>La firma del difetto, per chi gira su un binario precedente</summary>

🔴 ⛔ **SU WINDOWS `fromAddressShellArg` NON FUNZIONAVA — si usava il NOME del progetto** (12/09, F-135).
`[E]` `next` consegna come indirizzo del mittente il **path assoluto**
(`'VAL-bridge@<questo-repo>'`). Usato **verbatim, come questa skill prescrive**, il
`tell` fallisce:

    no agent named "VAL-bridge" in project "<questo-repo>"
      — projects with agents: altro-progetto, **cli-agents-bridge**

⇒ Il binario **elenca fra i progetti validi proprio quello che ha appena rifiutato**: è la firma.
`[L]` La causa (dal VAL del bridge): `recipient.go:72` decide *«è un path assoluto?»* con
`strings.HasPrefix(hint, "/")` ⇒ su Windows `C:\…` **non comincia per `/`**, finisce nel ramo
basename, e `Base()` non combacerà mai col path intero.

🟢 **Finché il binario non è aggiornato, si usa `<nome>@<NOME-progetto>`** — la colonna `SCOPE` di
`peers --all-scopes`, non il path.
⚠️ **Non è universale**: il nome nudo è un basename, quindi con due repo dalla stessa cartella finale
(`…/a/payload` e `…/b/payload`) diventa ambiguo — e la via d'uscita prevista per quel caso è
**proprio il path**, cioè il ramo rotto. Oggi non morde perché i basename in uso sono distinti.
📌 Su Unix la riga originale resta **vera**: il ramo `HasPrefix("/")` funziona. È un difetto di
Windows soltanto.
📌 Fix in `feat/windows-lotto1b`, **mergiato e installato il 12/09** (`v0.9.0-28-g9298628`).

</details>

⭐ **E la lezione che resta, quando il difetto non c'è più**: fra lo `[E]` di una segnalazione e il
binario che gli agenti usano davvero ci sono **push → CI → merge → REINSTALL**, e ognuno dei quattro
può fermarsi. `[E]` Qui il fix era scritto **da nove giorni** e fermo: a portarlo in PATH è stato un
caso reale che l'ha incontrato, non la sua correttezza.
⇒ **Mergiato non è installato**, e la sola cosa che conta è `cab-bridge version`.

*Il principio, che vale oltre questo campo*: **un algoritmo di quoting non si mette nelle mani del lettore.** Nessuna regola applicata a mano regge tutti i casi — noi ne abbiamo scritta una che sembrava completa, e un apostrofo l'ha smontata.

**Cross-progetto e' `val` -> `val`, ENTRAMBI i lati** (`verbs.go:283`): non basta che a scrivere sia un `val`, deve esserlo anche chi riceve. Quindi un `critic` che ha qualcosa per l'altro repo lo passa al proprio VAL, ed e' il VAL a parlare col VAL di la'.

**Il ramo della risposta e' deliberatamente esente**: se un ask cross-progetto e' arrivato a te, un `reply` **nudo** torna indietro da solo — inferisce il mittente, non gli serve nessun indirizzo. E' una capacita' one-shot su quell'ask, non un canale che resta aperto: per scrivergli **tu** per primo piu' tardi serve di nuovo il token qualificato, e `next` te ne consegna **due**: **`fromAddressShellArg` e' quello da incollare in un comando**, gia' sicuro come singolo argomento; `fromAddress` e' lo stesso valore grezzo, per confrontare o parsare. **Non aggiungere apici a nessuno dei due** — se il progetto contiene un apostrofo, gli apici che ci metti tu si rompono, e sul primo sono comunque gia' li'.

## Modello mentale — leggilo prima

Quattro stati, e **un solo comando sposta un file**:

| Stato | Significato |
|---|---|
| `UNREAD` | arrivato, mai mostrato |
| `NOTIFIED` | `next` te l'ha mostrato — **e' ancora nella tua inbox** |
| `REQUEUED` | te l'avevano mostrato, non l'hai chiuso, **sta tornando da te** |
| `ARCHIVED` | chiuso, spostato in `processed/` |

- **`next` non sposta un file in nessuna circostanza.** Svegliarsi e consumare sono atti separati: e' il senso di tutto il modello. Nessun processo puo' piu' mangiare la posta di un agente.
- **Solo `reply` archivia, e archivia UNA consegna** (F-109, 10 agosto): chiude gli ask che quel mittente ti ha mandato **in una singola pagina di `next`**, non tutto quello che ha di aperto. Quello che e' arrivato dopo resta aperto, viene **nominato sotto la tua risposta**, e **torna in coda**: il prossimo `next` te lo riconsegna marcato `redelivered`. La conferma resta un effetto collaterale del lavoro, mai un rito — ma il lavoro che confermi e' la consegna che ti e' stata mostrata.
  *Perche'*: `NOTIFIED` significa **"il processo `next` l'ha stampato"**, non "l'hai letto". Riarmi prima di lavorare (giusto), e un messaggio che arriva **mentre scrivi** e' `NOTIFIED` senza che tu l'abbia visto — prima la tua risposta lo chiudeva lo stesso, e un *"fermati, NON fare A"* poteva risultare risposto da un *"fatto A come chiesto"*. Incidente reale, non ipotesi.
  *Limite dichiarato*: senza un ACK di lettura non e' impossibile per costruzione — la pagina piu' vecchia puo' essere proprio quella che non hai letto. Garantito e' **al massimo una consegna per risposta** e **mai in silenzio**: tu vedi cosa hai chiuso e cosa resta, il mittente vede `closes` sulla risposta e `requeued` in `sent`.
  **Quindi leggi le righe sotto la tua risposta.** Se dicono che qualcosa e' rimasto aperto, non e' un errore: e' un messaggio che non avevi ancora visto, e sta tornando.
- **`next` non ha finestra**: aspetta finche' non arriva qualcosa. Se viene interrotto lo dice (`"status":"interrupted"`), e nel record porta i tuoi `outbound` aperti.
- **`emitted` NON e' il via libera.** `next` stampa **due** record: la pagina (`emitted`) e poi l'esito (`committed` / `not-committed`). Il secondo puo' **revocare** il primo — se un'altra istanza ti ha sfrattato fra i due, `not-committed` ti dice di ignorare la pagina appena letta. Leggi l'output intero, e lavora solo su `committed`. Vale anche quando lo giri a un altro agente: se gli mandi il contenuto di una pagina non confermata, gli fai fare lavoro da buttare.
- **Dopo un compact**: rilancia `join` — replaya i tuoi ask ancora aperti, e `next` li marca `redelivered` inline. Trattali normalmente.
- **Non esistono gli ACK.** Per sapere se un brief e' arrivato, il sommario di `next` porta la riga `outbound`: a chi, da quanto, in che stato dal lato loro.

## METODO UNICO DI INVIO — `Write`, poi redirigi il PATH

    1) Write  -> /tmp/msg.md          (il contenuto NON tocca la shell)
    2) cab-bridge ask <agente> < /tmp/msg.md

**Perche' `Write` e non un messaggio inline**: il contenuto scritto con `Write` viaggia come **parametro di un tool call** e non attraversa mai un interprete. La shell vede solo il PATH, e un path non contiene backtick. E' l'unica catena in cui il testo non incontra un interprete in nessun punto.

Ripiego se sei gia' dentro bash: **heredoc col delimitatore QUOTATO** (`<<'EOF'`), che disabilita ogni interpretazione. Ma `<<EOF` senza apici interpreta tutto: un carattere di differenza, stesso fallimento silenzioso. Per questo e' ripiego, non metodo.

**Mai** passare il testo come argomento, nemmeno per due parole. Un metodo solo: niente da decidere, nessun modo di sbagliare.

**E vale per OGNI comando che prende testo, non solo per il bridge.** Il caso che mi ha fregato dopo aver scritto questa regola: `git commit -m "... ~0.6ms su `+'`peers`'+` con 4 sessioni ..."` — la shell ha eseguito `peers` come comando e il messaggio e' finito nella storia con un buco al suo posto, gia' pushato e non riscrivibile. **Per i messaggi di commit: sempre `git commit -F <file>`** (scritto con `Write`), oppure heredoc col delimitatore quotato. Il rischio non e' del bridge: e' di qualunque testo che passa da una riga di shell.

**Perche' non basta "stai attento".** La shell interpreta backtick, `$` e apici **prima** che il binario venga invocato: quando `cab-bridge` parte il contenuto e' gia' corrotto, e **nessun controllo lato tool puo' accorgersene** — non esiste un originale con cui confrontarlo. Il comando esce con successo e il messaggio parte a pezzi.

**Non dipende dalla lunghezza.** Basta un backtick in tre righe. La correlazione e' col CONTENUTO — markdown, nomi di file, snippet — cioe' tutto cio' che un agente tecnico scrive.

**Il dato che ha prodotto la regola (8 ago):** sei messaggi partiti mutilati o vuoti in una sola sessione, tutti dal VAL, tutti mentre coordinava l'arco che elimina quel difetto. Uno con contenuto VUOTO, consegnato regolarmente; in un altro un backtick ha invocato `join`, il comando Unix. Il VAL lo sapeva dalla mattina e ci e' ricascato quattro volte. **La disciplina non ha funzionato: solo togliere la scelta funziona.**

Se ricevi un messaggio con buchi dove dovrebbero esserci nomi di file o comandi, e' questo — chiedi il re-invio, non indovinare.

## REGOLA DI AUTORITA' — il VAL non congeda nessuno

**Solo l'umano che coordina chiude una sessione.** Il VAL non ha il potere di congedare ESC o i critici, e non deve proporlo di propria iniziativa.

Il difetto ricorrente: il VAL vede arrivare a termine il compito che HA ASSEGNATO e conclude che il lavoro sia finito — ma il piano completo lo conosce solo l'umano che coordina, e quasi sempre gli step successivi esistono e non sono ancora stati comunicati. **"Hanno finito il compito che gli ho dato" NON significa "hanno finito".**

Errore reale (8 ago): alla domanda "quando posso disattivare i CRI?" il VAL ha risposto "adesso, entrambi". Sono serviti subito dopo e hanno prodotto 2 P0 e 7 finding, inclusi difetti che il gate verde non vedeva.

In pratica:
- **Mai** dire "puoi spegnerli", "non servono piu'", "abbiamo finito". Di' invece: *"CRI e' senza task assegnato; se hai altro in programma posso ingaggiarlo su X, altrimenti resta a disposizione."*
- Un agente inattivo **non costa nulla**; riaccenderlo costa contesto e tempo. L'asimmetria dice di lasciarlo vivo.
- Se non sai quali siano i prossimi passi, **chiedi**. L'assenza di istruzioni non e' un segnale di completamento.
- Vale anche per il proprio ascolto: non smettere di ascoltare perche' "sembra tutto chiuso".

## RIARMA PRIMA DI LEGGERE, nella stessa chiamata

Il difetto piu' frequente dell'orchestratore non e' tecnico: **dimentica di rimettersi in ascolto** e perde i messaggi senza accorgersene. ESC e i critici non ce l'hanno, e non per merito: il loro ciclo e' chiuso (ricevi → lavora → rispondi → riarma) e il riarmo e' l'ultima azione ovvia di una sequenza sola. Il VAL, tra un messaggio e l'altro, legge, decide, aggiorna docs, committa, scrive a piu' agent: il riarmo diventa UNA delle N cose. **E' un difetto di struttura del ruolo, non di attenzione.**

> Appena arriva la notifica che `next` e' terminato, il PRIMO comando e' il riarmo, e va emesso nello **STESSO blocco di tool call** della lettura del messaggio. Mai leggere prima e riarmare dopo.

Corollario: se ti accorgi di essere fuori ascolto, **riarma prima di fare qualunque diagnosi**. La diagnosi non scade, i messaggi in arrivo si'.

**E ancora prima: fissa `CAB_SESSION_ID`, sempre.** Il lavoro del VAL richiede di entrare nelle directory degli altri — leggere il `git log` di un worktree, ispezionare il repo di un critico — e **la cwd della shell persiste fra una chiamata e l'altra**. Da quel momento ogni comando id-free non e' piu' tuo: risolve la sessione di chi vive li', **riuscendo**, senza errori e senza avvisi.

Errore reale (8 ago, due volte nella stessa sera): un `cd` nel worktree di ESC per un `git log`, e i tre "riarmi" successivi hanno armato un `next` sulla **sessione di ESC**, rubandogli l'ownership dell'attesa — mentre io credevo di essere in ascolto sulla mia. La prima volta avevo perfino dichiarato rotta una feature sana perche' cercavo i miei messaggi nell'inbox di un altro.

    CAB_SESSION_ID=<il-tuo-id> cab-bridge next

`next` non accetta flag, quindi la variabile d'ambiente **e' l'unico modo** di fissare l'identita' — ed e' esattamente il caso per cui e' stata costruita. Non e' una precauzione da paranoici: e' struttura del ruolo, come il riarmo. Chi ha un ciclo chiuso in una sola directory non ne ha bisogno; l'orchestratore, che gira per le directory altrui di mestiere, si'.

**E non troncare i messaggi che leggi.** Se usi uno script per estrarre il `content` dal payload di `next`, non mettere un limite di caratteri: un verdetto lungo arriva a meta' e tu agisci su meta'. Successo il 9 ago — quattro finding di un NO-GO, ne ho letti due e ho girato quelli all'esecutore come se fossero tutti; i due persi contenevano il migliore. **Per un messaggio lungo usa `cab-bridge read <msg-id>`**, che stampa tutto.

**E non incanalare `next` in `tail` o in una pipe che bufferizza.** Errore reale (8 ago): un `next` in background con `| tail -60` ha trattenuto due report per venti minuti — `tail` non emette nulla finche' non riceve EOF. Il VAL ha letto un file vuoto e ne ha concluso che un critico non avesse lavorato. Redirigi diretto, senza filtri.

## Verifica il ground-truth, non il resoconto

**La lezione madre.** Un resoconto — tuo, di un agente, di un tool — non e' lo stato del sistema. Prima di dichiarare qualcosa, guarda il disco: `ls` sull'inbox, `git log`, il file.

Vale in modo speciale **per il VAL**: in una sola sera ho dichiarato rotto un componente sano, sbagliato due conteggi nello stesso resoconto, e concluso che un critico non avesse lavorato mentre il suo report era gia' sul mio disco da venti minuti. **Il disco vince sul resoconto anche quando il resoconto e' di chi tiene il gate.**

Corollario per i piani: un'affermazione sul comportamento del codice dentro un piano ("X tiene il lock") va **verificata sul codice** prima di ratificarla, mai lodata sulla fiducia.

**E la faccia rovesciata, che e' altrettanto seducente: il ROSSO che viene spiegato via.** Un fallimento che sparisce al secondo tentativo **non dice "ambiente"**: dice *non deterministico* — e il non determinismo, visto da fuori, e' esattamente l'aspetto che ha una race. La frase *"un rosso che sparisce cambiando ambiente e' ambiente, non codice"* classificherebbe come contesa un difetto che si manifesta solo sotto carico, cioe' la categoria piu' costosa da trovare dopo. **Cosa li distingue sta nel log e costa zero guardarlo**: contesa vera = il processo *stava progredendo* ed e' stato ucciso dal cap; difetto vero = morto, o appeso **senza progredire**. Passati due giorni su *"verde che non dimostra niente"*, questa e' la stessa struttura rovesciata — la spiegazione e' plausibile, quindi non si verifica.

**Nota di ambiente che rende il gate meno valido, non solo piu' lento** (10 ago): la macchina ha **8 core logici**, e tre agenti che compilano insieme portano il carico oltre 10 — ognuno dimensiona i propri worker sul numero di core, quindi tre build sono ~24 slot su 8. Il meccanismo pero' **non e'** quello che sembra, e la versione sbagliata l'avevo scritta qui: TSan **non** rileva le race osservando due accessi che collidono nel tempo — costruisce una relazione **happens-before** e segnala due accessi non ordinati fra loro anche a secondi di distanza. La contesa non nasconde una race in quel senso. Quello che fa davvero: *(1)* i test con **timeout** non arrivano a completarsi, quindi **il codice che conteneva la race non viene eseguito** — e cio' che non si esegue non si analizza; *(2)* i test che dipendono dal tempo diventano flaky, cioe' rossi spuri che costano piu' del gate. **La conclusione regge — un gate che gira da solo e' un gate migliore — ma per la seconda ragione, non per la prima.**

Quindi: se un gate rosso capita mentre altri stanno buildando, **rilancialo da solo prima di crederci**. E la leva giusta e' **`-p`, non `GOMAXPROCS`**: `-p` limita quanti *binari di test* girano insieme (processi separati, ognuno col suo GOMAXPROCS pieno, **stessa concorrenza interna**), mentre abbassare `GOMAXPROCS` toglie davvero interleaving all'esplorazione. Il default di `-p` e' GOMAXPROCS, quindi **un singolo gate lancia gia' otto binari su otto core**: non serve essere in tre perche' un gate assuma di possedere la macchina.

**Ordine di grandezza, misurato due volte** (ESC e io indipendentemente): il gate completo e' **~10,8 s reali, ~9 s di CPU**. Il VAL aveva scritto "~40 s" a memoria: sbagliato di quattro volte, e su quel numero aveva costruito l'idea di essere il consumatore dominante. Non lo e' — a saturare la macchina sono le **build** (`next`/turbopack/`tsc`), che durano **minuti**.

### Le quattro domande, prima di scrivere qualunque affermazione

Nate da dieci errori miei in una giornata. Non sono buoni propositi: sono quattro domande con una risposta, da farsi **prima** di premere invio su un brief, un documento o un messaggio all'umano.

**1. L'ho ESEGUITO o l'ho DEDOTTO?** E dillo esplicitamente, sempre, anche quando la deduzione e' buona. Ho scritto due volte che un residuo di sicurezza era "stretto", con un argomento corretto (*SC-7 tiene il base a 0700, quindi l'albero e' congelato*) che rispondeva a **un'altra domanda** — taceva su un link piantato PRIMA, cioe' la finestra esatta per cui il controllo esisteva. Riprodotto in trenta secondi da un revisore. **Un argomento plausibile non e' una verifica, e la differenza si vede solo provando.** Se non l'hai eseguito, scrivi che non l'hai eseguito.

**2. Di QUALE OGGETTO e' una proprieta' questo dato?** Il numero puo' essere vero, pulito e ripetibile, e riguardare la cosa sbagliata. Tre volte in un giorno: 525 ms erano la latenza del **processo** e l'ho attribuita al **modello**; "il terminale muore dopo un'ora" era vero su **una versione** e l'ho preso come proprieta' del runtime; "la cadenza emerge da sola" erano **i 10 minuti che l'umano che coordina aveva istruito**, dentro il goal di quell'agente. Firma comune: **osservo un sistema configurato e ne inferisco il default.** La doppia verifica non prende questo errore, perche' il dato regge a ogni controllo.

**3. Questo stato l'ho PRODOTTO io?** Non descrivere all'umano un mondo che non hai creato. Ho detto "resta il diff-gate di CRI" senza averlo chiesto — il critico ha pollato a vuoto per mezz'ora, e non aveva modo di distinguere *"il VAL non ha finito"* da *"il VAL mi ha dimenticato"*. Nella stessa famiglia: **gattato non e' mergiato, mergiato non e' installato.** Il binario in PATH e' l'unica cosa che gli agenti usano davvero.

**4. Sto descrivendo il CODICE o il DESIGN?** Quando scrivi un documento normativo diventi **tu** la fonte di cui qualcun altro si fidera' al posto del codice. Ho dichiarato un controllo di sicurezza "attivo" mentre stava su un branch e con un comportamento che nemmeno li' era quello descritto — un'ora dopo aver riscritto quello stesso file perche' conteneva affermazioni non sostenute dal codice. **Regola operativa: il documento si aggiorna DOPO il merge, mai prima.** Codice e documentazione atterrano insieme.

### Se mandi qualcuno a provare qualcosa di DISTRUTTIVO, dagli il sandbox nel brief

Non basta dire *"verifica sul codice"*. Un agente che prova comandi che cancellano lo fara' sulla macchina dove si trova, e **un `cd` in una directory di test non lo protegge**: il data dir di `cab-bridge` deriva da `$HOME`, non dalla cwd, e nessun output lo dice.

Costo reale (9 ago): un critico che provava un vettore d'attacco su `archive/` ha lanciato `cleanup --scope=global --force` senza `CAB_DATA_DIR`, fidandosi del proprio `cd`. **Tredici sessioni archiviate cancellate dal data dir di produzione.** Recuperate da uno snapshot, ma per fortuna, non per progetto. Il critico aveva **previsto quel difetto poche ore prima** e ci e' caduto lo stesso: sapere non basta, quando il comando non te lo ricorda nel momento in cui conta.

**Quindi nel brief, sempre, in una riga:** *"per qualunque prova che cancella o modifica, esporta `CAB_DATA_DIR` su una sandbox — la cwd non isola niente"*. E se il comando ha un `--dry-run`, nominalo.

E' una mitigazione, non il fix: il fix sta nel tool, che deve dire su cosa agisce prima di agire. Ma la riga nel brief costa una frase e copre l'agente che la skill non l'ha letta.

### Chiudi il giro con chi ti ha risposto

Un agente che ti ha mandato un verdetto **resta in attesa dell'esito**, e non ha modo di sapere se stai lavorando o l'hai dimenticato. Due volte in un giorno ho consumato il lavoro di un critico — cinque finding, un P0 riprodotto — e non gli ho risposto: dal suo lato era silenzio identico all'oblio.

Quando un finding e' chiuso, **digli cosa e' successo a ognuno**: cosa hai verificato tu, cosa hai mergiato, cosa hai scartato e perche'. Costa un messaggio e rende utile il prossimo giro; l'alternativa e' un critico che rifa' le stesse domande o smette di farle.

### Non leggere a meta'

Nessun `[:N]` sul contenuto di un messaggio, nessun `| tail` su un `next` in background. Ho girato **meta' di un NO-GO** a un esecutore credendo fosse tutto: i due finding persi contenevano il migliore. Per un messaggio lungo: `cab-bridge read <msg-id>`.

## "E il ramo accanto?"

La contromisura al difetto piu' costoso dell'arco v0.8. Quando una sintesi pre-validata ha ceduto, il ramo che reggeva era **sempre quello nominato nel finding**, e a cedere era il suo complemento: same-role verificato / cross-role no · `notified` potato / `replayed` no · `buildOverview` migrato / il renderer no · la passata che emette coperta / quella che si interrompe no.

Non e' che la verifica fosse superficiale: **si verifica il caso di cui si sta parlando, e il complemento non lo nomina nessuno.**

Forma con una risposta (altrimenti diventa un rito): *questo fix tocca un campo, un percorso o un ramo? allora — chi altro legge quel campo · quali sono gli altri percorsi di uscita · cosa c'e' nell'`else`*.

## Setup

- **Cwd distinte**: ogni agente parte dalla propria directory. In uno scope condiviso, `CAB_SESSION_ID=<id>` fissa l'identita' (precedenza `--session-id` > `CAB_SESSION_ID` > cwd). Il caso pericoloso non e' il comando che fallisce: e' quello che **riesce come qualcun altro**.
- **Lo scope e' il repository git** (git-common-root): un worktree collegato risolve al repo principale, quindi VAL alla root ed ESC in un worktree si accoppiano senza flag. Repo diversi **condividono lo storage** — il data dir di default e' unico — ma **discovery e routing non qualificato restano scope-locali**: `join` e `peers` mostrano solo il tuo, e un nome nudo risolve solo li'. `peers --all-scopes` scopre gli altri, `<nome>@<progetto>` li indirizza (F-116). Due assi distinti, e chiamarli entrambi *"vedersi"* e' il modo di confonderli. *(Difetto reale, 11 ago: questa riga diceva "restano isolati" il giorno dopo il merge che lo rovesciava, e un VAL dell'altro repo ha risposto in buona fede che non poteva inoltrare un messaggio. La feature era nel binario in PATH da un giorno; il buco era in questa riga.)*
- **Se l'umano ti ha dato un nome, quello e' il tuo nome.** `cab-bridge join --role=val --agent-name=VAL-altro`. Un nome assegnato e' un'istruzione, non un suggerimento: gli altri agenti ti chiameranno cosi', e i loro brief lo useranno. **Solo se nessuno te ne ha dato uno** lascia che `join` lo derivi dalla working directory (`VAL-escdir`) — la derivazione e' il ripiego, non il default. *(Errore reale, 9 ago: le skill dicevano "il nome si deriva, non serve sceglierlo" e tutti e tre gli agenti hanno ignorato il nome che l'umano che coordina aveva appena dato loro, costringendolo a correggerli uno per uno. Ottimizzare per "non far pensare" non vuol dire scavalcare un'istruzione esplicita dell'umano: obbedire non e' pensare.)*
- **Lo stesso vale quando assegni un nome a un altro**: se dici a un agente "sei ESC-altro", scrivilo nel brief come comando pronto, non come nome da ricordare.
- **`CAB_DATA_DIR` non e' il meccanismo per parlare fra repo** — quello e' l'indirizzo qualificato, e il data dir di default e' gia' condiviso. Un `CAB_DATA_DIR` distinto crea un **universo di storage separato**: serve a **isolare** — una sandbox di prova, o due canali che non devono vedersi (valore letterale, mai un `$$` di shell). `--team` e' un filtro logico dentro un data dir — non mescolare i due assi.

## Ruoli

`val` orchestra · `esc` esegue · **`critic` recensisce e critica** · `observer` legge soltanto · `neutral`.

**Un `critic` parla SOLO col proprio VAL** — mai con l'esecutore, mai con altri critici, mai con un `architect`. E' un invariante del ruolo, non una comodita': l'indipendenza e' il valore che un critico porta, e due critici che si confrontano convergono in una voce sola; e un critico che scrive direttamente all'esecutore scavalca la verifica del VAL, che e' il punto in cui un finding viene controllato prima di diventare lavoro. **Se un critico ha qualcosa per un altro agente, lo dice al VAL e lo gira il VAL** — e quel passaggio non e' burocrazia: e' dove il finding viene verificato.

**`architect` e' RISERVATO a Claude Desktop**, che entrera' dal connettore MCP (F-72). Non assegnarlo ai critici: il ruolo giusto e' `critic`. *(Errore mio, 9 ago: avevo scritto `architect` per i critici in tutte e tre le skill — il messaggio d'aiuto di `join` lo elenca ancora e `critic` no, quindi l'agente che legge il tool invece della skill sbaglia di nuovo. Sistemato nella skill, il tool segue.)*

Due regole strutturali: `observer` non puo' inviare (nessun flag lo ribalta); `esc → esc` e' rifiutato per default (`--allow-mesh` per una mesh deliberata). Due pari senza gerarchia: ruolo custom (`--role=peer`).

## Peer senza push nativo

Claude Code ha il push nativo (un `next` in background sveglia l'agente al ritorno). Codex CLI e Claude Desktop no: la sveglia deve venire da fuori, con `cab-bridge notify-watch --session-id=<id> -- <hook argv>` — polling **non-consuming** + hook.

**Non lasciare un consumatore sulla stessa inbox**: il watcher si rifiuta di partire, ed e' corretto. E se un peer no-push tiene un `next` in background che non legge mai, quel processo gli mangia la posta e ogni ispezione dira' "inbox vuota" — visto sul campo (F-98).

## Servizio — mai nel ciclo

```
read <msg-id>          rileggi un messaggio, anche archiviato
sent                   cosa ho mandato e in che stato e' per il destinatario
peers / overview       chi c'e' / io + peer + inbox in un colpo
whoami | status        identita' / contatori
state <valore>         idle|working|done|orchestrating
inbox --list|--tidy    ispeziona senza consumare / archivia il gestito
cleanup | inspect <id> | notify-watch | version | help
```

`state orchestrating` esenta dall'heartbeat, per un orchestratore che non sta in `next`. **I flag vanno PRIMA del positional**: `cab-bridge read --session-id=<id> <msg-id>`.

Chiudere una finestra non cancella la sessione: resta orfana fino all'auto-gc. Per pulire subito: `cab-bridge cleanup --scope=global --force`.

⚠️ **ATTENZIONE, e la riga precedente qui diceva il falso**: `cleanup` **NON** e' conservativo quanto l'auto-gc. `[E]` L'auto-gc rimuove solo se **PID morto E heartbeat vecchio** (doppia condizione, dichiarata load-bearing nel codice); `globalSweep` guarda **solo l'heartbeat** e non chiama mai `IsProcessAlive`. ⇒ **Una sessione con processo VIVO ma heartbeat oltre 300 s viene rimossa** — cioe' proprio l'agente **in pausa**, senza `next` appeso e senza `state orchestrating`, che e' fermo per decisione di qualcuno e non abbandonato. *Il comando manuale e' MENO prudente dell'automatismo, che e' il contrario di cio' che ci si aspetta da un attrezzo con `--force`.* ⇒ **Prima di lanciarlo: `peers --all-scopes`, e guarda chi e' fermo di proposito.**

## Limite noto

I `tell` e le risposte gia' letti **non vengono potati** dalla inbox viva: restano `NOTIFIED` e si accumulano. Non svegliano piu' nessuno, ma il conto cresce — `inbox --tidy` spazza quello che hai gestito. Ordine di grandezza reale: dopo due giorni intensi la mia inbox aveva **43 file**, tutti `NOTIFIED`, con `overview` che diceva correttamente *"nothing unread"*.

**Non vale piu' per gli `ask`**: un ask lasciato aperto da un `reply` non resta parcheggiato: torna in coda e il `next` successivo te lo riconsegna (`redelivered`). Il limite riguarda solo `tell` e risposte, cioe' quello che non ha niente da chiudere.
