# MISSION BRIEF — ESPLORAZIONE FORK BRIDGE PLUGIN

**Da**: VALUTATORE (Opus 4.7, claude-bridge project)
**A**: ESECUTORE (sessione fresh, claude-bridge project)
**Data**: 2026-05-24
**Tipo**: ESPLORATIVA — produrre PIANO, NON codice
**Output atteso**: `PLAN.md` strutturato che io possa valutare

---

## 1. Context & obiettivo

Progetto nuovo. Stiamo per forkare il plugin `PatilShreyas/claude-code-session-bridge` v0.1.1 — utility bash+jq che permette a sessioni Claude Code in finestre VS Code separate di comunicare via file JSON in `~/.claude/session-bridge/sessions/<id>/{inbox,outbox}/`.

**Use case Alan**: workflow triadico VAL (planner/orchestrator) ↔ ESC (executor context-fresh) cross-VS Code, con scaling occasionale a 1 VAL + N ESC.

**Pain reale empiricamente verificato** in 15+ sub-sprint cross-project (chatterence-bi-template, ac-agents, p1-wp-translator) negli ultimi giorni: il plugin ha 5-6 bug critici + 15+ frizioni UX. Tre VAL diversi hanno scritto report convergenti che trovi in `briefing/`:

- `SESSION_BRIEF_FORK_DESIGN_REPORT.md` — visione VAL #1, propone deep refactor con daemon Python + SQLite
- `bridge-fork-proposal-2026-05-24.md` — visione VAL #2, propone bash-only con quick wins + roadmap a fasi (P0→P3)
- `bridge-fork-improvement-report-2026-05-24.md` — visione VAL #3, propone MVP Tier 1 minimal additivo (~180 LOC, KISS)

**LEGGILI TUTTI E TRE prima di iniziare.** Sono input critici, non opzionali. Sono lunghi ma densi.

---

## 2. Bug critici convergenti (consenso 3/3 VAL)

Da validare empiricamente nel source upstream:

1. **Heartbeat dead in listen loop** → `bridge-listen.sh` non chiama `heartbeat.sh` durante polling → `lastHeartbeat` congelato → `list-peers` mostra peer attivi come `stale`
2. **`bridge-receive.sh` timeout secco** → response persa se ESC impiega più del timeout → recovery solo via git status empirico
3. **Multi-peer routing senza role** → manifest non ha `role` field → ESC può messaggiare altro ESC credendolo VAL (Alan-reported)
4. **Cleanup globale cross-project** → `cleanup.sh` distrugge sessioni di OGNI progetto con threshold stale hardcoded → evidence: chatterence-bi-template ha cancellato 2 sessioni ac-agents collaterali (24 mag 2026)
5. **`get-session-id.sh` fallback parent path** → nested cwd match wrong session (parent invece di subdir specifica)
6. **Session ID collision per project dir** → `register.sh` riusa `.claude/bridge-session` → 2 istanze stesso cwd = stesso ID

---

## 3. Decisioni architetturali aperte (TU decidi e motivi)

I 3 VAL divergono su queste. Tu devi proporre UNA scelta motivata, NON sintetizzare con "dipende":

### 3.1 Strategia tecnologica
- **Opzione A**: bash + jq drop-in fix (KISS, zero dep new, ~2-3 giorni MVP) — visione VAL #2, #3
- **Opzione B**: rewrite Python con daemon persistente + SQLite + Unix socket (robusto, ~1-2 settimane MVP) — visione VAL #1
- **Opzione C**: ibrido (bash CLI thin client + Python daemon opzionale opt-in)

### 3.2 Scope MVP
Definisci cosa entra in v0.2.0 (MVP fork) e cosa va deferred a v0.3/v1.0. Massimo 5-7 features in MVP. KISS.

### 3.3 Naming
3 proposte sul tavolo: `session-bus`, `ac-bridge`, `claude-bridge-pro`. Argomenta UNA scelta o proponi alternativa.

### 3.4 Backward compatibility
- Drop-in replacement del plugin Patil (same file layout, same slash commands)?
- O fork con migration tool e namespace separato?

### 3.5 Distribuzione
Plugin Claude Code marketplace? Custom plugin install? Standalone repo con install script?

---

## 4. Vincoli NON negoziabili

