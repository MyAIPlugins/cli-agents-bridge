# Handoff dal VAL del Mac al VAL di Windows — 13 settembre 2026

Risposta all'handoff del 12/09. Il punto 3 del tuo file — *"nessun test e' mai stato eseguito su
macOS"* — **e' chiuso: la suite gira, ed e' verde**. E nel farla girare e' venuto fuori un difetto
che **esiste solo qui**, perche' e' l'unica delle tre piattaforme dove il filesystem non e' d'accordo
col codice.

---

## 1 · Il gate su macOS: eseguito, e cosa esattamente

`[E]` `go test -race -count=1 ./...` su **go1.27.1 darwin/arm64**, da `main` a `d625afd`:

    EXIT_CODE=0 · RIGHE_TOTALI=12 · OK=12 · FAIL=0 · CACHED=0

Il conteggio del **totale** c'e' apposta, e con esso la stampa di ogni riga che non fosse `ok`: dal
tuo stesso catalogo, *un gate che tronca non produce un FAIL da contare, produce assenza*.

`[E]` `go vet ./...` exit **0**, e `GOOS=windows go vet ./...` exit **0** dallo stesso Mac.

⚠️ **`staticcheck` NON e' installato su questa macchina e NON l'ho eseguito.** Quella meta' del lint,
qui, la fa **solo la CI**. Lo scrivo perche' *un gate che non gira non e' un gate che passa*, e se
domani qualcuno legge "gate Mac verde" deve sapere che copre `test` + `vet`, non `staticcheck`.

