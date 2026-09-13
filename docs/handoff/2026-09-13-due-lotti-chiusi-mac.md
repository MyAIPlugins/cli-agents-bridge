# Mac → PC, 13 settembre (sera) — due lotti chiusi, e tre cose che cambiano come lavoriamo

> Segue `2026-09-13-gate-macos-mac.md`, che raccontava il primo gate su darwin e l'apertura di F-142.
> Da allora: **F-139 e F-143 sono chiusi, mergiati e pushati**, con CI verde su entrambi i merge.
> `main` e' a **`c9bb11b`** o piu' avanti. **Le sezioni 7-9 sono quelle operative**: come
> allinearti, e **cosa NON passa da git**.

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


---

## 7 · Come allinearti, in ordine

```
git pull --ff-only            # main e' a c9bb11b o piu' avanti
make build                    # il binario nuovo
make install-plugin           # il plugin: mergiato NON e' installato
make lint                     # deve comprendere staticcheck: vedi sotto
make test-race                # e leggi il TOTALE, non solo i FAIL
```

⚠️ **Dopo un pull che tocca `plugins/` o `skills/`, serve una sessione NUOVA**: quella in corso tiene
in contesto la versione precedente, e non e' igiene — e' l'unico modo.
⚠️ **`cab-bridge version` e' l'unica cosa che conta**: *mergiato non e' installato*.

---

## 8 · Cosa NON passa da git, e quindi va rifatto a mano sul PC

Questa e' la sezione che Alan ha chiesto esplicitamente. Tutto quello sotto **non e' nel repository**:
se non lo fai a mano, sul PC non c'e'.

### 8.1 · `CLAUDE.md` — la lezione della giornata

`CLAUDE.md` e' **gitignored** ([[claude-md-non-nel-repo-pubblico]]): quello che ho scritto li' oggi
**non ti arriva**. Sul Mac ho aggiunto **LL-21**, che raccoglie la giornata. Se tieni un `CLAUDE.md`
sul PC, i tre pezzi da riportare sono gia' tutti in questo documento — §4 (il metodo), §6 (gli
strumenti), e la forma nuova qui sotto, che e' la parte che non avevamo:

> ⭐ **La domanda sbagliata su un dato letto bene.** Avevo scritto *«il walk ordinario non paga
> syscall»* **dopo aver verificato il call-site** — e il call-site era giusto. Falso lo stesso: un
> repository normale il marker lo **trova**. *«Due letture corrette dello stesso codice, una
> conclusione falsa; a prenderla e' stato chi ha contato i **casi** invece di guardare la
> **posizione**»* (ESC). ⇒ LL-19(B) chiede *di quale oggetto* e' il dato; qui il dato era del proprio
> oggetto e la **domanda** era un'altra. **Antidoto: quando leggere il codice produce un'affermazione
> quantitativa — mai, sempre, zero, N volte — la lettura non basta: si contano i casi in cui ci si
> passa.**

### 8.1-bis · ⚠️ Il tuo `CLAUDE.md` puo' contenere QUATTRO affermazioni false — le stesse che aveva il mio

A valle dei due lotti ho riordinato i documenti, e il risultato riguarda te **direttamente**: sette
convenzioni tecniche che il codice segue vivevano **solo** in `CLAUDE.md`, cioe' in un file
**gitignored** — non raggiungevano questa macchina, non arrivavano a un contributor, e sarebbero
sparite col disco. Sono ora in **`docs/dev-conventions.md`**, che e' tracciato: ti arrivano col pull.

⛔ **Non le ho copiate: le ho riverificate sul codice, e quattro su sette erano gia' false.** Se il tuo
`CLAUDE.md` discende dallo stesso file, **ha le stesse quattro**:

    "Subcommand flag parsing ... cmd/cab-bridge/receive.go"   il file NON ESISTE PIU'
    "fs.SetOutput(io.Discard)"                                il codice usa os.Stderr
    "exitFromErr ... cmd/cab-bridge/common.go" + "124 timeout" vive in main.go, e il 124 non c'e'
    "goleak optional"                                          NON e' in go.mod, non e' importato

⇒ **Sul PC**: togli la sezione `## Go style conventions` dal tuo `CLAUDE.md` e sostituiscila con un
rimando a `docs/dev-conventions.md` (sul Mac l'ho fatto cosi', e la sezione nuova nel documento
tracciato porta **la data in cui ogni file e simbolo e' stato controllato**, cosi' il prossimo
ri-esegue invece di credere).

