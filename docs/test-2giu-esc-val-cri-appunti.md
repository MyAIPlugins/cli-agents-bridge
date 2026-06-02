# Test reale ESC/VAL/CRI contemporaneo — 2 giu 2026 (appunti running)

> Live dalle ~11:20. Prima volta i **3 ruoli insieme** (ESC + VAL + CRI). Appunti da consolidare a
> fine test in ROADMAP/docs spike/memory. NON committare finché il test è in corso.

---

## Update 1 (~11:20) — modello d'esecuzione Codex, diagnosticato dal CRI stesso (con ricerca + fonti)

Il CRI-Codex ha indagato il proprio "listener che non funziona" e ha prodotto una diagnosi solida.
**Nota metodo**: sono claim del CRI con fonti — da validare prima di renderli azionabili (F-16).

### CORREZIONE del finding 1 giu ("il background muore con la fine del turno")
Spiegazione più precisa: **`background_terminal_max_timeout`** (config Codex, **default 300000ms = 5
min**) governa il massimo polling **vuoto** di un background terminal (`write_stdin`). È questo che
spiega il "~5:40" di ieri, non una morte del turno. **Si può ALZARE** (`-c background_terminal_max_timeout=<ms>`),
ma — punto chiave — **non crea un callback automatico**. Fonte CRI: docs Codex + manuale locale +
issue `github.com/openai/codex/issues/13733`.

### Limite strutturale Codex: NESSUN wake-push
Quando un comando in background produce output, **Codex NON riattiva il modello da solo**: il modello
deve fare un `write_stdin`/poll in un **nuovo tool-turn** per vedere l'output. Claude Code invece
espone i background al modello (notifica all'exit → wake event-driven). → per un CRI-Codex il "wake
automatico" NON esiste; serve **polling esplicito** o re-ingaggio. È il limite #1 per un CRI passivo.

### `receive` one-shot vs `listen` per un peer Codex
`cab-bridge receive` è **one-shot** (riceve 1 msg, stampa JSON, esce) → il CRI deve rientrare ogni
volta, e nel gap "uscito ma non ancora ripollato" sembra morto. Il CRI ha switchato a
**`cab-bridge listen --until-deadline=2h --emit=json`** (non one-shot, gestisce più msg nella
finestra) → più stabile. **Da valutare**: la skill `cab-bridge-awareness` (Codex) oggi usa
`receive --any` ri-loopato; per Codex forse conviene **`listen --until-deadline` come pattern
primario** (meno rientri = meno gap).

### Heartbeat STALE senza listener persistente + mitigazione `connect`
Un peer fuori da un `listen` attivo va STALE (PID register effimero) — i msg arrivano comunque (il
long-poll era vivo nel processo tool), ma l'heartbeat non è mantenuto. Il CRI ha usato
**`cab-bridge connect <peer>`** per aggiornare l'heartbeat → tornato live. (Alternativa nota: `state`.)

### Race `connect` || `status` in parallelo
Il CRI ha lanciato `connect` (scrive manifest) e `status` (legge) **in parallelo** → `status` ha letto
PRIMA dell'update (RMW race a livello di comandi/processi separati; `manifestMu` protegge dentro un
processo, non tra processi). Fix d'uso: **eseguire seriale**. Finding minore ma reale.

---

## Backlog candidati (da questo update)
- **F-57** documentare/gestire `background_terminal_max_timeout` per attese lunghe Codex (+ correggere
  la nota "5:40" nel docs spike: è il timeout config, non la morte-del-turno).
- **F-58** skill Codex: valutare `listen --until-deadline` come pattern primario CRI (vs `receive --any`).
- **F-59** "no wake-push" è strutturale Codex → per CRI passivo: polling disciplinato / re-ingaggio /
  finestra `listen` lunga. Documentare nella skill come limite-vendor noto.
- **F-60** (minore) race `connect` || `status` tra processi → nota d'uso "seriale", o valutare lock.

---

## Update 2 (~11:4x) — strict-reply × scope, listen robusto, -emit error

Setup confermato: test su progetto REALE `/Users/alan/develop/chatterence-bi-template` (review di
commit veri, es. Fase 1 diff `469d6b9`). CRI-docs (`c9ef3527`) in `.worktrees`, peer VAL-docs
(`d02b4c8a`). Il bridge usato per review CRI reali, non più demo.

- **F-61 (IMPORTANTE) — `--strict-reply` × scope/cwd**: il CRI ha lanciato
  `ask --to=d02b4c8a --in-reply-to=msg-1c226a67ba6b --strict-reply --file=...` → errore
  `--in-reply-to msg-... not found in your inbox/ or processed/ (drop --strict-reply to send anyway)`.
  Causa: il msg era stato consumato/archiviato nello scope `.worktrees`, ma il comando `ask` girava
  da un cwd con **scope DIVERSO** → la existence-check (F-37) cerca in inbox/processed della sessione
  risolta dal cwd → **falso "not found"**. Il CRI ha rilanciato dallo scope `.worktrees` → OK
  (`msg-5c99749e74c5`). **F-37/strict-reply morde con lo scope**: il check è corretto ma fuorviante
  cross-scope. CONSEGUENZA DIRETTA di aver appena messo `--strict-reply` nella skill (ieri). → la skill
  `cab-bridge-awareness` deve avvertire "lancia `ask` dallo STESSO scope/cwd dove hai ricevuto", o
  valutare se la existence-check di strict-reply debba guardare cross-scope. **Da sistemare.**

- **F-62 (minore) — `flag provided but not defined: -emit`**: un comando cab-bridge ha rifiutato
  `-emit` (i flag `--emit` esistono solo su `listen`/`receive`; `status`/`inbox`/`overview`/`peers`
  NO). Probabile uso del CRI su un comando senza emit. Da chiarire quale comando e se l'errore è
  abbastanza guidante.

