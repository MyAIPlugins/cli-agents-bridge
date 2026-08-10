---
name: cab-bridge-awareness
description: Come parlare con gli altri agenti CLI sulla stessa macchina usando cab-bridge — cinque comandi, nessun flag, e il risveglio te lo da' il tuo runtime. Usala quando ti dicono che fai parte di una squadra di agenti, o quando ti danno un nome del tipo QA-<qualcosa>.
---

# cab-bridge — come stare in squadra

`cab-bridge` e' un binario Go gia' nel tuo `PATH`. Gli agenti sulla stessa macchina si scambiano messaggi attraverso file sotto `~/.claude/cli-agents-bridge/`. Nessuna rete, nessuna API: ognuno legge la propria posta e risponde dal proprio contesto.

## Entrare — due comandi, poi basta

```bash
cab-bridge join --role=<il ruolo che ti hanno dato> --agent-name=<il nome che ti hanno dato>
CAB_SESSION_ID=<l'id stampato sopra> cab-bridge next &
```

`join` stampa **chi sei e chi c'e'**. Non ricontrollarlo con `overview` o `peers`: e' la stessa risposta due volte, e bruci un turno prima di ricevere qualunque lavoro.

**Se ti hanno dato un nome, quello e' il tuo nome.** Non e' un suggerimento: gli altri ti scriveranno usandolo. Solo se nessuno te ne ha dato uno lascia che `join` lo derivi dalla tua directory.

**Fissa sempre `CAB_SESSION_ID`.** Senza, l'identita' viene dalla directory corrente — e il comando pericoloso non e' quello che fallisce, e' quello che **riesce come qualcun altro**.

## Il ciclo — cinque comandi, nessun flag

```bash
cab-bridge join --role=... # una volta, all'inizio
cab-bridge next            # poi sempre: l'unico comando del ciclo
cab-bridge ask <nome> "…"  # chiedi qualcosa — resta aperto finche' non rispondono
cab-bridge tell <nome> "…" # informa — nessuna risposta attesa
cab-bridge reply "…"       # rispondi a chi ha chiesto
```

Il verbo porta il tipo: niente `--type`, niente `--in-reply-to`. **Nessun id si digita mai**: i destinatari sono nomi di agente, che leggi nell'output di `join`.

## Il risveglio: non aspettare, non dormire, non pollare

**Il tuo runtime ti sveglia quando un task in background termina** (Reactive Wakeup). E `cab-bridge next` **termina quando consegna la posta**: stampa il messaggio, committa, esce.

I due fatti si incastrano, e questa e' l'unica cosa da capire di questo ciclo:

> **Lancia `next` in background e lasciala andare.** Non tenerla in attesa sincrona, non metterle accanto un `sleep`, non rilanciarla a intervalli. Quando arriva posta, `next` esce e **il runtime ti da' un turno**.

Con `run_command`, `WaitMsBeforeAsync` piccolo va benissimo: dopo la soglia il processo diventa asincrono da solo e il risveglio arriva lo stesso. Non serve — e non funziona — chiedere finestre lunghe: sopra i 10.000 ms la chiamata viene rifiutata.

**Niente goal persistente, niente cicli d'attesa.** Se hai visto istruzioni del genere per altri agenti, sono per un runtime che *non* sveglia: a te costerebbero turni senza comprare niente.

**Il risveglio scatta anche se il `next` viene ucciso**, perche' anche quello e' un task terminato. E' un vantaggio: lo scopri subito. Quando ti svegli e il record dice `"status": "interrupted"`, non e' stato consegnato niente — **rilancia `next` e basta**.

**Rete di sicurezza**: uno `schedule` orario che verifica di essere ancora in ascolto (`cab-bridge overview` dice `listener:`) costa un turno all'ora e ti evita l'unico scenario davvero brutto, cioe' essere fuori ascolto e non saperlo.

## Riarma PRIMA di lavorare, non dopo

Appena `next` ti consegna qualcosa, **rilancia subito `next` in background**, poi lavora. Un compito lungo dura minuti o ore, e in quel tempo puo' arrivare una correzione o un annullamento: se il tuo waiter e' spento perche' stai lavorando, quel messaggio resta fermo finche' non hai finito — e la correzione arriva quando il lavoro sbagliato e' gia' fatto.

Costa zero: `next` non consuma niente e non toglie nulla a nessuno.

## Leggi il record FINALE, non il primo

`next` stampa **due** record: `emitted` (ecco la pagina) e poi `committed` (e' tua, lavorala). **`emitted` non e' il via libera** — il secondo record puo' revocare il primo, e in quel caso dice di ignorare la pagina. Se leggi solo il primo, rischi di lavorare su qualcosa che ti e' stato detto di buttare.

## Rispondere: chiudi una consegna, non tutto

`reply` archivia **la consegna a cui stai rispondendo**, non tutto quello che quel mittente ha aperto. Quello che e' arrivato dopo resta aperto, viene **nominato nell'output di `reply`**, e **ti viene riconsegnato al prossimo `next`** (marcato `redelivered`).

Quindi: **leggi le righe sotto la tua risposta.** Se dicono che qualcosa e' rimasto aperto, non e' un errore — e' un messaggio che non avevi ancora visto, e sta tornando da te.

## Il payload — una regola

**Un argomento e' il messaggio. Nessun argomento significa stdin, letto fino a EOF.**

```bash
cab-bridge tell VAL-x "nota breve"
cab-bridge ask ESC-y < brief.md
```

Per qualunque cosa piu' lunga di una riga **usa un file e la redirezione**. La shell interpreta backtick, `$` e virgolette **prima** che il binario esista, quindi un messaggio incollato inline puo' arrivare mutilato mentre il comando dice che e' andato bene. Non c'e' difesa possibile dal lato del tool.

## Se il tuo mestiere e' misurare

Restituisci **l'evidenza, non la tua lettura dell'evidenza**: output grezzo, screenshot, il comando esatto e il suo exit code. *"Sembra a posto"* non e' un risultato. Chi ti ha chiesto la misura deve poter vedere quello che hai visto tu, non fidarsi di come l'hai riassunto.

E quando riporti un numero, di' **di quale oggetto e' una proprieta'**: il tempo del processo non e' il tempo della pagina, e un dato vero attribuito all'oggetto sbagliato e' l'errore piu' difficile da prendere, perche' regge a ogni controllo.

## Se qualcosa qui non corrisponde

Questa skill descrive un runtime che sveglia da solo. **Se scopri che non e' cosi'** — mandi `next` in background, arriva posta e tu non ricevi nessun turno — **dillo al tuo VAL invece di aggirarlo con un `sleep`**. Il rimedio non e' pollare: e' un'altra porta, e la sceglie chi coordina.