⭐ **E la ragione per cui erano sopravvissute e' la lezione della giornata**: nessun gate poteva
prenderle, perche' **un testo non ha modo di fallire**. La quarta e' la peggiore — `goleak` dichiarato
in uso per mesi: *chi legge conclude che esista un controllo che nessuno esegue*.

### 8.2 · Le skill: due canali, e solo uno e' su git

    skills/<vendor>/...              TRACCIATO, generico       -> ti arriva col pull
    .skills-personal/<vendor>/...    GITIGNORED, specifico     -> NON ti arriva
    ~/.claude/skills/, ~/.codex/     INSTALLATE, fuori repo    -> NON ti arrivano

`[E]` Oggi ho aggiornato **la stessa riga in tre posti**: la versione tracciata (ti arriva), la copia
personale e quella installata (**non** ti arrivano). La riga e' quella su `codex queue`: diceva *«`queue`
mentre lavora: mai misurato»* e ora dice **che e' FIFO**, col dato di §4(b).
⇒ **Sul PC**: dopo il pull, riporta quella riga anche nella tua copia personale e in quella installata,
**o** reinstalla la generica se non hai personalizzazioni che valga la pena tenere.
⚠️ **La copia personale VINCE su quella generica**: se aggiorni solo il repo, l'agente continua a
leggere la vecchia.
⚠️ `[E]` **La skill Codex installata differisce da `skills/codex/` nel repo** anche sul Mac: non l'ho
ispezionata e **non l'ho toccata**. Se sul PC vale lo stesso, guardala prima di sovrascriverla.

### 8.3 · `staticcheck` — controlla che il tuo gate lo comprenda

`[E]` Sul Mac **non era installato**, e i gate che hanno autorizzato i due merge **non lo
comprendevano**. Installato a valle (`v0.8.1`, la versione pinnata in `.staticcheck-version`) e
rieseguito su `main`: **exit 0, nessun finding**.

    go install honnef.co/go/tools/cmd/staticcheck@$(cat .staticcheck-version)

📌 `make lint` lo prende da **GOBIN prima del `PATH`**, ed e' voluto: il pin deve governare
**l'esecuzione**, non solo l'installazione — il commento nel Makefile spiega perche' (in CI l'ordine
inverso faceva sì che il pin governasse l'install e qualcos'altro il run).
⚠️ **Un lint silenzioso e un lint che non ha girato stampano la stessa cosa.** Prima di chiamarlo
verde, dagli un file con un difetto noto e verifica che **esca 1**.

### 8.4 · Lo stato della macchina che nessun diff mostra

    binario in PATH    Mac: ~/.local/bin/cab-bridge e' un SYMLINK a bin/cab-bridge del repo
                       -> ogni `make build` aggiorna il PATH da solo. Sul PC verifica se e' una COPIA:
                          una copia va stale in silenzio.
    plugin             servito dal REPO VIVO (marketplace di tipo `directory`) -> il pull aggiorna
                       skill e comandi da solo, ma serve una sessione nuova
    worktree           i tre worktree del Mac (.worktrees/esc|cri|cri2) sono LOCALI e gitignored

---

## 9 · Cosa ho lasciato aperto di proposito sul Mac

- **I tre worktree e i due branch mergiati non sono stati rimossi**: gli agenti (ESC, CRI, CRI2) sono
  **vivi e senza compito assegnato, non congedati**. Rimuovere un worktree mentre qualcuno ci lavora
  dentro gli rompe la sessione. La pulizia si fa quando Alan li congeda, non prima.
- **F-142, F-144 e l'arco sul ciclo di vita delle sessioni**: aperti, nessun lotto.
- **Il ramo permission-denied di F-143**: aperto e **dichiarato**. ⛔ Non chiamarlo chiuso.
- **Il ramo `.git` FILE (worktree) in `FindProjectRoot`**: `gitMarkerRoot` ritorna la root del pointer
  e non passa dal guard. ⚠️ **Non e' un bypass da chiudere**: un worktree del repo dotfiles **deve**
  risolvere alla propria common-root (contratto **F-41**), e bloccarlo dividerebbe due checkout dello
  stesso repository. E *«must never return $HOME»* e' il messaggio dell'assert **sui figli
  marker-less**, non il contratto generale: `TestFindProjectRoot_CwdEqualsHome_Degenerate` **accetta**
  `scope == home`. **Decisione separata, non un difetto.**
- **Gli scope gia' persistiti**: `reconnect.go:320` scrive `mf.Scope` **solo se vuoto** ⇒ una sessione
  collassata resta collassata anche col binario nuovo. Nessuna migrazione, e **non dichiararle
  risanate**.

---

— VAL-bridge (Mac), 13/09/2026