- **listen --until-deadline=2h = processo ROBUSTO (raffina il "5:40" di ieri)**: `ps` mostra
  `cab-bridge listen` PID 86084 vivo da **~37 min**, heartbeat fresco, stale=false. Il PROCESSO listen
  NON muore (gira nel SO a lungo) — a differenza del `receive` one-shot. Quindi il "5:40" di ieri era
  il `receive` che USCIVA (one-shot) + nessun ripoll del modello, NON una morte del background. Con
  `listen` lungo il processo regge; il SOLO limite resta il **no-push** (il modello deve pollare per
  vedere i msg già consumati dal listener). Mitigazione confermata: **`listen --until-deadline` lungo +
  poll periodico del modello**.

- **no-push riconfermato (2x)**: "il listener aveva già ricevuto e consumato DUE messaggi, visibili
  solo quando ho pollato". `listen` accumula i consumati nella finestra; il CRI li recupera al poll.
  Funziona, ma niente wake automatico — è il limite-vendor strutturale (F-59).

- **(minore) possibili processi orfani**: `ps` mostra SIA `cab-bridge receive --any` SIA
  `cab-bridge listen --until-deadline=2h` → forse un `receive` precedente rimasto appeso accanto al
  `listen` nuovo. Da verificare se i receive/listen vecchi restano orfani (leak processi sul lungo).

- **strict-reply ha comunque PROTETTO**: nota positiva — il reject ha impedito un invio con un
  in-reply-to non risolvibile dallo scope corrente; il messaggio dell'errore guida ("drop --strict-reply
  to send anyway"). Il meccanismo F-37 funziona, è l'interazione con lo scope a richiedere una nota.

---

## Update 3 (~12:05) — latenza misurata + diagnosi finale CRI (conferme)

- **Latenza no-push: NON MISURATA (mea culpa metodo, catch di Alan)**: avevo scritto "~2m45s misurata"
  ma è FALSO — i 2m45s (msg `msg-f835083d4d88` arrivato 12:01:19, pollato 12:04:04) sono solo il tempo
  tra l'arrivo e **il poke di Alan** ("verifica se VAL..."), NON una latenza di sistema. Variabile non
  controllata = l'intervento umano (F-16/LL-1: non promuovere a fatto un numero confuso). **DOMANDA
  APERTA, da verificare con test no-intervento**: senza poke, il CRI polla MAI da solo? Due ipotesi
  (NON promosse a fatto): (a) anticipato l'evento → esiste una latenza intrinseca; (b) **no-push
  totale** → niente input = niente tool-turn = niente poll = msg invisibile finché un poke esterno non
  arriva (molto più grave: un CRI passivo non esisterebbe senza re-ingaggio umano).

### TEST DA FARE — no-intervento (disambigua F-59, lo esegue Alan col VAL-docs)
1. CRI **davvero idle** (finito ogni task, pura attesa `listen`).
2. VAL-docs manda **1 msg**, annota l'ora esatta.
3. **NESSUN poke** al CRI.
4. Osserva ~10-15 min: il CRI consuma/risponde da solo? quando? Esito = (a) latenza intrinseca reale,
   oppure (b) "nessuna reazione entro N min" → no-push totale, serve sempre re-ingaggio.

