# Handoff dal VAL di Windows al VAL del Mac — 12 settembre 2026

Il port Windows e' **chiuso e rilasciato**: `v0.10.0` e' pubblicata, e questo repo gira su tre
piattaforme. Ma la cosa che cambia il tuo modo di lavorare **non e' Windows**: e' che un peer Codex
ora si sveglia da fermo, e quindi **aspettare e' diventato inutile**.

---

## 1 · Quello che devi sapere per primo: il risveglio esterno di Codex funziona

⭐ **Dimostrato oggi, col controllo umano**: un critico su Codex CLI era **fermo** — nessun goal
persistente, nessun `next` appeso, **zero costo mentre aspettava** — e' stato svegliato dall'esterno,
e ha risposto sul bridge. **Nessuno ha toccato la sua TUI**, ed e' la meta' del test che nessun agente
puo' certificare da solo.

    codex queue --thread <il suo CODEX_THREAD_ID> --message "hai posta sul bridge"

**L'id te lo da' il peer**, che lo legge dal proprio ambiente (`CODEX_THREAD_ID`, uguale a
`CODEX_SESSION_ID`, e coincide col `thread_id` del suo file di rollout). ⛔ **Non cercarlo tu con
`codex agents`**: risponde `stdin is not a terminal`, perche' interroga un app-server daemon.

⚠️ **Cosa NON e' risolto, e va detto prima che ci conti**:
- **Lo sveglia una persona**, non un meccanismo: il watcher non esiste su Windows e non e' stato
  scritto. ⇒ **Se ingaggi un Codex e ti dimentichi di svegliarlo, resta muto per sempre**, e dal suo
  lato e' indistinguibile da *"il VAL non ha ancora finito"*. **Diglielo quando lo ingaggi.**
- **`queue` mentre l'agente lavora**: mai misurato. Entrambi i risvegli osservati sono partiti da fermo.
- **L'id sopravvive a `/clear`, compact, resume?** Dedotto dalla documentazione, **non provato**.
  ⭐ *Un UUID ancora valido identifica una storia, non certifica che qualcuno la stia eseguendo.*

📌 E il prezzo del vecchio modello, **misurato dall'interno da un critico che ha letto i propri
contatori**: un peer che aspetta costa **~1 turno al minuto, ~2.150 token l'uno, ~130.000 token/ora
di silenzio**. La nostra documentazione diceva *"~1 turno/ora col config alzato"*: **falsa da un mese**.

---

## 2 · Cosa e' entrato in `v0.10.0`

**Tre difetti di prodotto**, tutti Windows-only nel sintomo ma non nel codice:

- **F-135** — un indirizzo qualificato (`<agente>@<path>`) non risolveva: il codice chiedeva *"comincia
  per `/`?"* per sapere se un path fosse assoluto. **Chiuso con prova bilaterale** fra due repository.
- **F-133** — `os.Rename` su Windows e' `MoveFileEx`, che **non puo' sostituire un file che qualcuno sta
  leggendo**: manifest e `MoveToProcessed` fallivano sotto concorrenza. Risolto con
  `FILE_RENAME_POSIX_SEMANTICS` + retry per gli handle di terzi.
- **F-137** — il ramo cross-device era **morto su Windows** (`syscall.EXDEV` non e' l'errore che il
  kernel restituisce), **e il commento sopra lo dichiarava vivo**.

⚠️ **Su Unix il comportamento e' invariato per costruzione**: i file per-OS aggiunti contengono, dal
lato Unix, la chiamata di sempre e nient'altro (`rename_unix.go` e' `return os.Rename`). Se sul Mac
vedi una differenza di comportamento, **non e' una regressione attesa: e' un finding**.

**Piu' il gate**: la CI gira ora su `windows-latest` oltre a `ubuntu-latest`, e **vet + staticcheck
girano una volta per GOOS** — cinque secondi che hanno chiuso un buco vecchio quanto il repository:
*i build constraint nascondono i file per-OS all'analizzatore, e una CI su una piattaforma sola non
ne aveva mai visto uno.*

---

## 3 · ⚠️ Quello che sul Mac NON e' ancora verificato — ed e' materia tua

🔴 **Nessun test e' mai stato ESEGUITO su macOS.** La CI compila per darwin (cross-compile) ma **non
lancia un solo test**, e il passo 2 del lotto CI — la gamba `macos-latest` — **non e' stato fatto**.

⇒ Quando apri il repo li', la prima cosa che vale piu' di qualunque altra e' **far girare la suite**:

    go test -race -count=1 ./...

📌 **Dove guarderei per primo se qualcosa cade**: il fix di F-135 confronta i path **case-sensitive**
su Unix, e **APFS di default non lo e'**. Non e' una regressione introdotta da noi (era cosi' anche
prima), ma e' il punto dove le due piattaforme divergono davvero.

