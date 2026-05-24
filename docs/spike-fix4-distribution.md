# Spike FIX-4 — Distribution path verification (Day 0)

> **Sprint**: 0
> **Data setup**: 2026-05-24
> **Time-box**: 4h hard
> **Goal**: validare empiricamente quale distribution path è funzionante per `cli-agents-bridge` v0.2.0 (self-marketplace primary vs pure-PATH fallback) prima di scrivere codice produzione.
> **Decision tree**: Esito A → §3.5 primary = self-marketplace. Esito B → §3.5 fallback = pure-PATH + +1 giorno roadmap.

---

## 1. Setup eseguito da ESC

Spike repo creato in `/tmp/cab-spike/`:

```
/tmp/cab-spike/
├── .claude-plugin/
│   ├── plugin.json              # manifest schema 2026 minimal
│   └── marketplace.json         # self-marketplace registration
├── cmd/
│   └── main.go                  # binary placeholder ~30 LOC Go
├── commands/
│   └── cab-spike.md             # slash command markdown
├── bin/
│   └── cab-spike                # binary compilato (darwin-arm64, ~2.5MB)
└── go.mod                       # module github.com/myAIPlugins/cab-spike
```

Binary verificato in standalone:
- `./bin/cab-spike --version` → `cab-spike spike-0.0.1` ✓
- `./bin/cab-spike` stampa env vars + checklist + path info ✓

---

## 2. Protocollo test (Alan-driven, ~20 min)

### Test 2.1 — Dev mode `--plugin-dir`

In una sessione Claude Code separata:

```bash
claude --plugin-dir /tmp/cab-spike
```

Una volta dentro la sessione:

```
> /cab-spike
```

**Cosa osservare nell'output del binary**:
- `argv` mostra come Claude Code invoca il binary (con/senza args)
- `CLAUDE_PLUGIN_DATA` env var: deve mostrare un path tipo `~/.claude/plugins/data/cab-spike/` se popolato
- `CLAUDE_PLUGIN_ROOT` env var: deve mostrare path tipo `/tmp/cab-spike` o cache path
- `Self binary` deve mostrare path del binary (verifica se è simlink, copia, o ref diretto)

**Test edit live**:
- Modifica `/tmp/cab-spike/cmd/main.go` (aggiungi una riga `fmt.Println("EDITED")`)
- `go build -o /tmp/cab-spike/bin/cab-spike ./cmd` (recompile)
- Nella sessione Claude: `/reload-plugins`
- Riinvocare `/cab-spike` — vedere output con "EDITED"?

### Test 2.2 — Marketplace mode (local repo)

In una sessione Claude Code separata (senza `--plugin-dir`):

```
> /plugin marketplace add /tmp/cab-spike
> /plugin install cab-spike@cab-spike-marketplace
```

Poi:

```
> /cab-spike
```

**Cosa osservare**:
- Comando `/plugin marketplace add` accetta path locale (oppure richiede repo GitHub remoto)?
- Plugin viene copiato in `~/.claude/plugins/cache/cab-spike-marketplace/cab-spike/0.0.1/`?
- Binary `cab-spike` è invocabile come comando shell (PATH-injected) dopo install?
- Env vars `CLAUDE_PLUGIN_DATA` + `CLAUDE_PLUGIN_ROOT` valorizzati?
- `which cab-spike` (in shell esterna a Claude Code) ritorna path?

### Test 2.3 — Shell verification (fuori Claude Code)

```bash
# Verifica installazione filesystem (post marketplace install)
ls -la ~/.claude/plugins/cache/ 2>/dev/null
find ~/.claude/plugins/ -name "cab-spike" 2>/dev/null
ls -la ~/.claude/plugins/data/cab-spike/ 2>/dev/null

# Verifica se binary è in PATH ereditato da Claude Code subprocess
which cab-spike 2>/dev/null || echo "NOT in PATH"
```

---

## 3. Esito test (compilato 2026-05-24 da Alan + ESC)

### 3.1 Test 2.1 — Dev mode `--plugin-dir` (ESEGUITO)

- [x] `/cab-spike:cab-spike` slash command si registra
- [x] Binary `cab-spike` invocato correttamente (banner ricevuto + output binary)
- [x] **PATH auto-injection confermata**: PATH contiene `/tmp/cab-spike/bin` (verificato via `env` nella sessione). Stesso meccanismo per altri plugin installati: `~/.claude/plugins/cache/<marketplace>/<plugin>/<version>/bin` viene aggiunto automaticamente. Conferma claim PLAN §4.6.
- [ ] **`CLAUDE_PLUGIN_DATA` env var: EMPTY** ❌ — confermato sia dall'output binary sia da `env | grep CLAUDE` nella shell sessione. Nessuna var d'ambiente `CLAUDE_PLUGIN_*` esposta al subprocess.
- [ ] **`CLAUDE_PLUGIN_ROOT` env var: EMPTY** ❌ — stesso esito.
- [?] Edit live + `/reload-plugins`: **non testato** (deferred — sapere già che `--plugin-dir` + recompile manuale funziona nella prassi, criticità bassa)

