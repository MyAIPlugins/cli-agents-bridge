# 2026-09-03 — Windows nativo: primo giro sul PC (per il VAL sul Mac)

> Scritto dal VAL-win sul PC Windows, HEAD `a1f3fe1` = `origin/main`. Ogni riga dice se e'
> ESEGUITA `[E]` o DEDOTTA `[D]`. Il brief del port (`WINDOWS-PORT-BRIEF-2026-09-02`) resta la
> mappa dei lotti; questo file dice cosa e' successo sul PC e cosa cambia per te.

## Cosa e' verificato SUL PC — tutto `[E]`

    go build ./cmd/cab-bridge (windows/amd64, Go 1.27.1)    EXIT=0 → bin/cab-bridge.exe
    cab-bridge version                                       0.9.0-9-ga1f3fe1
    go vet ./... · gofmt -l .                                 puliti
    join --role=val                                          registra in %USERPROFILE%\.claude\cli-agents-bridge\
    whoami · peers                                           corretti, scope = project root
    worktree .worktrees/<esc> (gitdir: con slash avanti)     lo SCOPE COINCIDE col repo principale
    internal/security                                        VERDE alla prima esecuzione su Windows
    alive_windows.go su un PID inesistente                   dice MORTO (ramo ERROR_INVALID_PARAMETER)

Le `[D]` del brief §1.f e §2 su data dir, scope walk, puntatore del worktree e quoting (le
virgolette singole compaiono su ogni path Windows, perche' `\` e `:` non sono byte "safe")
sono confermate. Il caso ③ (PID riciclato) NON e' misurato: resta il lotto 2.

## La suite su Windows, prima esecuzione

    go test -count=1 -p 4 ./...          EXIT=1 · ok 7/12 package · 60 test rossi · cached 0   (gcc assente)
    go test -race -count=1 -p 4 ./...    EXIT=1 · ok 7/12 · 55 rossi · 0 DATA RACE · cached 0   (un'ora dopo:
                                         MinGW-w64 16.1.0 installato, Developer Mode ON → i 5 test sui
                                         symlink passano: il rifiuto dei symlink VALE su Windows)

Sei famiglie, **tutte negli strumenti di test, nessuna nel prodotto**:

    F1  helper con exec.Command("/bin/sh", ...)               29 test   → LookPath("sh"), skip nominato
    F2  harness di integrazione costruisce il binario senza .exe  20 scenari → una riga, PRIORITA' 1
    F3  asserzioni di mode 0700/0600 (Perm() = 0777/0666 qui)  ≈7       → Unix-only per natura, tag
    F4  fixture con symlink (privilegio assente senza Dev Mode)  7        → skip nominato; CI windows e' elevata
    F5  path POSIX nelle fixture (/repo/p1, a:b, ToSlash, tar)   ≈7       → fixture portabili
    F6  PID 1 come "vivo" (lock_test.go:39,143)                  2        → os.Getpid() / figlio terminato

Il lotto 1 e' in mano all'ESC-win sul branch `feat/windows-lotto1`: SOLO test, il gate ESC
include `git diff --stat -- ':!*_test.go'` vuoto.

## Aggiornamento 02:50 — lotto 1 completo sul PC, branch `feat/windows-lotto1` (a1f3fe1..e9686cf)

Sei commit, SOLO file di test, accettati dal VAL-win con gate indipendente: 72 dei 74 rossi
recuperati, 0 nuovi, SKIP 0, 0 data race, `-race` su Windows con MinGW. Restano rossi di
proposito `TestLegacyEndToEnd` (F-135, difetto di prodotto: `recipient.go:72` chiede "e' un
path assoluto?" in POSIX) e, a intermittenza, `TestStartHeartbeatOwned` (F-133: `os.Rename`
su Windows fallisce se un lettore tiene aperto il target, Go apre senza `FILE_SHARE_DELETE`).
Terzo difetto di prodotto: F-134, `join` nella cwd di una sessione VIVA di un altro agente la
sequestra. Design-gate fatto da un CRI Codex via bridge; decisioni in `.handover/` del PC.

**Cosa devi fare tu, se il branch arriva prima di me**: `go test -race -count=1 -p 4 ./...`
su darwin deve dare 12/12 come prima, 0 cached — su Unix ogni helper introdotto e' l'identita'
per costruzione (`binExeSuffix` vuoto, `hostPath` = Abs, `ToSlash` no-op, `assertPOSIXPerm`
e `mustSymlink` invariati), ma e' DEDOTTO finche' non lo esegui. `git diff --stat
a1f3fe1..e9686cf -- ':!*_test.go'` deve essere vuoto. Se entrambe reggono, il lotto 1 e'
mergiabile; se no, fermalo e scrivi qui il rosso.

## Cosa cambia per te sul Mac

- **In `main` non cambia niente** finche' `feat/windows-lotto1` non viene mergiato. Quando lo
  vedrai in PR o pushato: il tuo gate e' `go test -race -count=1 -p 4 ./...` su darwin, 12/12
  come prima, 0 cached — e il diff deve essere di soli `_test.go`. Se non lo e', il lotto ha
  sconfinato: fermalo.
- **Il gate sul PC e' ora quello vero**: gcc (MinGW-w64 16.1.0) c'e' dalle 01:20 del 3 set e
  `-race` gira; il primo giro senza race e' storia. La CI `windows-latest` (lotto 4) resta la
  seconda porta, come `ubuntu-latest` lo e' per il Mac.
- Se riprendi il port dal Mac, il binario Windows si cross-compila come nel lotto 0; quello
  che NON puoi fare dal Mac e' eseguire i test (F4 e F5 sono proprio la prova che serve la
  macchina).

## Regole nuove scoperte sul PC, valide per chiunque ci lavori

- Il binario in PATH su Windows e' una COPIA (`~/.local/bin/cab-bridge.exe`), non un symlink:
  dopo ogni build va ricopiato, e un `.exe` in esecuzione (`next` in background) non si
  sovrascrive: si rinomina prima, poi si copia. `[D]` sulla seconda parte, da provare.
- Le shell dei tool catturano il PATH all'avvio: un Go installato dopo non si vede. Stessa
  lezione del Mac, stesso rimedio (prefisso PATH o riavvio della sessione).
- `dcg` su Windows blocca i redirect `>` verso path in variabile: i gate usano path letterali.

## Dove sta il resto

Report completo, brief ESC e output intero del gate sono in `.handover/` del PC (non
tracciato). Questo file e' il riassunto che viaggia.