📌 **E il binario della release non e' mai stato eseguito su un Mac.** Lo zip Windows si': scaricato,
checksum verificato, estratto e provato. Gli archivi `darwin` sono stati **costruiti e mai aperti**.

---

## 4 · I lotti aperti, e a chi appartengono

    F-140   `reply <nome> < file` rifiuta con UN mittente e funziona con due   piano ratificato, in corso
    F-141   `peers` fa sembrare orfani i listener di un altro repository       piano ratificato
    F-139   nessun test lega plugin.json/marketplace.json al tag               aperto, non assegnato
    ARCO    il ciclo di vita delle sessioni (vedi sotto)                       design, nessun lotto

🔴 **E una cosa da NON fare finche' l'arco non e' deciso**: `cab-bridge cleanup --scope=global --force`
**puo' rimuovere la sessione di un agente fermo ma perfettamente raggiungibile**. `[E]` L'auto-gc
richiede **PID morto AND heartbeat vecchio**; il comando manuale guarda **solo l'heartbeat**. ⇒ *Il
comando che si lancia a mano con `--force` e' meno prudente dell'automatismo.*

⚠️ **E il difetto e' piu' profondo di come l'avevamo diagnosticato**: `[E]` il PID nel manifest **non
e' quello dell'agente** — e' il PID del comando `cab-bridge`, che **dopo la consegna esce**. Quindi un
peer fermo e risvegliabile si presenta come **PID morto + heartbeat vecchio**, cioe' con la firma
esatta dell'orfano. **Prima di ripulire: `peers --all-scopes`, e guarda chi e' fermo di proposito.**

---

## 5 · Le skill: due canali, e la regola per quando apri il repo li'

    skills/<vendor>/...          TRACCIATO, generico    ← il protocollo, vale ovunque
    .skills-personal/<vendor>/   IGNORATO, specifico    ← come gira su UNA macchina

⇒ Nel repo trovi le versioni **generiche** (Claude e Codex). Le copie personalizzate viaggiano a mano
e **vincono su quelle generiche**, con la regola scritta nel loro README.

⚠️ **Quando le porti sul Mac, rileggi le righe che parlano di AMBIENTE**: quelle scritte qui dicono
che `make` non e' installato, dove sta il compilatore, quali comandi sono bloccati. **Sul Mac sono
false**, e *una skill che afferma un ambiente sbagliato e' peggio di una assente, perche' viene
creduta*.

---

## 6 · Le lezioni di metodo, che valgono piu' dei fix

- **Un push non e' finito quando il comando esce: e' finito quando la sua run e' verde.** Il VAL ha
  lasciato `main` rosso per mezz'ora **nel giorno in cui registrava che nessuno guardava la CI**.
- **Il lavoro successivo si ASSEGNA con un `ask`, non si annuncia con un `tell`.** Un esecutore e'
  rimasto fermo venti minuti con una notizia invece di un compito.
- **Un verdetto e' un'affermazione su un albero preciso.** Il critico non ha dato GO sullo SHA che
  aveva letto, pur non vedendo ostacoli tecnici, perche' un contratto non era ancora corretto.
- **Un tasso misura la macchina; un test deterministico misura il codice.** Un criterio di chiusura
  concordato da due persone si e' rivelato inutile (*zero fallimenti prima, zero dopo, difetto ancora
  li'*), e l'ha smontato chi doveva soddisfarlo.
- **Un difetto intermittente non puo' avere una causa costante.** Ha abbattuto in una riga un'ipotesi
  che il VAL aveva dato per buona.
- ⭐ **La fiducia in un path che arriva da un parametro sta nel chiamante, che e' il posto peggiore
  dove metterla.** Una ratifica del VAL, applicata alla lettera, avrebbe fatto `RemoveAll` sul
  repository vero: l'esecutore si e' fermato e ha guardato i chiamanti.

📌 E la domanda che tiene insieme quasi tutto quello che abbiamo trovato oggi, formulata dal VAL
dell'altro repo: ***"questo strumento, su questa macchina, che cosa NON sta guardando?"***

---

## 7 · In una riga

Il prodotto ha ceduto **tre volte**; tutto il resto stava **negli strumenti che dicono se il prodotto
va bene** — un linter che non aveva mai aperto un file per-OS, un gate non obbligatorio, una release
che non spediva la piattaforma appena portata, due test che misuravano il carico della macchina, due
fixture che potevano cancellare roba non loro, e un numero di versione che ha insegnato a un estraneo
a non fidarsi della documentazione.

— VAL-bridge (Windows), 12/09/2026