**Artefatto rilevato (non bug)**: il binary stampa `Self binary: /Users/alan/develop/cli-agents-bridge/cab-spike` invece del path reale `/tmp/cab-spike/bin/cab-spike`. Causa: `filepath.Abs(os.Args[0])` con `os.Args[0]="cab-spike"` (string nuda dal PATH lookup) risolve contro CWD della sessione. Non indica self-detection rotta del PATH — il binary è correttamente in `/tmp/cab-spike/bin/cab-spike` e viene invocato.

### 3.2 Test 2.2 — Marketplace mode (local repo) (ESEGUITO)

**Iterazione 1 (FAIL)**: con layout originale (marketplace.json + plugin.json **nella stessa** `.claude-plugin/` dir, `source: "."`) → errore `Failed to install: This plugin uses a source type your Claude Code version does not support. Update Claude Code and try again.` (Claude Code 2.1.150).

**Iterazione 2 (PASS post restructure Patil-style)** — vedi §4.1 Layout finding:

- [x] `/plugin marketplace add /tmp/cab-spike` accetta path locale → **"Successfully added marketplace: cab-spike-marketplace"**
- [x] `/plugin install cab-spike@cab-spike-marketplace` completa → **"✓ Installed cab-spike. Run /reload-plugins to apply."** + scope prompt mostrato (scelto user scope = stesso degli altri plugin baseline)
- [x] Plugin copiato in `~/.claude/plugins/cache/cab-spike-marketplace/cab-spike/0.0.1/` con struttura completa (`.claude-plugin/plugin.json`, `bin/cab-spike`, `commands/cab-spike.md`)
- [x] `installed_plugins.json` aggiornato con entry `cab-spike@cab-spike-marketplace` (scope user, installPath cache canonical)
- [x] Binary auto-added a PATH dal plugin runtime (argv = `[cab-spike]` stringa nuda = lookup PATH riuscito)
- [x] `/cab-spike:cab-spike` invoca correttamente il binary dalla cache copy (namespace `plugin-name:command-name`)
- [ ] **Env vars `CLAUDE_PLUGIN_DATA` / `CLAUDE_PLUGIN_ROOT` EMPTY anche in marketplace mode** ❌ — confermata assenza già osservata in Test 2.1 dev mode. **Nice-to-have non requirement**: l'architettura PLAN §3.4 usa namespace separato `~/.claude/cli-agents-bridge/` derivato da `$HOME`, indipendente da env var Claude Code.

**Path canonical install confermato**: `~/.claude/plugins/cache/<marketplace-name>/<plugin-name>/<version>/`

### 3.3 Test 2.3 — Shell verification (COMPLETATO post Test 2.2)

- [x] `installed_plugins.json` pre-test: `cab-spike` **NON** presente (baseline corretta)
- [x] `installed_plugins.json` post-install: entry `cab-spike@cab-spike-marketplace` presente con `scope=user`, `installPath=/Users/alan/.claude/plugins/cache/cab-spike-marketplace/cab-spike/0.0.1`, `version=0.0.1`
- [x] Cache directory popolata: `~/.claude/plugins/cache/cab-spike-marketplace/cab-spike/0.0.1/{bin,commands,.claude-plugin}/` con tutti i file deployati
- [x] Binary in PATH del subprocess plugin: confermato indirettamente via argv = `[cab-spike]` nuda (string non-absolute = PATH lookup), il binary REALE è in cache install path

---

## 4. Decisione finale

**Verdict**: **Esito A definitivo** ✅

Self-marketplace distribution path **funziona end-to-end** in Claude Code 2.1.150 marketplace mode (non solo `--plugin-dir` dev mode), confermato da Test 2.1 + Test 2.2 (iterazione 2 post restructure Patil-style).

**Criteri PLAN §3.5 raggiunti**:
- [x] Marketplace add accetta path locale (no GitHub URL richiesto per dev/test)
- [x] Plugin install completa con scope user
- [x] Binary deployato in path cache canonical: `~/.claude/plugins/cache/<marketplace>/<plugin>/<version>/bin/`
- [x] PATH auto-injection del subprocess funzionante (argv string nuda = lookup PATH)
- [x] Slash command registrato come `/<plugin-name>:<command-name>` namespace
- [x] `/reload-plugins` rilegge senza restart

**Caveat non bloccanti** (documentati per Future Alan):
- ❌ `${CLAUDE_PLUGIN_DATA}` env var **EMPTY** anche in marketplace mode 2.1.150 — non requirement (PLAN §4.6 era hypothesis "DA VERIFICARE", ora falsificata). Architettura PLAN §3.4 usa `$HOME`-based namespace `~/.claude/cli-agents-bridge/`, zero impact.
- ❌ `${CLAUDE_PLUGIN_ROOT}` env var **EMPTY** — stesso esito, nice-to-have.
- ⚠️ Self-detection binary via `os.Args[0]` non risolve al cache path: `filepath.Abs` lo risolve contro CWD della sessione = artefatto display, non bug. Sprint 1 TODO: usare `os.Executable()` se mai serve self-detection (probabilmente mai — namespace è derived from `$HOME` config).