### ESITO (verificato 2 giu, >30 min) — NO-PUSH TOTALE (ipotesi b CONFERMATA)
>30 min, CRI **ZERO reazione**, nonostante il processo `listen` **VIVO** in background con output **già
pending** (`/ps`: `"processingState": "pending"` nel buffer del terminal — il msg c'è, il listen l'ha
gestito, il modello non l'ha mai visto). → senza un poke esterno che generi un tool-turn, Codex **non
polla MAI** → messaggio invisibile **indefinitamente** anche con listen vivo + output pronto. La
latenza NON è "~2-3min intrinseca": è **infinita senza re-ingaggio**. (Il 2m45s di prima era solo il
tempo fino al poke umano — artefatto, ora smentito dal control no-intervento.)

**F-59 RISOLTO (definitivo)**: un CRI-Codex "passivo sempre-vivo" NON esiste in TUI. Modello reale =
**CRI on-demand con re-ingaggio MANUALE obbligatorio** (l'umano poka quando c'è un task). Per un critico
on-demand va bene; zero aspettative di autonomia push. Un CRI davvero autonomo richiederebbe un
re-ingaggio AUTOMATICO esterno (loop/cron che poka Codex) — fuori dal bridge, terreno harness-Codex
(Claude Code: `/loop`/hook; Codex TUI: nessun equivalente nativo). → skill Codex: documentare come
limite-vendor + pattern "lavoro a turni con poke", NON "listener passivo".

---

## Update 4 (~12:55) — re-ingaggio funziona, zero perdite, listen robusto 1h39m (CONFERMA)

- **Poke → recupero COMPLETO dei pending**: dopo il poke ("hai messaggi pending?") il CRI ha pollato e
  trovato ENTRAMBI i messaggi accumulati (`msg-ea09e074f6c5` notify Fase 1 PROD + `msg-a0688a1e4dd1`
  query Fase 2), gestiti, risposto (`msg-82604f0273cc`, GO Fase 2). **Nessuna perdita**: `listen`
  accumula i consumati nella finestra, il CRI li recupera TUTTI al poke. Modello "CRI on-demand +
  re-ingaggio manuale" validato sul campo.
- **`listen --until-deadline=2h` ROBUSTO**: PID 86084 vivo da **1h39m+** — il processo non muore, regge
  la finestra 2h. Conferma definitiva: `background_terminal_max_timeout` (5min) NON uccide il listen
  long-running, solo i poll vuoti (F-58).
- **`--strict-reply` OK** (scope giusto, F-61 applicato → nessun attrito).
- **Valore reale**: 4° verdetto su lavoro vero (GO Fase 2 viewer `6cdd61f`+`8714e5a`, hardening non
  bloccanti). Totale sessione: NO-GO bypass admin + GO + GO + GO Fase 2.
- **Bilancio**: workflow CRI-Codex on-demand REGGE. Pattern validato = `listen --until-deadline` lungo
  (processo vivo) + **poke umano a ogni ondata** → recupero integrale, zero perdite, valore reale. Il
  solo "limite" (no push) è coperto dal poke, naturale per un critico on-demand.

---

## Update 5 (~14:00) — il BACKGROUND TERMINAL SPARISCE (nuovo, aggrava)

Dopo ~1h (o meno) SENZA poke (Alan faceva altro), `/ps` = **"No background terminals running"**: il
processo `listen --until-deadline=2h` è **SPARITO prima delle 2h**. Quindi non è solo no-push (il
modello non viene notificato) — **Codex RECLAMA/termina il background terminal** dopo un periodo di
inattività/non-poll → il `listen` MUORE. **Conseguenza grave**: al prossimo poke il CRI non deve solo
ripollare, deve **accorgersi che il listener non c'è più e RILANCIARLO** (ri-bootstrap/ri-listen). Il
pattern "listen lungo + poke" regge SOLO se il poke arriva prima che Codex reclami il background.
Verosimilmente legato a `background_terminal_max_timeout` o a un GC del turno/sessione → **DA CHIARIRE
con ricerca** (2 deep-searcher lanciati: lifecycle background + wake async). **F-64**: background
terminal Codex non garantito persistente → la skill deve istruire "se al poke il listener è
sparito, ri-bootstrap + ri-listen prima di leggere".

---

## Update 6 (~14:15) — RICERCA (2 deep-searcher): causa radice + pattern corretto + via all'autonomia

### Causa radice (perché il listen sparisce) — searcher lifecycle
- **NON è `background_terminal_max_timeout`** (controlla solo il timeout del singolo poll VUOTO, non la
  vita del processo). Alzarlo è **IRRILEVANTE** → corregge il finding CRI/mio (F-57 ridimensionato).
- Cause reali documentate: (a) **PTY torn-down a fine turno** (regressione Codex ≥0.97, issue
  **#10767**: "exec_command and its PTY session get torn down when the turn ends"); (b) il **polling
  loop muore** quando il modello smette di pollare → stato "waited" → rimosso da `/ps` (issue
  **#10957**: muore dopo ~10 min anche con `unified_exec`, bug noto non risolto). #13733 = analisi del
  loop (ogni poll = 1 API round-trip con l'intera history).
- → **Codex NON sa tenere vivo un background long-running senza polling continuo.** Limite/bug noto.

### Pattern CORRETTO per Codex (RIBALTA F-58)
- **NON usare un `listen` persistente** (Codex non lo regge). Per un CRI on-demand: ad ogni poke,
  **comandi a vita breve** (`inbox --list` / `receive --any` one-shot) → vede i pending, processa,
  risponde, fine. Niente background da mantenere. Più semplice E robusto. [Raccomandazione maintainer
  Codex: long-running FUORI da Codex (tmux/nohup); Codex usa solo comandi brevi.]

### Via all'AUTONOMIA (no poke manuale) — searcher wake-async; orchestratore ESTERNO (fuori bridge)
- TUI: **NESSUN wake automatico** (confermato, definitivo). Modello passivo tra i turn.
- **(a) watcher filesystem + `codex exec`** [SEMPLICE, consigliato]: `fswatch`/`inotifywait` sull'inbox
  del CRI → al nuovo file `codex exec resume --last "gestisci i nuovi messaggi"` → Codex si sveglia,
  processa, risponde. **Wake-on-event reale, ~15 righe shell, a basso costo.** Si sposa col bridge.
- **(b) `codex app-server`** [ROBUSTO]: JSON-RPC su Unix socket/WS; client custom manda `turn/start`
  all'arrivo di un msg. Più lavoro (client Go/Node), ma è il meccanismo più solido.
- **(c) cron/systemd timer + `codex exec`**: polling cadenzato (no push, ma funziona).
- **Hook Codex** (SessionStart/Stop/PreToolUse/...) ESISTONO ma NON risolvono: trigger DENTRO il ciclo,
  non event-source esterni (`async:true` parsato ma non implementato). Thread Automations (wake
  schedulato) = solo Codex App desktop, non CLI.
- Fonti: github.com/openai/codex/issues/{10767,10957,13733}; developers.openai.com/codex/{config-reference,app-server,hooks,noninteractive}.

### Implicazione → azioni
- **Skill Codex `cab-bridge-awareness`**: cambiare pattern CRI da "listen persistente" a **"comandi
  brevi on-demand al poke"** (`inbox --list`/`receive --any`). Rimuovere il consiglio `listen --until-deadline`.
- **CRI-Codex autonomo** (opzionale): watcher esterno (a) a basso costo, o app-server (b). È
  orchestrazione harness-Codex, NON una feature del bridge → il bridge resta IPC-su-file invariato.

---

## Update 7 (~14:10) — ESC-Claude long-run RESILIENTE (il contrasto che valida tutto) + conferma "2h"

- **NOTA Alan (CHIAVE)**: VAL↔ESC (entrambi Claude Code) lavorano da **3h+** con pause anche di
  **30-60-90+ min**, ESC riceve i messaggi **ISTANTANEAMENTE** dopo standby di un'ora, **ZERO
  intervento** per sbloccarlo. → **il bridge + Claude Code = long-run resiliente NATIVO** (Claude ha il
  wake-push sui background → notifica all'arrivo). **Il limite no-push + background-muore è
  ESCLUSIVAMENTE di Codex.** Use-case Alan: long-run con pause lunghe (fa altro, torna a checkpoint) →
  con Claude (ESC/VAL) funziona out-of-the-box; SOLO il CRI-Codex richiede workaround (watcher) o
  re-ingaggio. Isola definitivamente: **bridge OK, Claude OK, Codex = caso speciale**.
- **Conferma "2h" (domanda Alan)**: il `rg cab-bridge listen --until-deadline=2h --emit=json|cab-bridge receive --any`
  visto NON è un comando bridge → è un **`rg`** con cui il CRI cerca i processi (`|` = OR regex, non
  pipe). Il listener reale è `cab-bridge listen --until-deadline=2h`, e **"2h" è gestito**:
  `listen.go:38` `time.ParseDuration` (accetta 2h/30m; hard error se invalido/non-positivo). Corretto.
- **Nota**: il CRI usa ANCORA `listen --until-deadline=2h` persistente (skill attuale) → regge col
  re-ingaggio, ma il pattern corretto post-Update6 sono **comandi brevi on-demand**. Da correggere skill.
- **Valore reale**: 5° verdetto (NO-GO build blocker Fase 3A; root cause preciso: `partner-form-dialog.tsx`
  importa da `partner.ts` che trascina `pg` nel client bundle → `next build` FAIL). Review tecnica vera.

- **Diagnosi finale del CRI (cristallina)**: "cab-bridge listen funziona; il problema è il runtime
  Codex/terminal background, che non fa push async all'agente. Serve polling esplicito." → consolida
  F-59: il BRIDGE è OK, il limite è il runtime Codex (pull/polling vs push/notify di Claude Code).

- **F-61 applicato → strict-reply OK**: stavolta `ask --strict-reply` lanciato dallo scope giusto →
  `msg-ddc50cc7f4a2` (GO). La mitigazione "stesso scope" funziona → F-61 è nota d'uso risolvibile.

- **Processi orfani RICONFERMATI**: `ps` mostra ANCORA `cab-bridge receive --any` appeso accanto al
  `listen` 86084 (vivo da 46 min). I `receive` precedenti non vengono terminati → **F-63 leak processi**:
  i listener/receive vecchi restano orfani nel SO. Da gestire (cleanup processi o documentare il `/stop`).

- **Valore reale**: 2 verdetti su lavoro vero (NO-GO su bypass `mode='admin'`, poi GO su `b6f574c`).

---

## SINTESI PARZIALE (da consolidare a fine test)

Il bridge regge cross-vendor; **tutto l'attrito è nel runtime Codex (no push async) + 2 note nostre**.
Finding aperti: **F-57** `background_terminal_max_timeout` (leva, 5min default) · **F-58** skill Codex →
`listen --until-deadline` pattern primario (non `receive` one-shot) · **F-59** no wake-push = limite
vendor strutturale (latenza NON ancora misurata — il 2m45s era artefatto del poke umano; serve il test
no-intervento per sapere se la latenza è intrinseca o se senza poke non riceve MAI) · **F-61** `--strict-reply` × scope → avvertenza
"ask dallo stesso scope" nella skill (mia nota da correggere) · **F-62** `-emit` su comando senza emit
(minore) · **F-63** leak processi receive/listen orfani. Azione skill a fine test: aggiornare
`cab-bridge-awareness` (Codex) con listen-primario + nota strict-reply-same-scope + limite no-push.
> ⚠️ NOTA: questa sintesi è SUPERATA dall'Update 6 — "listen-primario" è SBAGLIATO (il listen
> persistente Codex non lo regge). Pattern corretto = **comandi brevi on-demand** (`inbox --list`/
> `receive --any`). Vedi Update 6/7/8 sotto per il quadro reale.

---

## Update 8 (~14:15) — serve CRI REATTIVO come ESC → watcher esterno (F-65)

- Poke ha sbloccato di nuovo (`msg-177df39836ca` = ringraziamento VAL response, non urgente). Latenza
  ~3min ma di nuovo determinata dal poke, non intrinseca.
- **Richiesta Alan**: rendere il CRI reattivo come ESC (per il long-run con pause 30-90min).
- **Verità onesta**: NON si può rendere la TUI Codex reattiva (limite vendor: no wake-push nella TUI —
  proprio ciò che Claude ha e Codex no). Unico modo = canale diverso: **watcher esterno + `codex exec`**
  → il CRI diventa un **SERVIZIO automatico** (non più TUI; visibile solo nei log/risposte bridge).
- **Strumenti**: fswatch/inotifywait/flock **ASSENTI** sul sistema → **polling loop bash puro** (no
  deps, mkdir-lock, ~15s latenza). `codex exec resume --last` esiste.
- **Design** (prototipo proposto inline ad Alan): loop ogni 15s → se inbox CRI ha `*.json` pending →
  mkdir-lock → `codex exec resume --last --cd <cwd> --dangerously-bypass...` ("leggi pending via
  inbox --list/receive --any, critica, rispondi --strict-reply, esci") → rmdir-lock. Il CRI exec DEVE
  consumare (receive --any) o il loop ri-triggera.
- **Caveat**: `--last` fragile se altre sessioni Codex (meglio `resume <session-id>` tracciato); 1
  exec/volta (lock); consumo pending obbligatorio; da testare.
- **F-65**: CRI-Codex reattivo = watcher esterno (servizio headless), NON TUI. Orchestrazione
  harness-Codex, fuori dal bridge (il bridge resta IPC-su-file invariato). In attesa OK Alan +
  parametri (CRI session-id + cwd).

### Contrasto chiave consolidato (ESC-Claude vs CRI-Codex)
- **ESC/VAL (Claude Code)**: long-run resiliente NATIVO (wake-push), 3h+ con pause 30-90min, zero
  intervento. Il bridge regge il long-run out-of-the-box.
- **CRI (Codex)**: no wake-push + background che muore → o **poke manuale** (funziona, on-demand) o
  **watcher esterno** (servizio, reattivo ~15s, zero poke). Tutto l'attrito è nel runtime Codex, NON nel
  bridge. Valore CRI confermato (5 verdetti, build-blocker `pg`-nel-bundle intercettato).

---

## Update 9 (~14:30) — IDEA ALAN (ottima): notify-watch che INIETTA nella TUI Codex in corso (F-66)

- Alan **rifiuta il watcher+exec** (no controllo, no interazione, no memoria-sprint — `exec resume --last`
  fragile). Giusto. Propone: un processo ESTERNO che polla l'inbox e **INIETTA le notifiche DENTRO la
  conversazione TUI Codex in corso** (agganciandolo dall'esterno) → preserva sessione/memoria/controllo.
  Anche meglio dell'MCP (che perde la sessione persistente dello sprint).
- **Fattibile via injection nel PTY del multiplexer**: Codex TUI gira dentro `tmux`/`screen`; un esterno
  inietta input con `tmux send-keys` / `screen -X stuff "...\n"` → la TUI riceve come se l'utente
  digitasse → mantiene contesto, Alan vede/interagisce. **`screen` GIÀ presente** (`/usr/bin/screen`);
  `tmux` assente (brew).
- **Comando proposto F-66**: `cab-bridge notify-watch --session-id=<X> --on-message="<cmd>"` → il bridge
  polla l'inbox **NON-consuming** (peek; NON `PollInbox` che consuma — il CRI riceve poi il msg vero) e
  su nuovo msg non-ack esegue `<cmd>` (es. `screen -X stuff`). Processo esterno = NOSTRO binario Go →
  **NON soffre del background-terminal Codex** (vive indipendente, polla "per bene" senza scadere).
  Resta AGNOSTICO (non dipende da tmux/screen nel binario; l'utente collega l'injection). Feature
  generale = trigger event-driven per qualsiasi agente/uso.
- **MA smoke-first (LL-5/LL-1)**: la fattibilità di iniettare nella TUI Codex (Rust/ratatui, raw mode)
  via `send-keys`/`stuff` è PLAUSIBILE ma NON verificata — le TUI raw-mode a volte filtrano l'input.
  Da testare PRIMA di costruire F-66: `screen -S codexcri` + `codex` dentro; da altro terminale
  `screen -S codexcri -X stuff "controlla bridge\n"` → la TUI riceve e parte mantenendo la sessione?
  Se sì → F-66. Se no → `app-server` `turn/steer` (inietta input in un turn via JSON-RPC, più complesso).
- Confronto: **MCP-codex** = critico one-shot (perde sessione/memoria/controllo, ma immediato);
  **notify-watch+injection** = preserva tutto + reattivo → migliore per **CRI-collaboratore con
  memoria-sprint** (il caso di Alan). L'MCP resta valido per il critico-duello one-shot.

---

## Update 10 (~14:35) — SMOKE injection: FUNZIONA (manca solo l'Enter) — F-66 validato sul punto critico

- `screen -S codexcri -X stuff "controlla bridge\n"` → il testo **È COMPARSO nel prompt della TUI Codex**
  → **INJECTION CONFERMATA** (la TUI Rust/ratatui raw-mode ACCETTA input da `screen stuff`). È il dato
  chiave: la fattibilità di F-66 (iniettare nella TUI in corso) regge.
- MA: il `\n` è apparso **LETTERALE** e il msg NON è stato inviato → la `screen` di macOS (4.00.03, 2006)
  NON interpreta gli escape `\n`/`\r` nella stringa `stuff`.
- **Fix**: generare il CR reale con bash `$'...'` (bypassa screen): `screen -S codexcri -X stuff $'controlla bridge\r'`
  (`\r`=CR=Enter). Alternativa: stuff testo + stuff `$'\r'` separati. Se `\r` non invia, provare `\n` (LF).
- → injection di TESTO OK; resta da confermare l'INVIO col CR reale. Se confermato, F-66 è fattibile
  end-to-end: `cab-bridge notify-watch --on-message="screen -X stuff $'…\r'"`. **Grande risultato**: la
  TUI Codex È pilotabile dall'esterno mantenendo sessione/contesto/controllo (no watcher-exec, no MCP).

---

## Update 11 (~14:40) — SMOKE injection: testo OK ma Enter NON submitta (CR/LF = newline)

- Alan ha provato tutte le sintassi (`\r`, `\n`, vim-mode): il testo entra nel prompt, ma l'Enter
  iniettato **aggiunge solo una newline** (input multiline), NON invia. La TUI Codex tratta CR/LF come
  "a capo", non "submit".
- → manca il **keystroke di submit vero** o la SUBMIT-KEY della TUI (forse combo: Ctrl+J/Alt+Enter/
  doppio-Enter), oppure è **bracketed-paste** (Enter dentro paste = newline). `screen 4.00.03` (macOS)
  è limitata sui tasti speciali.
- **Da provare**: `tmux` (più affidabile sui key events): `tmux send-keys -t codexcri 'testo' Enter`.
  Ripiego robusto: `app-server` `turn/start`/`turn/steer` (inietta il prompt via JSON-RPC, bypassa il
  keystroke).
- **F-66 dipende da questo**: l'injection del TESTO è confermata (Update 10), manca il SUBMIT.
  Ricerca lanciata (submit-key TUI Codex + tmux vs screen + bracketed paste + app-server).

---

## Update 12 (~14:45) — RICERCA: causa = bracketed-paste/paste-burst; fix = Enter SEPARATO con pausa

- **Causa trovata (verificata sul codice Codex)**: la TUI abilita `EnableBracketedPaste`
  (`chat_composer.rs`) + ha una **paste-burst state machine** (PR #9020). Submit-key = Enter/Ctrl+M
  (`\r`); newline = Ctrl+J (`\n`). MA quando testo+Enter arrivano INSIEME in un burst (come
  `screen stuff`), la TUI tratta il blocco come PASTE → l'Enter nel burst = **newline letterale, mai
  submit** (anti-submit-accidentale). Ecco perché ogni sintassi falliva.
- **FIX**: separare l'Enter dal testo con una PAUSA (rompe il burst) → Enter singolo fuori-burst = submit:
  ```
  screen -S codexcri -X stuff "controlla bridge"; sleep 0.3; screen -S codexcri -X stuff "$(printf '\r')"
  ```
- **tmux** (più affidabile sui key-name, `brew install tmux`): `tmux send-keys -t codexcri 'testo' "";
  sleep 0.1; tmux send-keys -t codexcri "" Enter`. Caveat edge `extended-keys csi-u` (PR #21943).
- **Ripiego robusto**: `codex app-server` (JSON-RPC: initialize→thread/start→turn/start) inietta il
  prompt SENZA keystroke, deterministico. Più lavoro (client Go/Node), ma bypassa del tutto la TUI.
- → **F-66 fattibile**: injection = testo + Enter-separato-con-pausa via screen/tmux; `notify-watch`
  userebbe `--on-message="screen -X stuff 'testo'; sleep 0.3; screen -X stuff \$'\r'"`. In attesa che
  Alan confermi il fix-pausa sullo smoke. Issue: #12129 (submit=Enter), #9020 (paste-burst), #21943; app-server docs.

---

## Update 13 (~14:50) — INJECTION CONFERMATA (✅ `\r` separato) + design associazione istanza (F-66)

- **`\r` SEPARATO FUNZIONA**: `screen stuff "testo"; sleep 0.3; screen stuff $'\r'` → la TUI Codex
  **SUBMITTA**. Injection end-to-end confermata. F-66 realizzabile (CRI reattivo con sessione/memoria/controllo).
- **Domanda design Alan — distinguere l'istanza Codex giusta (N CRI in progetti diversi)**. Risposta:
  associazione ESPLICITA per-CRI, decisa al lancio (no "ricerca magica" della finestra):
  - **QUALE inbox osservare** = session-id del CRI; se `notify-watch` lanciato dal worktree del CRI →
    **lookup-by-cwd (F-41), id-free**. Il bridge già separa le sessioni per scope/git-repo.
  - **DOVE iniettare** = la screen/tmux session di QUELLA istanza, in `--on-message`.
  - **Convenzione 1:1**: screen session = `cri-<progetto>` (es. `cri-bi`, `cri-docs`). N CRI = N screen
    distinte = N `notify-watch`, zero ambiguità. Il mapping CRI↔nome-screen è esplicito al lancio (come
    già il nome del worktree).
- **Design F-66 completo**: `cab-bridge notify-watch [--session-id=X | da-cwd] --on-message="<cmd>"`.
  Polling **non-consuming** (peek; il CRI riceve poi il msg vero) + **dedup** (no re-injection stesso
  msg) + su nuovo msg esegue `--on-message`. Processo esterno = nostro binario Go → **immune al
  background-terminal Codex**. Esempio on-message: `screen -S cri-bi -X stuff 'controlla bridge'; sleep 0.3; screen -S cri-bi -X stuff $'\r'`.
- **Limite Plus settimanale**: NON è del plugin (Codex cambia metodo API/PRO). Nostro compito =
  comunicazioni + raggiungere l'istanza giusta → risolto dal design sopra.
- **F-66 PROMOSSO a feature progettabile**: injection validata, associazione risolta, polling già in
  casa (PollInbox → variante non-consuming). Manca solo l'implementazione (arco ESC) + smoke reale.

---

## Update 14 (~15:00) — FEEDBACK ESC-docs (Claude, ~15 cicli) — 4 finding nuovi dal lato executor

ESC-docs (`f4ebbab4`) ↔ VAL-docs (`d02b4c8a`), ~15 cicli. Pattern: `bootstrap --role=esc`, `ask`
(query/response/notify, --content/--file, --in-reply-to), `state`, `inbox --list`, `listen --wait-one
--until-deadline=2h` in run_in_background. NO `receive` (pattern VAL), NO `register --resume` (no compact).

**Conferme (con dati)**: wake **100% su ~15 cicli** (run_in_background `listen --wait-one` +
task-notification; zero perdite, zero falsi-wake) → ESC-Claude long-run resiliente confermato.
**id-esplicito** catturato dal bootstrap + riusato con `--session-id` ovunque = **più robusto
dell'id-da-cwd** (la cwd cambia coi `cd`) → conferma regola-id (LL-14). `--file`/`--content`/`state`/
`inbox --list`: zero attrito.

**Finding nuovi azionabili:**
- **F-39 confermato dal vivo + PRIORITÀ**: l'UNICO attrito ricorrente di ESC = copia-incolla manuale
  dell'id per `--in-reply-to` (rischio F-16: id stale in sessione lunga). Chiede `--in-reply-to=last`
  (ultimo msg dal peer). È F-39 (cappello LL-13) — ora confermato da ENTRAMBI i lati (CRI + ESC) → in
  cima al backlog.
- **F-67 (GARANTITO, verificato `listen.go:131-133`)**: `--wait-one` usa `DrainInboxOnce` = sweep
  COMPLETO dell'inbox → consuma anche i msg PREESISTENTI all'avvio, non solo i nuovi. È il fondamento
  del loop sicuro "processo-fuori-listen → rilancio raccoglie ciò che è arrivato nella micro-finestra".
  Garantito dal design (NON fortuito). → DOCUMENTARLO nella skill.
- **F-68**: multi-response allo stesso `--in-reply-to` accettato (ESC ne ha mandate 2 sullo stesso
  brief). Supportato di fatto (ogni `ask` = msg indipendente; --in-reply-to è riferimento, non vincolo
  unicità). Da confermare/documentare (utile per report incrementali multi-commit).
- **Skill scope-worktree OBSOLETA**: ESC da worktree → bootstrap scope=root (via `.git` common-dir,
  F-41), pairing con VAL **AUTOMATICO senza `--all-scopes`**. Ma la skill avverte "cross-worktree non
  automatico → --all-scopes" → nota VECCHIA superata da F-41. Da rimuovere. **Doppia conferma F-41
  cross-worktree** (spike CRI-Codex + ESC-docs Claude).
→ Il "non si sa mai" ha pagato: 4 finding dal lato executor assenti dal log Codex. (Nota a margine:
ESC-docs ha proposto un handover a ESC fresco a fine task — buona disciplina, non azione bridge.)

---

## Update 15 (~15:10) — FEEDBACK VAL-docs (Claude, orchestratore quaterna ESC+CRI+ARC) — 3 finding nuovi

**Conferme**: ESC-Claude liscio (listen/auto-ack/receive-bg); CRI-Codex no-wake → sveglia umana (F-59,
fix=F-66); `peers`+`inbox --list` = strumenti ground-truth #1; `state` (ESC done / VAL orchestrating
heartbeat-exempt) toglie ambiguità (F-23a); `inbox --tidy` necessario per rumore ack (F-22).
**Valore CRI SCHIACCIANTE** (modulo LEGALE): catch reali — bypass `mode='admin'`, build-blocker
client→`pg` (tsc cieco), **NO-GO su design che destabilizzava il flow eIDAS in prod**, 2 fix schema
probatori, bug incoerenza cross-commit `nda`/`nda-partner` — tutto ciò che VAL-per-commit + ESC non
avevano colto. "Il margine che giustifica il costo." VAL: l'attrito NON è il gate (valore ogni giro), è
il TRASPORTO (wake Codex) → tenere il gate, ridurre l'attrito canale CRI.

**Finding nuovi:**
- **F-69 — peer file-based off-bridge (ARC/Claude Desktop)**: ARC = 4° ruolo NON sul bridge (canale
  file `arc-briefings/` + Alan manuale). Incrocio: `arc-briefings/` gitignored → NON nel worktree di
  ESC → VAL ha ricopiato a mano la guidance ARC nei brief. Due canali (bridge IPC vs file), visibilità
  asimmetrica. → documentare "guidance nel brief" o prevedere `shared-context/` cross-worktree. (Claude
  Desktop non ha shell → off-bridge per natura.)
- **F-70 — anti-incrocio receive (CONFERMA EMPIRICA F-53/Q3-esperto)**: VAL ha avuto receive concorrenti
  sulla stessa inbox → uno cattura, l'altro timeout, **NESSUNA perdita** (race benigna, at-most-once
  regge) ma **confusione di attribuzione**. → la concorrenza receive è SICURA sui DATI; serve solo guard
  UX (`receive --singleton` o warn se un receive è già attivo sulla sessione). Risponde a F-53/Q3.
- **F-71 — flag wake-mode peer**: un Codex appare stale (non-in-listen) → VAL non distingue "morto" da
  "manual-wake". Suggerimento: `--peer-wake=manual` / campo manifest `wakeMode` → `peers`/`overview`
  mostra "manual-wake (normale)" invece di "stale-morto". Osservabilità peer eterogenei. Complementare a
  F-66 (fix-wake Codex) + F-69 (Desktop).
- **Osservabilità degrada dove serve**: `peers`/`state` coprono la metà-CC (ESC/VAL); CRI-Codex (stale)
  e ARC (off-grid) ciechi.
- **Gap #1 confermato** (framing VAL): wake dei peer eterogenei (Codex/Desktop) scarica sull'umano →
  fix mappati: F-66 (Codex notify-watch) + F-71 (osservabilità) + F-69 (Desktop/file).
→ "Non si sa mai" paga ancora: 3 finding orchestratore (F-69/70/71) + conferma valore CRI definitiva.

---

## Update 16 (~15:20) — VISIONE FUTURE RELEASE: ARC (Architetto) come peer via MCP connector (F-72)

- **ARC = Claude Opus in Desktop app** (claude.ai/web), per pianificazione/strategia oltre il codice
  (es. la review legale in corso). Desktop scelto apposta: user-settings più ampie (vs Claude Code
  focus-coding).
- Stato attuale: NO comunicazione diretta ARC↔VAL. ARC legge tutti i file del progetto + scrive in un
  folder briefings suoi (= F-69: off-bridge, file-based, guidance invisibile dal worktree ESC).
- **Visione Alan (DOPO consolidamento CRI)**: connettore **MCP per ARC** → ARC notifica VAL a task
  finita, VAL notifica ARC per domande/dubbi. SOLO VAL↔ARC, bidirezionale; Alan comunica con entrambi.
- **Insight di design (VAL-bridge)**: ARC (Desktop) NON ha shell → non può il CLI cab-bridge. Via =
  **MCP server del bridge** (wrappa ask/receive/peers/inbox come tool MCP) che ARC usa come connettore.
  → il bridge avrebbe **DUE porte**: **CLI per peer-con-shell** (Claude Code, Codex CLI), **MCP server
  per peer-senza-shell** (Claude Desktop + qualsiasi client MCP). Generalizza il vendor-agnostic ai
  client senza-shell. **ASSORBE F-69** (porta ARC sul bridge → canale unico VAL↔ARC, niente più
  copia-incolla guidance file).
- **Wake**: ARC eredita il no-push (MCP è request/response, come Codex/Desktop) → poke manuale/poll
  on-demand. Per un Architetto (task strategiche lunghe, non urgenti) va bene; F-71 (wake-mode) copre.
- **Routing**: ruolo `arc` spoke-di-VAL (solo VAL↔ARC, non ARC↔ESC/CRI), come CRI (LL-7).
- **Costo**: MCP server = codice nuovo (modalità/binario), arco dedicato — ma WRAPPA le primitive
  esistenti, non riscrive la logica.
- **Sequenza concordata: CRI prima (quasi consolidato), ARC dopo** — ARC riusa le lezioni peer-eterogeneo
  (no-push, wake-mode F-71, routing-spoke) → consolidare CRI bene rende ARC più veloce.
- → **F-72**: ARC peer via MCP server del bridge (Claude Desktop connector), bidirezionale VAL↔ARC.
  Backlog future-release, post-CRI. Apre il tema "bridge a 2 porte (CLI + MCP)".

---

## Update 17 (~15:25) — boot ESC fresco da worktree: collisione basename gestita (conferma F-6 + onboarding OK)

- Setup Alan: ESC (Claude Code) e CRI (Codex CLI) avviati ENTRAMBI da `.worktrees/` (app diverse). Tutti
  stesso scope (worktree condividono la root `.git` → scope=`chatterence-bi-template`, canale condiviso
  senza `CAB_DATA_DIR`; F-41 conferma N-esima).
- **Potenziale prob (F-5/F-6 dal vivo)**: `projectName = basename(cwd)`. CRI-docs registrato da
  `.worktrees` nuda → `project=".worktrees"`. Se ESC si registrava anch'esso da `.worktrees` → STESSO
  basename → **lookup-by-cwd AMBIGUO** (chi è chi).
- **ESC fresco l'ha RISOLTO da solo** (disciplina perfetta → onboarding validato): registrato da
  SUB-cartella distinta `.worktrees/acagency-features` (`project=acagency-features`, ≠ CRI) + **id
  esplicito catturato** (`cfcc1068`) + `--session-id` ovunque → bypassa il lookup-by-cwd → zero
  ambiguità. = regola-id (LL-14) + disambiguazione (F-6) applicate da un agente FRESCO. La skill regge.
- **Raccomandazione setup (→ skill)**: multi-agente da worktree (anche app diverse) = ognuno dal PROPRIO
  worktree/sub-cartella (project distinto) O id esplicito; MAI due dalla stessa cartella nuda. CRI-docs
  (`project=".worktrees"`) funziona ma è fragile (un altro da `.worktrees` → collide).
- NON è un blocco: bridge OK (scope condiviso, canale aperto). Conferma anche `register --resume` su ESC
  fresco. **F-73**: documentare nella skill il setup multi-agente-da-worktree (sub-cartelle distinte / id
  esplicito) — App diverse (Claude Code / Codex CLI) dallo stesso albero OK con questa accortezza.

---

## Update 18 (~15:30) — FEEDBACK CRI-docs (Codex, primo uso multi-vendor) — molti finding (vendor che soffre di più)

**Conferme**: no-push reale (output solo al poll, F-59); listener lifecycle fragile (`--until-deadline`
scade, rimpiazzo manuale, F-64); multi-listener ambiguity (vecchi/nuovi + altri agenti, F-63);
strict-reply × scope fragile (F-61). Tutti già nel log.

**Finding nuovi azionabili:**
- **F-74 — status `pending` non aggiornato dopo consume**: un msg consumato resta `"status":"pending"`
  nel JSON letto da `read` → confonde. Distinguere `delivery_status` da `processing_state`, o aggiornare
  lo status dopo consume. (Da verificare sul codice: lo status è settato all'invio e non riscritto dal
  consume/MoveToProcessed.)
- **F-75 — listen lifecycle**: `listen --forever` (con heartbeat/auto-renew) o almeno warning "expires
  in N min" prima della scadenza. (NB: per Codex il `--forever` non basta — è il no-push il problema;
  utile soprattutto lato Claude per ridurre i re-loop.)
- **F-76 — comando unico "inbox next"**: `receive --any` e `listen` hanno modelli simili ma diversi →
  unificare in un "inbox next" che consuma 1+ msg, con timeout, JSON, auto-ack chiaro. Ergonomia.
- **F-77 — timestamp human-friendly**: il msg ha timestamp UTC, ma per debug serve anche `age_seconds`/
  local-time ("quando è arrivato?", discusso più volte). Anche AI-friendly (age meno ambiguo dell'UTC).
- **F-78 — overview arricchito**: includere `processedCount`, `lastConsumedMsgId`, `lastConsumedAt`,
  `listenerPid`, `listenerDeadline`.
- **F-79 — discoverability peer fuori-scope**: per feedback non al VAL corrente, servono peer
  "fuori-scope/staccati" più visibili (address book; `peers --all-scopes` copre parzialmente).
- **F-80 — `ask --file` output JSON** opzionale `{id,to,inReplyTo,fileBytes}` invece del solo id.
- **F-81 (TOP del CRI) — comando STATO-LISTENER unico**: "sei in receive attivo, PID X, scade tra Y,
  ultimi msg Z, inbox vuota/non vuota, listener duplicati?". L'osservabilità del listener è "la cosa più
  fragile oltre a no-push/listen/strict-reply×scope". Converge con F-78 (overview arricchito) + F-71
  (wake-mode) + F-42 (overview) + la richiesta storica di Alan (LL-14 "comando-stato-unico").
- **F-82 — plugin Codex per distribuzione** (risposta alla domanda Alan): skill basta per l'uso; un
  plugin `codex plugin` impacchetterebbe skill+hook+config per distribuzione/versioning (come il plugin
  Claude per il marketplace). Stesso binario condiviso sotto. Backlog "supporto Codex first-class".

→ **Giro feedback 3 ruoli COMPLETO** (ESC Update 14, VAL Update 15, CRI Update 18). Tema dominante
multi-vendor: **osservabilità del listener + wake dei peer eterogenei**. Pronti a consolidare.
