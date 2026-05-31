# Handoff sessione 2026-05-30 — cli-agents-bridge

> **AGGIORNAMENTO 2026-05-31 — Sprint v0.3 1° giro CHIUSO + MERGED su `main`.** Le 3 coppie di test (VPS/gioco/BI) hanno consegnato i feedback → backlog v0.3 consolidato (F-13→F-32, 8 voci). Primo giro implementato via cab-bridge (canale `teams/cab-dev`): F-30 `8612af3` (receive→archivia), F-24 `88d98a1` (--wait-one exit0+payload), F-26 `4761bfe` (--until-deadline). Gate VAL verde 10/10 no-cached, binario rigenerato `0.2.4-5-g4761bfe` in PATH (fix attivi), CHANGELOG [Unreleased]/ROADMAP/skill aggiornati. **NON taggato** → tag v0.3.0 dopo il testing reale nei progetti. Restano: F-22 (follow-up), F-23/F-27 (portanti), F-25/F-28/F-29/F-31/F-32/F-11. Dettaglio in `ROADMAP.md` + memory.

> Stato + pending pre-compact. Per riprendere: leggi questo + `docs/v0.2.2-plan.md` + la skill
> `cab-bridge-awareness` (findings di metodo). Il principio guida resta **F-16: verifica il
> ground-truth su disco (git/inbox/peers), MAI il resoconto**.

## SHIPPED oggi (tutto su `main` + tag + GitHub Release pubbliche, MIT)
- **v0.2.2** — F-12 osservabilità/ACK (auto-ack su listen, inboxCount/lastConsumedMsgId) · F-10 `listen --wait-one` (wake immediato, drain-once lossless) · F-5 team/`whoami` · F-9 outbox + `cab sent`.
- **v0.2.3** — GoReleaser: binari prebuilt multi-OS (darwin/linux × amd64/arm64) sulle Release · skill pubblica `bridge-workflow` (role-agnostic, nel plugin) · README role-agnostic · version-injection-da-tag.
- **v0.2.4** — **F-17 auto-isolamento per project-root**: `register` deriva `scope` dalla root `.git` (worktree-aware, `$HOME` escluso, fallback cwd); `peers` filtra di default per lo scope del cwd; `--all-scopes` per globale + hidden-count su stderr; `whoami` mostra scope.
- Workflow `release.yml` SUCCESS su ogni tag; binari pubblicati. `main` HEAD ~ `edccd08` (+ commit successivi).

## Binario LOCALE in PATH = v0.2.3 (deciso: NON rigenerato a v0.2.4)
- `~/.local/bin/cab-bridge` è **v0.2.3** → **niente auto-scope F-17 in locale** (ma ha già `--wait-one` + auto-ack, di v0.2.2).
- Deciso: resta v0.2.3 per il GIRO CORRENTE (coppie già avviate). Rigenerare a v0.2.4 (`make install-dev` da `main`) al **prossimo giro pulito con sessioni fresche** — l'auto-scope si popola al `register`, quindi rigenerare a metà flusso richiederebbe ri-registrare le coppie (mismatch legacy/scope). "install ≠ hot-reload".

## Findings di metodo OGGI — tutti catturati nella skill `cab-bridge-awareness`
- **F-13** cleanup al lifecycle finestre (hook on-close NON affidabile → riconciliazione on-start).
- **F-14** executor-sordo durante il lavoro (non legge inbox mentre implementa → uno STOP non arriva).
- **F-15** ACK manuale sciatto → falso allarme orchestrator (auto-ack minimale > riassunto).
- **F-16 (MADRE)** verifica il ground-truth (git/disco), MAI il resoconto dell'executor.
- **F-17** → SHIPPED v0.2.4 (auto-scope).
- **F-18** invocazione: usa `cab-bridge` in PATH (MAI `~/go/bin`), flag esatti (`--agent-name`/`--session-id`, **non** `--name`).
- **F-19** il VAL aspetta sull'**INBOX** (receive / until-loop su `inbox/*.json`), NON con un poll su `peers` (`peers` = bootstrap one-shot per scoprire l'id ESC).
- **F-20** `CAB_DATA_DIR` (canale / isolamento FISICO) **≠** `--team`/`teamId` (filtro LOGICO): non sovrapporli; con data-dir-per-coppia `--team` è ridondante; usarlo incoerente (VAL sì / ESC no) nasconde il peer a `peers --team`.