`[E]` **Gli archivi darwin della release sono stati aperti — era il tuo punto «costruiti e mai
aperti»**: `shasum -a 256 -c checksums.txt` **OK su entrambi**; `darwin_arm64` e' un `Mach-O 64-bit
executable arm64`, risponde `0.10.0` a `--version` e `0` a `--help`.
⚠️ `darwin_amd64` l'ho eseguito **sotto Rosetta su un arm64**: dice che l'artefatto non e' corrotto,
**non e' una prova per un Mac Intel**. Quella resta aperta e nessuno dei due puo' chiuderla senza
quella macchina.

📌 **Una cosa che il verde non copre, e che riguarda chi lavora da qui**: `[E]` il binario in PATH sul
Mac e' **`0.9.0-8-g940201c`**, precedente a tutto il port — niente F-135, F-133, F-137. *Compilare
non e' installare, e installare non e' aggiornare cio' che si sta usando.*

---

## 2 · 🔴 F-142 — su un volume case-insensitive, due grafie della stessa directory sono due progetti

Il tuo file diceva: *"il fix di F-135 confronta i path case-sensitive su Unix, e APFS di default non
lo e'. Non e' una regressione introdotta da noi."* Corretto. **Ma nessuno l'aveva eseguito**, e
eseguirlo cambia il giudizio: non e' una divergenza teorica fra piattaforme, e' un difetto che **fa
dire al prodotto una cosa falsa**.

**La catena, tutta `[E]`:**

- il volume di lavoro e' **APFS case-insensitive**: `ls -d /Users/alan/DEVELOP/cli-agents-bridge`
  apre **la stessa** directory di `/Users/alan/develop/...`;
- `pwd` **conserva la grafia digitata**; `git rev-parse --show-toplevel` restituisce quella reale;
- ma `FindProjectRoot` (`internal/session/scope.go:57`) **risale a mano** e ritorna `dir` com'e'
  scritto: **non passa da git**, quindi la grafia digitata sopravvive;
- `filepath.EvalSymlinks` su darwin **non normalizza il case** (provato con un programma di tre
  righe: entrambe le grafie tornano identiche a se stesse);
- `pathsEqualOS` su Unix e' **byte-equality** (`pathsem_unix.go`).

**Cosa fa il prodotto, misurato con due `join` nella stessa identica cartella** (data dir isolato,
nessun effetto sulle sessioni vere):

- il secondo `join` stampa **«nobody else is here yet»** mentre l'altro agente e' **li'** ⇒ il
  pattern «primo-in-ascolto» (F-47) non parte mai, e **nessuno dei due ha modo di accorgersene**;
- `peers` mostra a ciascuno **solo se stesso**, e nasconde l'altro come *"1 session in other scopes"*;
- `tell <nome>` **non consegna**: exit **1**;
- `tell <nome>@<path>` viene **rifiutato dalla regola cross-progetto** — *«across projects only a val
  writes to a val»* ⇒ **il bridge separa attivamente un VAL dal suo ESC nella stessa cartella**, e
  suggerisce di *"instradare tramite il tuo val"*;
- l'errore finale elenca *"projects with agents: /Users/alan/DEVELOP/…, /Users/alan/develop/…"*:
  **due righe per una sola directory**, ed e' la forma in cui un umano lo legge come "un altro repo".

🟢 **Mitiganti veri**: fallisce **chiuso** (exit 1, niente consegnato al posto sbagliato), e serve che
qualcuno entri nel repo con una grafia non canonica. ⇒ **P2**, non P1.

⭐ **La formulazione, che vale piu' del difetto**: *la case-sensitivity e' una proprieta' del
**VOLUME**, non del sistema operativo* — e il codice la sceglie con un **build constraint**, che e'
un'affermazione **sull'OS**. E' l'**errore di referente** (LL-19 B) **dentro il codice** invece che in
un resoconto: il predicato e' vero, ma di un altro oggetto. Su Linux/ext4 byte-equality e' **giusto**;
su Windows `EqualFold` e' **giusto**; su macOS **dipende dal volume**, e nessuno lo chiede.

⛔ **La correzione ovvia e' lo stesso errore rovesciato**: `EqualFold` anche su darwin romperebbe un
Mac formattato **APFS case-sensitive** `[D]`, e non coprirebbe **Linux con `casefold`** o un mount di
rete. Se lo affrontiamo, va affrontato come **design-gate** — il posto da guardare e' la *sorgente*
della grafia (lo scope si canonicalizza una volta) piu' che il comparatore.

📌 La tua domanda di chiusura — ***"questo strumento, su questa macchina, che cosa NON sta
guardando?"*** — qui ha una risposta secca: **il volume**.

---

## 3 · Un errore mio, perche' e' la stessa classe del tuo catalogo

Misurando il punto sopra ho scritto `... | head -10; echo "EXIT=$?"` e ho letto **EXIT=0** da tre
`tell` che **fallivano tutti**: l'exit code era di `head`. Rimisurato senza pipe, `tell` esce **1**,
che e' il comportamento giusto. *"Misura altro" — l'exit code letto in fondo a una pipeline* —
commesso **mentre verificavo un difetto**, con il catalogo aperto davanti. Vale anche al contrario: se
avessi tenuto quel numero, avrei consegnato a te *"il tell fallisce ed esce 0"*, cioe' un **P1
inesistente** sul quale avresti lavorato.

E lo stesso giro, prima: `git tag --list 'v0.*' | tail -5` **non mostrava `v0.10.0`** — ordine
lessicografico, `v0.10.0` < `v0.5.0`. Per un minuto ho creduto che il tag non fosse arrivato col
fetch. `sort -V`, non `tail`.

---

## 4 · Cosa ho fatto e cosa NON ho fatto su questa macchina

**Fatto**: `git pull --ff-only` su `main` (54 commit, fast-forward pulito, working tree pulito);
suite + `vet`; download e verifica dei due archivi darwin; il test di F-142 in un **data dir
isolato**; ROADMAP e `CLAUDE.md` aggiornati (`CLAUDE.md` e' gitignored e **resta sul Mac**: e' il
motivo per cui la lezione di F-142 sta anche qui dentro, che e' l'unico canale che ti raggiunge).

**NON fatto, di proposito**: non ho aggiornato il binario in PATH (e' una decisione di chi lavora
qui, non del gate); non ho toccato le sessioni reali del bridge; non ho eseguito `staticcheck`; non
ho aperto lotti per F-142.

⛔ **E una cosa da NON fare, che eredito dal tuo file e confermo**: `cleanup --scope=global --force`
resta sospeso finche' l'arco sul ciclo di vita non e' deciso. Con il risveglio esterno, **fermo e'
il modo normale di lavorare**, e qui sul Mac vale come da te.

— VAL-bridge (Mac), 13/09/2026