## 4.1 Layout finding — Patil-style mandatory

**Discovery critica del spike**: Claude Code 2.1.150 NON supporta `source: "."` (marketplace + plugin nella stessa `.claude-plugin/` dir). Errore: "This plugin uses a source type your Claude Code version does not support."

**Layout supportato** (verificato funzionante, copiato da pattern Patil session-bridge upstream):

```
<marketplace-repo>/
├── .claude-plugin/
│   └── marketplace.json              # plugins[].source = "./plugins/<plugin-name>"
└── plugins/
    └── <plugin-name>/                 # SUBDIR obbligatoria per ogni plugin
        ├── .claude-plugin/
        │   └── plugin.json
        ├── commands/
        │   └── <name>.md
        └── bin/
            └── <binary>
```

**Schema marketplace.json minimal verificato** (Claude Code 2.1.150):
- `name` (required)
- `description` (raccomandato)
- `owner` object con `name` + `email`
- `plugins[]` array con per ciascun entry:
  - `name` (required, deve matchare plugin.json `name`)
  - `version` (raccomandato, matcha plugin.json)
  - `description`
  - `author` object
  - `source` string relative path a subdir plugin (es. `"./plugins/cab-spike"`) — **string `.` NON supportata**
  - `category` (opzionale)
  - `keywords` array (opzionale)

## 4.2 Implicazioni layout production cli-agents-bridge

Il repo attuale `/Users/alan/develop/cli-agents-bridge/` ha layout flat con `.claude-plugin/{plugin,marketplace}.json` in root. Questo layout **NON è installabile via `/plugin marketplace add`** (riproduce il bug Test 2.2 iterazione 1).

**Refactor proposto post-Sprint 0** (da ratificare VAL prima di applicare):

```
cli-agents-bridge/                       ← Go module root
├── .claude-plugin/
│   └── marketplace.json                 ← source: "./plugins/cli-agents-bridge"
├── plugins/
│   └── cli-agents-bridge/
│       ├── .claude-plugin/
│       │   └── plugin.json
│       ├── commands/
│       │   └── cab*.md
│       └── bin/
│           └── cab-bridge               ← copy da ../../bin/ via Makefile target
├── cmd/cab-bridge/main.go               ← Go module main (root)
├── internal/{config,session,...}/       ← Go packages (root)
├── bin/cab-bridge                       ← build output (gitignored)
├── go.mod                               ← Go module root
└── Makefile                             ← target `install-plugin` copia bin/ in plugins/cli-agents-bridge/bin/
```

**Rationale**:
- Go module a root rispetta convention Go standard (`go.mod` top-level)
- Plugin subdir replica Patil-pattern verified empiricamente
- Build artifact `bin/cab-bridge` resta gitignored; `plugins/cli-agents-bridge/bin/cab-bridge` riceve binary via Makefile target `install-plugin` (cp o symlink — preferenza cp per evitare path resolution issue, da decidere)
- Marketplace.json + plugin.json restano in subdir separate, no source `.` ambiguity

**Effort refactor**: stimato 1-2h (Sprint 1 day 1 task pre-bug fix). Modifica solo `.claude-plugin/{plugin,marketplace}.json` location + nuovo Makefile target + aggiornamento `commands/` location nel manifest. Zero impatto su `cmd/`, `internal/`, test, CI.

**Decisione**: refactor lasciato fuori Sprint 0 commit (Sprint 0 = security baseline + spike verification, non layout refactor). Il commit Sprint 0 documenta il finding nel CHANGELOG/PLAN aggiornamenti, l'apply effettivo è Sprint 1 day 1.

---

## 5. Implicazioni per Sprint 0 step 10 (APPLICATE)

### Esito A* applicato
- `.claude-plugin/plugin.json` come da §4.6 PLAN (schema 2026 standard) — **scritto**
- `.claude-plugin/marketplace.json` self-marketplace con `source: "."` (path repo) — **scritto**
- `bin/cab-bridge` auto-PATH **confermato** ✓ (Test 2.1)
- `${CLAUDE_PLUGIN_DATA}` **NON disponibile** ✗ → state path = `~/.claude/cli-agents-bridge/` hardcoded (già in `internal/config/config.go` `DefaultConfig()`, zero rework)

### Se Esito B
- `.claude-plugin/` directory NON necessaria
- Distribuzione via GitHub Release con binary pre-built cross-OS
- Install instructions in README: `curl -L <url> -o ~/.local/bin/cab-bridge && chmod +x`
- Slash commands minimal in `~/.claude/commands/cab*.md` che invocano binary in PATH
- State path: hardcoded a `~/.claude/cli-agents-bridge/` (no `${CLAUDE_PLUGIN_DATA}` available)

---

## 6. Riferimenti

- PLAN.md v3 §3.5 — Distribuzione decision
- PLAN.md v3 §12 — Sprint 0 brief
- github-hunter agent verification (ESC v2 rework): docs verifica plugin system Claude Code 2026