## Test in corso (3 coppie LOCALI in parallelo — stress multi-coppia, v0.2.3)
- **ac-agents** (sviluppo del VPS in locale; il VPS NON è deployato): `CAB_DATA_DIR=…/teams/ac-agents`.
- **chatterence-bi**: `CAB_DATA_DIR=…/teams/chatterence-bi` · VAL-bi `c4fac477` (team=chatterence-bi) · ESC-bi `4029ef1e` (team VUOTO — canale OK perché stesso data dir, F-20). VAL-bi sbloccato (leggeva peers invece dell'inbox, F-19).
- (terza coppia avviata.)
- **Verdetto**: il bridge REGGE; le frizioni sono tutte di METODO (path/dove-aspetta/isolamento), non di affidabilità. Catturate nella skill.
- **Caveat**: le coppie GIÀ avviate hanno la skill caricata PRIMA dei fix F-18/19/20 → girare le correzioni a mano; le prossime (skill ricaricata) partono pulite.

## Pending / azioni aperte
- **Dossier long-run autonomo** per l'Architetto del VPS: `/tmp/dossier-longrun-autonomo-ac-agents.md` — Alan deve passarglielo (integra nel briefing della coppia VPS).
- **Naming**: agents del VPS DEPLOYATO → **JOE/WAL**; agents locali (sviluppo) → VAL/ESC.
- **Recap findings F-13→F-20** richiesto da Alan "quando ci fermiamo".
- **Resilienza long-run / rate-limit**: retry-backoff lo fa l'harness (`CLAUDE_CODE_MAX_RETRIES`, esponenziale), NON il bridge (resiliente via stato-su-disco). NO ping applicativo nel bridge (anti-pattern — non riattiva un modello fermo). Documentato in skill (sezione "Long-run autonomo") + nel dossier.

## Backlog v0.3 — consolidato dal dogfooding VPS (2026-05-30)
Feedback strutturato coppia ac-agents (VAL-vps↔ESC-vps, binario v0.2.3). Tabella completa + 2 catch in **ROADMAP §"Dogfooding VPS"**. Sintesi:
- **F-21** disciplina ACK — **FATTO** (fix-skill): l'auto-ack `{query}` esiste già ed è ON da v0.2.2; ESC lo duplicava a mano → doppione. Skill aggiornata (sezione "L'auto-ack è NATIVO").
- **F-30** **TOP backlog — `receive`/`scanForReply` consuma DISTRUTTIVAMENTE** (`receive.go:140` `os.Remove`) invece di `MoveToProcessed` come `listen`/`PollInbox` → in bg con output perso la late-reply sparisce dall'inbox e resta solo nell'outbox del mittente (pain#1 security review VAL-bi). **NON è bug di consegna** (`send.go:78` inbox PRIMA). Fix radice **P1 Basso**: `scanForReply`→`MoveToProcessed` (esiste già) → recupero da `processed/` propria. Primo commit codice v0.3.
- **F-24** exit 124 in `--wait-one` ≠ failure → **exit 0 + payload** `{"status":"timeout"}` — **P1 Basso, TRIPLA convergenza** (ESC-vps#1 ⊕ VAL-flusso#1 ⊕ ESC-flusso#2). Trade-off: 124 serve all'until-loop bash → flag opt-in o default per bg.
- **F-23** stato task strutturato role-aware (enum + `orchestrating`=heartbeat-exempt) — **P1 Medio, 1ª feature portante** (quadrupla: VAL-vps#3+#4 ⊕ ESC-vps#5 ⊕ VAL-flusso#3). Sotto-fix quick-win: heartbeat passivo da qualsiasi comando.
- **F-27** reconnect idempotente by `(agent-name,team,projectPath)` post-compact — **P1 Medio, 2ª feature portante** (VAL-flusso#2, consolida F-3). Il compact è frequente per long-running.
- **F-26** default finestra `listen` per-ruolo (ESC standby più lungo in bg) — P1 Basso (ESC-vps#3 ⊕ ESC-flusso#4).
- **F-22** sottocomando `inbox`: `--list` (ispeziona senza consumare, ESC-flusso#3, elimina poll `ls` fragile) + `--tidy` (≤ lastConsumedMsgId, VAL-vps#2) — P2 Basso.
- **F-25** `ErrTargetSessionGone` semantico (vs raw `open manifest.json`, `send.go:37`) — P2 Basso (ESC-vps#2).
- **F-28** `cleanup --notify-peers` (avvisa il team prima di staccarsi, simmetrico a F-25) — P2 Basso (VAL-flusso#4).
- **F-29** unificare/chiarire isolamento CAB_DATA_DIR (fisico) vs --team (logico) — P2 (ESC-flusso#1). **MA F-17 v0.2.4 è già la risposta** → vedi AZIONE #0.
- **F-31** `listen --replace`/warning su listen orfano (doppio-backgrounding) — P2 Basso (ESC-bi#3).
- **F-32** visibilità team a livello processo (`cab-bridge[team] listen` o lock-file per-team) per kill orfani — P3 Medio (ESC-bi#4).
- **F-11** globalSweep PID-aware — P3, già in roadmap (confermato dal campo VAL-vps#5).
- **F-33 sospetto VERIFICATO INFONDATO** (F-16): `--wait-one`/`DrainInboxOnce` archivia at-most-once (`drain.go:47,83`, single-source `consumeInboxEntry`); la "re-emissione" di ESC-bi = re-invio del mittente per receipt mancante (→ F-23). Corollario: asimmetria `os.Remove` SOLO in `scanForReply` (F-30 ben circoscritto).
- Già deferred (invariati): SC-3 ownership wiring, bump action GitHub a Node 24, F-20-prodotto (warn teamId misto in data dir condiviso) = parte di F-29.
- **AZIONE #0 (zero codice, max impatto): deployare binario v0.2.4 in locale** (`make install-dev`, al prossimo giro con sessioni fresche). Chiude F-29 + tutto l'attrito CAB_DATA_DIR/--team: è il 3° agente indipendente a inciampare in qualcosa **già risolto da F-17**.
- **3 catch verificati sul codice (F-16 su VAL)**: (a) auto-ack non-da-fare, esiste già (`send.go:102`, ON, allow-list `{query}`); (b) finestra 540s tarata per FOREGROUND — in bg 1800s OK (mea culpa VAL: `API_TIMEOUT_MS` è timeout API-modello, non shell); (c) `--wait-one` esce 124 al timeout (`listen.go:119-121`) → un exit≠0 dedicato sarebbe comunque "failed" per l'harness, solo exit 0+payload risolve.
- **Tabella completa + priorità + bilancio**: ROADMAP §"Dogfooding VPS" + §"Aggiornamento coppia gioco".
- **Recap findings F-13→F-32** richiesto da Alan "quando ci fermiamo" (3 coppie chiuse: VPS, gioco, BI — 8 voci-agente).

## Workflow VAL/ESC (promemoria)
VAL=orchestratore (NON in listen; bootstrap peers one-shot → ask → aspetta INBOX) · ESC=executor (in `listen --wait-one`, ascolto attivo). Boundary: ESC scrive codice, VAL scrive docs. Gate VAL: `go test -race -count=1 ./...` indipendente, mai fidarsi del "verde dichiarato" (LL-9/11).