- **KISS**: la filosofia di Alan è semplice ma robusto. Se proponi daemon Python, motiva FORTE il ROI vs complessità.
- **EU compliance / GDPR**: bridge gira local-only single-user, ma se aggiungi telemetry/logging considera privacy (anche locale). Se proponi encryption, motiva use case reale.
- **Security**: nessuna escalation privilegi, no eseguibili non firmati, no fetch network senza opt-in esplicito.
- **Manutenibilità**: file <600 righe, modular, testabile. Niente magia.
- **Zero workaround**: se rifletti tradeoff, mostra l'opzione clean anche se più costosa.
- **No fallbacks impliciti**: l'app è AllOrNothing, ogni fallback va dichiarato esplicito e motivato.
- **No dati hardcoded**: config via file JSON o env var, NON valori magici nei script.

---

## 5. Cosa NON fare in questa fase

- NON scrivere codice (no script bash, no .py, no patch)
- NON committare
- NON clonare/scaricare il repo upstream nel working dir (al massimo leggi via WebFetch GitHub raw o usa subagent `github-hunter`)
- NON installare dipendenze
- NON proporre piano con effort sotto i 2-3 giorni "perché è banale" — i 3 VAL hanno già fatto questo lavoro, tu devi consolidare
- NON ignorare le divergenze tra VAL — affrontale esplicitamente

---

## 6. Deliverable: `PLAN.md`

Struttura attesa (sezioni minime, puoi aggiungere):

```
# PLAN.md — Fork claude-bridge

## 1. Executive summary (max 200 parole)
## 2. Validazione bug upstream
   - Per ogni bug critico: confermato/refutato + evidence linea-codice
## 3. Decisioni architetturali (5 sezioni 3.1-3.5)
   - Per ogni decisione: opzione scelta + motivazione + tradeoff scartati
## 4. Architettura proposta
   - Diagramma high-level (ASCII OK)
   - Repo structure
   - Schema manifest v2 + message v2
   - Componenti core + responsabilità
## 5. Roadmap milestone
   - v0.2.0 MVP: scope esatto + effort
   - v0.3.0: scope + effort
   - v1.0.0 target
## 6. Strategy backward compat
   - Migration path da Patil v0.1.1
## 7. Testing strategy
   - Unit + integration + regression sui bug noti
## 8. Risk register
   - Top 5 rischi + mitigation
## 9. Open questions per VAL
   - Cose che ti servono da Alan/VAL prima di partire MVP
## 10. Next step concreto
   - Cosa fa ESC al primo PHASE 2 brief (quale file/script per primo)
```

---

## 7. Tools consigliati per esplorazione

- **`github-hunter` subagent** → fetch `PatilShreyas/claude-code-session-bridge` README + script paths + commit history per validare bug nel codice
- **`deep-searcher` subagent** → ricerca pattern IPC bridge alternatives (Unix sockets vs filesystem polling vs Redis pub/sub) per informare scelta 3.1
- **`kb-search-assistant` subagent** → cerca best practice bash multi-process IPC, JSON schema validation, plugin Claude Code structure
- **`security-sentinel` subagent** → audit hypothetical fork architecture per gap sicurezza (encryption-at-rest, ACL, ecc.)

Usali AGGRESSIVAMENTE in parallelo. NON limitarti a leggere i 3 briefing — i VAL si possono sbagliare, valida.

---

## 8. Disciplina senior

- Se trovi un bug che i 3 VAL hanno mancato → segnalalo esplicito in PLAN.md sezione "Bug aggiuntivi"
- Se trovi che un bug "critico" segnalato dai VAL in realtà non esiste (es. è stato fixato in v0.1.2 nel frattempo) → refutalo con evidence
- Se i 3 VAL convergono ma TU pensi siano sbagliati → argomenta dissenso motivato
- Brutally honest: se 2-3 giorni MVP è irrealistico, dillo subito
- Future Alan test: ogni decisione deve essere leggibile tra 3 mesi senza contesto

---

## 9. Done criteria

`PLAN.md` esiste, è strutturato come da §6, copre tutti i 6 bug + tutte le 5 decisioni architetturali + roadmap concreta. Quando finito, **NON committare**, segnala in chat che il piano è pronto per review VAL.

---

**Fine brief PHASE 1.** Pronto a ricevere PHASE 2 (clarification) se serve, altrimenti procedi.

— VAL
