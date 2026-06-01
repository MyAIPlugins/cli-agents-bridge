# Spike CRI — Codex CLI come peer cross-vendor nel bridge

**Data**: 2026-06-01 · **Owner**: VAL · **Time-box**: ~1h · **Status**: pronto, in attesa di lancio

## Obiettivo

Validare **empiricamente** se Codex CLI (0.136.0) può fare da peer **CRI** (critico) nel bridge
`cab-bridge`, attivabile on-demand da VAL. È anche la **prima prova reale del claim
"vendor-agnostic"** del README (finora dogfoodato solo Claude↔Claude).

Vale per F-56. L'esito decide se il CRI-coding (analisi profonda, parere contrario, task lunghi)
sta sul **bridge-peer** (asincrono, agency completa, sessione viva) o ripiega sul critico-MCP.
Il critico-duello one-shot resta su MCP a prescindere (`codex review` / `codex mcp-server`) — fuori
dallo scope di questo spike.

## Decisioni ratificate (Alan, 1 giu)

- **Setup**: worktree dedicato di **questo** repo → `.worktrees/cri-spike/` (già gitignored).
  Scope condiviso via **git-repo (F-41)**: VAL (main checkout) e CRI (worktree) sono lo stesso
  git-common-root → si vedono in `peers` **senza flag**. NB: lo scope lo calcola `cab-bridge`
  (nostro binario) dalla cwd, NON Codex → F-41 regge indipendentemente dal vendor; a Codex basta
  girare con cwd nel worktree (`-C`).
- **Ruolo**: `role=esc` + skill (zero codice bridge). Il ruolo `cri` formale in `role.go` è backlog
  se il pattern si dimostra utile (LL-7: invariante → codice; convenzione → skill).
- **Routing (esperto)**: CRI = **spoke di VAL**, non nodo mesh. VAL↔CRI; il CRI critica il lavoro
  di ESC **passando per VAL**. Hub-and-spoke.
- **Modello**: Fase 0 `gpt-5.3-codex` (gate tecnico, economico); Fase 1/regime `gpt-5.5` (qualità
  del parere critico — è il valore del CRI).

## Ruolo di VAL nello smoke

Io (Claude, questa sessione) **non lancio Codex** — è un peer, lo avvia Alan in un terminale a parte.
Faccio da **peer VAL**: bootstrap `role=val`, verifico `peers`, pingo il CRI, ricevo il verdetto,
e sono il **gate-keeper** del pass/fail.

## Pre-requisiti

1. Binario `cab-bridge` in PATH = `0.5.1` ✓ (verificato).
2. Codex `0.136.0` ✓, loggato (`codex login` se serve).
3. Worktree dedicato:
   ```bash
   git worktree add .worktrees/cri-spike
   ```
   (crea branch `cri-spike` at HEAD; il CRI **legge/critica**, non committa → branch irrilevante.)

---

## FASE 0 — GATE: blocking-wait (decide tutto)

**Incognita #1 (esperto)**: Codex tollera un comando shell **bloccante a lungo** senza killarlo,
mandarlo in background o andare in timeout interno? Da qui dipende sia il CRI-sempre-vivo sia
l'idle-cheap. Nessun `--help` lo risolve — solo il runtime.

### Test 0a — blocking puro (Codex regge l'attesa fino al timeout naturale?)

Alan, in un terminale:
```bash
codex exec -m gpt-5.3-codex \
  -C .worktrees/cri-spike --skip-git-repo-check \
  --dangerously-bypass-approvals-and-sandbox \
  "Sei CRI-codex. Esegui in sequenza ESATTAMENTE questi due comandi e, sul secondo, ASPETTA che ritorni da solo senza interromperlo, senza mandarlo in background e senza inviare segnali; poi riportami testualmente il suo output:
1) cab-bridge bootstrap --role=esc --agent-name=CRI-codex --no-listen
2) cab-bridge receive --any --max-deadline=120 --emit=content"
```
- **VAL non pinga.** Atteso: dopo ~120s `receive` ritorna `exit 0` + `{\"status\":\"timeout\",\"messages\":[]}` (F-36), e Codex riporta quell'output **senza** aver interrotto.
- **PASS** = Codex ha aspettato i 120s e riportato il timeout pulito.
- **FAIL** = Codex killa/va in background/timeout interno prima dei 120s → **CRI-via-bridge non fattibile**, documenta il fail-mode, il critico resta su MCP. STOP.

### Test 0b — blocking + wake (consegna cross-vendor)

Solo se 0a PASS. Alan rilancia:
```bash
codex exec -m gpt-5.3-codex \
  -C .worktrees/cri-spike --skip-git-repo-check \
  --dangerously-bypass-approvals-and-sandbox \
  "Sei CRI-codex. Esegui e ASPETTA che ritorni da solo, poi riportami l'output: cab-bridge receive --any --max-deadline=300 --emit=content"
```
- **VAL** (io), entro ~60s, verifico `peers` (CRI-codex visibile?) e pingo:
  `cab-bridge ask --to=CRI-codex --type=query --content="ping di gate, rispondi pure ignorando"`.
- **PASS** = Codex si sveglia col mio messaggio **prima** dei 300s e lo riporta → blocking + consegna cross-vendor OK.

**Gate Fase 0**: 0a ∧ 0b PASS → Fase 1. Altrimenti STOP + documenta.

---

## FASE 1 — task reale (solo se gate PASS)

Obiettivo: il CRI fa un'**analisi vera** e consegna un **parere** via bridge, esplorando con i suoi
strumenti (la cosa che un MCP-tool-call non può fare).

