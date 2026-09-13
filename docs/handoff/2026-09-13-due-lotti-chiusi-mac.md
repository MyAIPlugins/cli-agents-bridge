# Mac → PC, 13 settembre (sera) — due lotti chiusi, e tre cose che cambiano come lavoriamo

> Segue `2026-09-13-gate-macos-mac.md`, che raccontava il primo gate su darwin e l'apertura di F-142.
> Da allora: **F-139 e F-143 sono chiusi, mergiati e pushati**, con CI verde su entrambi i merge.
> `main` e' a `fbb96d4`.

---

## 1 · Cosa e' entrato in `main`

    4036e14   merge F-139   la versione stava in otto posti, il guard si disarmava in dieci modi
    3c4dafc   merge F-143   il guard $HOME confrontava byte su un path non canonicalizzato
    fbb96d4   docs

`[E]` Gate su `main` dopo ciascun merge: **EXIT=0 · 12 righe · 12 `ok` · 0 `FAIL` · 0 `cached`**;
`go vet` a **zero su darwin, linux e windows**.
🟢 **`staticcheck` ERA assente sul Mac e ora c'e'** (installato a valle dei due merge, `v0.8.1`,
la versione pinnata in `.staticcheck-version`). ⇒ I gate che hanno **autorizzato** i due merge
**non** lo comprendevano: quella meta' l'aveva fatta solo la CI. `[E]` Rieseguito **dopo**
l'installazione su `main` che contiene entrambi: `make lint` **exit 0, nessun finding** ⇒ i due lotti
sono validati anche da quella meta', retroattivamente. `[E]` E il verde e' stato verificato con un
**controllo positivo** — su un file con difetti noti lo stesso binario trova `SA4017`/`S1039` ed esce
**1** — perche' un lint silenzioso e' il caso in cui *l'assenza sembra un risultato*.
⚠️ Se dal PC dichiari un gate verde, di' se il tuo comprende staticcheck.

---

## 2 · F-139 — e perche' il lotto e' cresciuto di tre volte

Il brief chiedeva di legare **i due manifest** al tag. `[E]` Erano **gli unici due posti che dicevano
il vero**: gli altri **sei** dichiaravano `v0.9.0` col tag a `v0.10.0`, e **due erano comandi
copiabili** che facevano scaricare un archivio inesistente. *Un test costruito su quel brief sarebbe
stato verde su un repository falso in sei punti.*

🟢 **La fonte non l'abbiamo scelta noi**: `[E]` `claude plugin validate --strict` dichiara che a
install time vince `plugin.json` e che il campo del marketplace e' **silenziosamente ignorato** ⇒ due
posti sono stati **tolti** invece che verificati.

🟢 **Il tag si verifica solo in `release.yml`**, dove esiste per costruzione. ⛔ **Non in `ci.yml`**:
`[E]` li' il checkout e' shallow, e la riga del Makefile (`git describe --tags --always`, stderr
scartato) **non fallisce senza tag — restituisce uno SHA che ha l'aspetto di un dato buono**.

**Dieci forme di disarmo, dieci rosse.** Cinque lasciano uno step che **si legge benissimo**. Le due
da ricordare:
- ⭐ **il contratto copiato dal workflow**: il messaggio d'errore diceva *«aggiorna il contratto
  deliberatamente»*, e il modo naturale (`cp release.yml release.yml.contract`) **cancella i
  placeholder** ⇒ verde per sempre. **Il gate si disarmava obbedendo alla propria istruzione.**
- ⭐ **il guard tolto dal binario** (un `//go:build ignore`): in release diventa `ok … [no tests to
  run]` **exit 0**, e nel gate completo **nessun segnale** — il package ha altri test, quindi quella
  riga non compare. A prenderla e' il **passo di rilascio**, che ora chiede il **verdetto**
  (`--- PASS`), non l'exit code.

📌 **Per te sul PC**: `release.yml` e' ora sotto **contratto di uguaglianza esatta** (golden in
`tests/integration/testdata/release.yml.contract`, due slot). **Ogni riga** e' vincolata, pin
compresi. Se lo tocchi, il gate diventa rosso **apposta**: il messaggio spiega che e' un allarme di
**revisione del contratto**, non un errore YAML. ⛔ Non renderlo verde allentando il confronto.

---

## 3 · F-143 — il guard `$HOME`, e le quattro grafie

`FindProjectRoot` salta `$HOME` di proposito, ma il guard era `dir != cleanHome`: **byte-equality su
un input che il walk non canonicalizza**. Bastava una grafia diversa perche' la risalita trovasse il
`.git` dei dotfiles e restituisse **`$HOME` come scope di progetti diversi** — e **non falliva chiuso**:
il `tell` fra i due usciva **0** col messaggio in inbox.

`[E]` **Quattro grafie, e nessuna richiede il case**: alias **symlink** (nei due versi) · `/tmp` contro
`/private/tmp` (nativo su macOS) · **Unicode NFC/NFD** · **firmlink** — `/Users/alan` e
`/System/Volumes/Data/Users/alan` hanno lo stesso `(dev, ino)` e `EvalSymlinks` **li lascia distinti**.
`[E]` **Non** lo attivano: separatore finale e `..` ordinario (`Clean` li assorbe).

🟢 **Fix del solo predicato**: fast path lessicale, altrimenti — e **solo dove un marker c'e'** —
`os.Stat` dei due lati con **`os.SameFile`**. Walk lessicale, output invariato.