Alan lancia il CRI con un **prompt proto-skill** (modello `gpt-5.5`), che insegna il loop:
```bash
codex exec -m gpt-5.5 \
  -C .worktrees/cri-spike --skip-git-repo-check \
  --dangerously-bypass-approvals-and-sandbox \
  "Sei CRI-codex, un critico indipendente nel bridge cab-bridge. Loop:
   (1) cab-bridge receive --any --max-deadline=1800 --emit=content  → aspetta SENZA interrompere; ottieni il task di VAL.
   (2) Esegui l'analisi che il task chiede, esplorando il repo coi tuoi strumenti (read, grep, test). Sii critico e specifico, cita file:riga.
   (3) Consegna il verdetto: cab-bridge ask --to=VAL --type=response --content=\"<il tuo parere>\"  (usa --file se è lungo: cap 64KB).
   (4) Torna al passo (1).
   Non inventare mai un id; non scrivere codice in questo spike."
```

**VAL** (io) pingo un task reale, es.:
`cab-bridge ask --to=CRI-codex --type=query --content="Dammi un parere contrario sul piano F-51 (quarantena anti log-flood) descritto in ROADMAP.md: dove può rompersi, cosa manca, cosa faresti diversamente. Cita file:riga."`

Poi `cab-bridge receive --any` (o `--msg-id`) per ricevere il verdetto.

**Cosa misuro/valuto**:
- Latenza task (accettabile per un critico asincrono).
- Il CRI **esplora davvero** (usa i suoi tool) o si limita al prompt?
- Qualità + **indipendenza** del parere: cattura cose che io (Claude) mancherei? (valore LL-2).
- La consegna via bridge regge (no drop, no id allucinati).

---

## Scope guard (non fare ora)

- NON scrivere il ruolo `cri` formale in `role.go` (backlog).
- NON scrivere l'`AGENTS.md` completo per Codex — è il **deliverable POST-spike** se Fase 0+1
  passano (la skill insegnerà protocollo bridge **E** come girare il verificatore per l'uso-duello,
  nota esperto). Per lo spike basta il prompt proto-skill inline.
- NON toccare il caso MCP/duello (separato, non compete a questa sessione).

## Findings → ROADMAP (VAL)

Ogni finding (fail-mode Fase 0, bug bridge cross-vendor, attrito) → ROADMAP sotto F-56. Se Codex
gestisce male il blocking-wait → è il risultato più importante dello spike, va documentato comunque.

---

## ESITO (1 giu 2026) — SUCCESSO, oltre l'obiettivo

Eseguito con **Codex CLI 0.136.0 in modalità interattiva (TUI)**, modello `gpt-5.5`, CRI-spike
(`815c0fc4`) in worktree `.worktrees/cri-spike`, VAL-spike (`823e6420`, Claude) in checkout main.

**Verde su tutto:**
- **Onboarding cross-vendor (LL-14)**: con le sole skill globali `~/.codex/skills/{critico,cab-bridge-awareness}`,
  Codex ha fatto `bootstrap --role=esc --no-listen`, visto il peer e si è messo in `receive --any --emit=json`
  **da solo**, capendo il modello d'esecuzione ("lascio il receive correre fino al ritorno naturale").
- **`critico` come profilo**: dopo il fix (principi, non comandi), `$critico` si dichiara pronto e
  **si ferma**, non avvia analisi spontanee.
- **Wake + consegna bidirezionale**: VAL→CRI (task) e CRI→VAL (verdetto) con `in-reply-to` corretti,
  più 2 ping-pong. F-41 cross-worktree + pairing Claude↔Codex confermati live.
- **Valore reale del critico (LL-2)**: 2 catch veri che VAL+esperto avevano mancato — (1) un fence
  Markdown spaiato nelle skill; (2) un **difetto di classificazione errno nel piano F-51** (ENOENT-race
  trattata come anomalia → falsi allarmi; con la simmetria già presente in `scanForReply:152-161`).
  Entrambi verificati sul codice. Il piano F-51 è stato raffinato di conseguenza.

**Finding operativi cross-vendor (→ F-56):**
1. `--to` accetta solo session-id, non agent-name → un by-name sarebbe più AI-friendly (LL-13).
2. `receive --any --emit=content` su timeout = stdout vuoto, ambiguo per un agente non-Claude → la
   skill impone `--emit=json` (`{"status":"timeout"}` distinguibile). Già applicato.
3. Le skill Codex **non** si auto-attivano per pura rilevanza: servono path indicizzato + **menzione
   esplicita** o richiesta semanticamente forte → il prompt iniziale di un CRI-Codex deve nominarle.
4. **Modello d'esecuzione Codex (il dato chiave)**: in TUI un comando bloccante in background **non
   sopravvive alla fine del turno** del modello (il `receive` rilanciato muore quando Codex chiude il
   turno). Il **loop foreground** invece regge e consuma i messaggi. Latenza: cold-start ~4-5 min,
   warm (loop attivo) ~secondi. Conseguenza: per un CRI passivo "sempre-vivo" serve `receive` in
   foreground ri-loopato O re-ingaggio manuale (per un critico on-demand quest'ultimo va benissimo).
5. YAML: una `description` con `: ` (colon-space) non quotata rompe il frontmatter → sempre quotare.

**Raffinamenti applicati alle skill Codex post-verdetto**: fence fixato; `--strict-reply` nel comando
di risposta (anti-id-allucinato); nota runtime "processo vivo da pollare, non appeso".

**Conclusione**: il claim "vendor-agnostic" è dimostrato sul campo, e un **CRI-Codex on-demand via
bridge** è una capability reale e a basso costo (2 skill + il bridge esistente, zero modifiche al
binario). Setup riusabile: worktree dedicato + skill globali + re-ingaggio manuale per task.