⛔ **Il ramo permission-denied resta APERTO**, dichiarato nel codice e in un test scritto *perche' e'
scomodo*. **Non dichiararlo chiuso da nessuna parte.**

⚠️ **Due cose che ti riguardano se tocchi quel codice:**
- **Gli scope gia' persistiti non si sanano**: `reconnect.go:320` scrive `mf.Scope` **solo se vuoto**.
  Una sessione collassata resta collassata anche col binario nuovo.
- **Il costo**: `[E]` un repository normale **paga fino a due `Stat`** (il marker lo trova); a non
  costare sono gli antenati **senza** marker. Sui **manifest legacy** dentro `LookupByCWDDetails` e
  `collectPeers` sono **fino a 2N `Stat` per scansione**. Dichiarato, **senza cache**.

---

## 4 · Tre cose che cambiano il metodo, e valgono su entrambe le macchine

**(a) Due critici di famiglia diversa, e chi disegna non fa il gate del proprio disegno.** Il design
del guard di F-139 e' del critico Codex; il diff-gate e' andato al critico Claude, **che quel disegno
non aveva mai visto** — ed e' lui ad aver trovato la forma del `cp`. Il valore massimo e' venuto
esattamente da li'.

**(b) `codex queue` mentre l'agente LAVORA e' FIFO.** `[E]` Misurato: wake mandato a 46 s dall'inizio
di un turno lungo **3m56s** ⇒ **non compare durante il lavoro**, arriva come nuovo turno **alla fine**,
**col testo integro**, dopo ~3m10s in coda. **Non interrompe, non si perde.** Tre fonti concordi (file
del bridge, referto dell'agente, TUI). ⚠️ **n=1**. ⭐ E cio' che l'ha riportato al bridge **prima** del
wake e' stato il **`reply`**, che segnala posta non letta: **la rete e' il bridge, non il risveglio
esterno**.

**(c) I worktree sono la mitigazione strutturale di F-142.** `[E]` Lo scope di un worktree **non**
deriva dalla cwd digitata ma dal pointer `gitdir:` che git scrive **canonico** ⇒ **ESC e i critici
entrano da `.worktrees/<ruolo>`, solo il VAL sta nella root**, dove la grafia va digitata giusta.
⚠️ `CLAUDE.md` e' gitignored: in un worktree **non arriva**, va collegato con un symlink.

---

## 5 · Aperti, e due sono tuoi

    F-140   reply <nome> < file rifiuta con UN mittente        piano ratificato, tuo
    F-141   peers fa sembrare orfani listener di un altro repo  piano ratificato, tuo
    F-142   due grafie = due progetti                           DIREZIONE NUOVA, vedi sotto
    F-144   NUOVO: le action della CI inseguono Node 20         non assegnato
    ARCO    ciclo di vita delle sessioni                        la decisione aspetta Alan

🟢 **F-142 non e' piu' senza direzione**: `[E]` **`fcntl(F_GETPATH)`** sul fd di una directory
restituisce **la grafia del kernel** e risolve **symlink, firmlink e normalizzazione Unicode insieme**;
e' in **`golang.org/x/sys/unix`, gia' in `go.mod`**. `[D]` Analoghi: `readlink(/proc/self/fd/N)` su
Linux, **`GetFinalPathNameByHandle` su Windows** — e quella meta' **puo' guardarla solo il PC**.
⚠️ Non serviva a F-143 (li' `SameFile` basta): serve dove il problema e' la **grafia persistita**.

🟡 **F-144, e ti riguarda perche' la CI e' condivisa**: `[E]` la run verde di oggi annota *«Node.js 20
is deprecated … actions/checkout@v4, actions/setup-go@v5 are being forced to run on Node.js 24»*, su
**8 usi** fra i due workflow. E' **la stessa struttura di `go-version: stable`**, che l'11 agosto ha
reso rossa la CI **senza che nessuno toccasse il repo**. Il bump delle major va **verificato uno per
uno**, non fatto al buio. ⚠️ Seconda annotazione della stessa run, separata: *«Failed to restore:
"/usr/bin/tar" … exit code 2»* sul job ubuntu — e' il **restore della cache**, la suite passa lo
stesso. **Da guardare sulla prossima run**, non da spiegare adesso.

---

## 6 · Il dato di metodo che vale piu' dei due lotti

**Quasi tutti i difetti trovati oggi stavano negli STRUMENTI, non nel prodotto** — ed e' la terza volta
che questa frase si scrive in questo repository:

- **tre strumenti di misura difettosi in tre giri** di F-139: una classificazione `and`/`or` **senza
  parentesi** che etichettava dieci mutazioni come *«non compila»* (verdetto giusto, **spiegazione
  falsa**) · una sonda che **troncava a 60 caratteri** · una sonda `bash` che **misurava zsh**
  (`set -- $x` non fa word splitting li');
- **il test del limite di F-143 costruito TRE volte** in modo da **passare senza esercitare nulla**;
- ⭐ e la forma nuova, dal VAL: *«il walk ordinario non paga syscall»* scritto **dopo aver verificato
  il call-site**. Il call-site era giusto, la conclusione falsa — **guardavo dov'e' il controllo,
  mentre la domanda era quante volte ci si entra**. *Non il dato sbagliato: la domanda sbagliata su un
  dato letto bene.*

— VAL-bridge (Mac), 13/09/2026
