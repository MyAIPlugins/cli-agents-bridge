# Developer conventions — cli-agents-bridge

Style, commit format, and test pattern conventions in use across Sprint 0-3. Distilled from the project rules plus empirical patterns from the Sprint 1-3 commits.

**This file is the published source for the rules the code refers to.** Comments and older documents in this repository sometimes cite `CLAUDE.md`: those are the maintainers' internal method notes, which are not published — every rule they invoke that constrains the code is restated here.

---

## Go style

### Package naming

- Short, lowercase, no underscores: `session`, `routing`, `cleanup` — not `session_manager`.
- One concept per package. `internal/transport/fs/` for filesystem transport, separate from `internal/message/` which models the wire format.

### Error handling

- Always explicit: `if err != nil { return fmt.Errorf("context: %w", err) }`.
- NO `panic` in library code. Even `must-have` invariants surface as returned errors so the caller can log + recover.
- Sentinel errors (`errors.Is` friendly) when callers may want to branch:

```go
var ErrTimeout = errors.New("receive timeout: no reply within deadline")
// caller:
if errors.Is(err, transportfs.ErrTimeout) { os.Exit(124) }
```

### Context propagation

- `context.Context` is the first parameter of any long-running operation:

```go
func (m *Manager) Register(ctx context.Context, opts RegisterOpts) (*Manifest, func() error, error)
```

- `defer cancel()` after `context.WithCancel` or `context.WithTimeout`.
- Goroutines must respect `ctx.Done()`:

```go
go func() {
    for {
        select {
        case <-ctx.Done(): return
        case <-ticker.C:  doWork()
        }
    }
}()
```

### Goroutine discipline (heartbeat idiom)

When a goroutine has a lifecycle tied to a session, return a `<-chan struct{}` that closes when the goroutine exits. Caller can wait for clean shutdown:

```go
done := mgr.StartHeartbeat(ctx, sid)
// ... work ...
cancel()
<-done   // wait for goroutine to exit
```

This idiom is used by `Manager.StartHeartbeat` and any future long-running goroutine helper.

### File length

- Hard cap: **600 lines per file**. If a file is approaching the cap, split the package or extract helpers into a sibling file.
- As of Sprint 3 the largest file is `internal/session/manager.go` at 341 lines.

### Comments

- Only for non-obvious WHY. Don't repeat what well-named code already says.
- Document security-critical invariants with SC-* reference for traceability:

```go
// SC-4 enforces ^[a-z0-9]{6,32}$ to prevent path traversal — see PLAN §9
```

- Reference issue/BUG numbers in fix commentary:

```go
// BUG-2 fix vs Patil bridge-receive.sh:15-43 strict-< loop
```

---

## Commit conventions

### Subject format

```
<type>(<scope>): <subject>
```

Types in use:

- `feat` — new feature
- `fix` — bug fix not covered by an upstream BUG identifier
- `refactor` — code reshape without behavior change
- `test` — test-only changes
- `docs` — documentation
- `chore` — tooling, dependencies, release prep

Scope examples from Sprint 1-3: `feat(session)`, `feat(transport)`, `feat(message)`, `feat(routing)`, `refactor(layout)`, `docs(sprint-N)`.

### Body content

For non-trivial commits the body should document:

1. **Which BUG this fixes** (1-2 sentence summary).
2. **Patil reference** (file:line) + cli-agents-bridge symbol implementing the fix.
3. **Test coverage** (which tests assert the fix).
4. **Caveats** (e.g. EXDEV explicit handling, NFS limitations).

See [git log](../) on Sprint 1-3 commits for templates.

### Co-author trailer

Mandatory on every commit:

```
Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
```

### Discipline rules

- No emoji in commit messages, code, or docs (unless explicitly requested).
- Commit one logical unit at a time — not one commit per micro-change.
- Docs are committed separately from code, for audit clarity.
- No commit until done criteria are green.

### Project rules the code cites

Two rules are invoked by name in source comments; they constrain the code, so they are stated here rather than only in the internal notes.

- **No implicit fallbacks.** A fallback must be explicit in the code, documented, and tested. Silent degradation is a defect: when an operation cannot do what it says, it returns an error rather than quietly doing something else. Cited by `internal/message/validate.go`, `internal/routing/role.go`, `internal/transport/fs/atomic.go`, `internal/transport/fs/process.go`.
- **Zero runtime dependencies.** The bridge ships as a single static Go binary: no jq, no Python, no Node, no cgo. A design that needs a runtime dependency is rejected or reworked, not shipped with an install note.
- **Shell only as Makefile glue, with one named exception.** Product logic is Go. `scripts/skills.sh`, behind `make install-skills` and `make skills-check`, is a development tool written in POSIX `sh`: a double loop over runtimes and skills with per-file comparisons does not fit in recipe lines without the quoting errors this repository has already paid for (paths with spaces). It never ships in the binary, it is tested from `tests/integration/makefile_skills_test.go` under both bash and dash, and it is not for Windows. A second script needs the same justification, written here.

---

## Test patterns

### Unit test naming

`TestXxx_Scenario` pattern:

```go
func TestValidateSessionID(t *testing.T) { ... }
func TestValidateSessionID_RejectsPathTraversal(t *testing.T) { ... }
```

### Table-driven for >3 cases

```go
cases := []struct {
    name string
    in   string
    want bool
}{
    {"happy", "abc123", true},
    {"too short", "abc", false},
    ...
}
for _, tc := range cases {
    t.Run(tc.name, func(t *testing.T) { ... })
}
```

### Parallel

`t.Parallel()` at the top of every test that doesn't mutate process state. Exclusions:

- Tests that touch `syscall.Umask` (process-global) must run serially.
- Tests that share a fixed file path (rare) should use `t.TempDir()` per call instead of serializing.

### Subprocess integration tests

For tests that drive `cab-bridge` as a subprocess (regression bug7, integration scenarios):

- `buildCabBridge(t)` / `buildBinary(t)` helper compiles once per package, caches via `sync.Once`.
- `run(t, args, env)` returns `(stdout, stderr, exitCode)` — caller asserts.
- `dataDirEnv(dataDir, extra...)` constructs the env override slice with `CAB_DATA_DIR` and optional `CAB_*` accelerations.

### Test acceleration via config injection

When a behavior depends on wall-clock (heartbeat interval, poll interval), inject via env vars:

```go
cmd.Env = append(os.Environ(),
    "CAB_HEARTBEAT_TICK_MS=50",  // vs 30000 production
    "CAB_POLL_INTERVAL_MS=100",
)
```

Test wall-clock should never exceed a few seconds.

### Race detector

`make test-race` runs `go test -race ./...`. CI gate. All goroutine code must pass with race detector clean. As of Sprint 3, 90+ sub-tests pass with `-race`.

---

## Lessons learned

The maintainers keep a running `Lessons learned` log in their internal notes, one entry per durable insight. Active entries as of Sprint 3:

- **LL-1**: empirically-unverified hypotheses are delayed mines — flag "DA VERIFICARE" explicitly.
- **LL-2**: `/ultraplan` cloud is valuable for kickoff (independence) but echo-chamber for late-stage review.
- **LL-3**: naming triage — verify GitHub/npm/PyPI/cargo collisions before committing.
- **LL-4**: motivated ESC pushback beats VAL deference — code review valued over hierarchy.
- **LL-5**: empirical-first spike for "DA VERIFICARE" hypotheses pays for itself.
- **LL-6**: actual sprint velocity ~1h per sprint vs estimated 3-7h (Sprint 1-3 trend).

When proposing a process change, draft an LL entry for VAL review.

---

## Patterns the codebase established

These are not style preferences: each one was adopted after a concrete defect, and the code
implements it today. Every file and symbol named below was checked against the tree on 2026-09-13 —
if you find one that no longer matches, the document is wrong and the code is right.

### Subcommand flag parsing

`flag.NewFlagSet(name, flag.ContinueOnError)` plus an explicit `flag.ErrHelp` branch. NOT
`flag.ExitOnError`, which takes error handling away from the caller and makes the subcommand
untestable without a subprocess.

```go
// cmd/cab-bridge/connect.go
fs := flag.NewFlagSet("connect", flag.ContinueOnError)
fs.SetOutput(os.Stderr)
// ...
if err := fs.Parse(args); err != nil {
    if errors.Is(err, flag.ErrHelp) {
        // print the subcommand's own usage, then return without an error
        return nil
    }
    return fmt.Errorf("connect: %w", err)
}
```

Same shape in `join.go`, `register.go`, `cleanup.go`, `notify_watch.go`.

### Two decoders, not one boolean

The JSON gateway exposes **two named functions** instead of one with a `strict bool` parameter:

- `message.DecodeStrict` — `DisallowUnknownFields`, for the **write/audit** path: a typo or a schema
  drift is refused rather than silently dropped.
- `message.DecodeLenient` — ignores unknown fields, for the **runtime read** path: a peer on a newer
  additive schema can still talk to us.

Both in `internal/message/validate.go`. A single function taking a boolean is the anti-pattern: at the
call site `true` says nothing about which of the two behaviours the caller wanted.

### Pointer for JSON null semantics

Optional fields that can be **explicitly null** are `*T`, not a zero value — so `null` and
`"absent"` stay distinguishable from `""`. `Message.InReplyTo` is `*string`
(`internal/message/schema.go`), and the type's own comment states the reason.

### One place maps errors to exit codes

Subcommands return `error` to the dispatcher; a single `exitFromErr` in `cmd/cab-bridge/main.go`
turns sentinel errors into exit codes. The anti-pattern is each subcommand calling `os.Exit`
directly: exit codes scatter and none of them can be tested without a subprocess.

Add a new code only when it is **semantically distinct for a caller writing a script** — today the
function maps `ErrConfirmRequired` to `3` and everything else to `1`.

### Structural invariant vs overridable default

Two kinds of restriction, and they are implemented differently on purpose:

- **Structural invariant** — cannot be relaxed, no flag exists. An `observer` cannot send: it is
  read-only by design, so the check is an early `return` before any flag is consulted.
- **Convenient default** — can be overridden deliberately. `esc → esc` is refused *by default*, and
  `--allow-mesh` permits it for a documented case.

Both in `internal/routing/role.go`. Putting a flag on an invariant corrupts the model; leaving a
default without one turns a preference into a rule nobody can escape.

### Minimal dependency injection for testability

Logic that operates on a production path hardcoded in the real world exposes an **optional override
flag** that defaults to production. `migrate` takes `--patil-dir` (`cmd/cab-bridge/migrate.go`),
defaulting to `~/.claude/session-bridge`, so a test injects a temp dir instead of reaching for
`os.Setenv("HOME", ...)` or a filesystem mock. Go-idiomatic, zero runtime overhead.

### What we do NOT use

- **`goleak`** — not a dependency and not imported anywhere. Goroutine discipline is enforced by the
  done-channel idiom above and by `-race`, not by a leak detector. *(Stated because an internal note
  claimed otherwise for months: a tool nobody runs is worse than no tool, because readers assume the
  check exists.)*

### Kernel locks in tests (F-160)

On Unix a `flock` belongs to the open file description, and a fork in flight in the same process carries a copy of the descriptor until its exec. So a lock released in this process can still look busy for a few milliseconds. A test that asks "is it free?" right after a release in the same process asks "does it become free?" (`internal/fslock/locktest.Take`, with its numeric bound). A fixture that releases a lock and hands it to code that does not wait uses `locktest.Settle`. A test that asserts the lock is HELD uses neither: the window only makes a free lock look held.

### A test's premise is asserted, never skipped (F-154)

A test that needs an operating-system state to exist first checks that the state is there, and fails with "premise not reproduced" when it is not, never `t.Skip`. In F-154 this stopped two wrong models of the Windows mechanism before the merge: a test built on "the name stays delete-pending after `os.Remove`" failed on the first runner instead of passing or skipping in silence. A measurement probe follows the same rule, and it records the outcome of every side it exercises: the second probe of F-154 discarded the error of the side that deleted, so it could not say anything about it.

### A test that bets on winning a race (F-163)

A test that needs one side of a race to win, and repeats the round when it loses, must have that probability measured, not stated. `TestLock_ASuccessorKeepsItsLockWhileAContenderPolls` said the successor wins "almost always"; measured, the contender won about one round in ten, so three lost rounds in a row came up on the first macOS runner. Make the interleaving happen instead (a seam that parks one side, as `acquire`'s `afterOpen` does), or measure the odds and size the repetitions on them.

The odds can belong to the scheduler rather than to the code, and then they size nothing. F-153's defect was the order of two operations (the scan guard released after the result was handed over): on one machine the same loop of calls was refused from 0 to 16974 times in 20000 depending on the repetition, and 0 times in 26000 through the MCP layer. The test parks the worker right after the hand-off (`scanSeams.afterHandOff`) and fails every time on the old order.

### Tests that depend on the volume (F-163)

The CI step "The volume, and the tests that depend on it" in `.github/workflows/ci.yml` prints what the runner's volume does (case, NFC/NFD, firmlink), runs by name the tests that depend on it, and fails when one of them is skipped where its property holds. A new test that depends on the volume is added to that step's `-run` and to a `check` line: otherwise it runs, but nobody sees whether it was skipped. `TestWorkflows_TheVolumeStepNamesTestsThatExist` catches a rename, not a test that was never added.

### File identity on Windows (F-160)

On Windows `os.Stat` and `os.Lstat` record the path and load the file identity lazily, at the first `os.SameFile`, by opening the path again at that moment (Go 1.27.1). Two `os.Stat` of the same path therefore always compare as the same file, even when the file was replaced between them. To compare identities taken at two moments, take one from an open handle (`File.Stat` loads it at once) or, in tests, force the load with `internal/fslock/locktest.Identity`. With two `os.Stat`, an assertion that two files differ fails on Windows even when they do, and an assertion that they are the same passes without testing anything: in F-160 the CI caught one of the first kind, and three of the second were found next to it.

### A retry loop that decides on a sentinel (F-160)

When a retry loop decides with `errors.Is(err, sentinel)`, a failure that must be final is not joined to the sentinel with `errors.Join`: it would be retried, and a later success would discard it. The final failure goes in the chain (`%w`), the cause in the text (`%v`). A second example, F-166: when `fslock.TryLock` failed to take the lock and the close failed too, it joined the close error to `ErrBusy`, and a caller that retries on busy retried a close failure; now the close failure is the error, and `ErrBusy` comes back bare only when the lock is busy and the close succeeded.

### A delete that retries is blind (F-170)

`security.RemoveFile` retries a transient ACCESS_DENIED on Windows. The retry deletes by NAME and cannot tell a delete in flight from a replace of the name, so in the second case it removes the newer file. Use it only where that cannot happen or does not matter, and write down which: the Claude Desktop grant, where losing the newest content is a valid outcome of the caller's own operation; and the reply journal (F-176), which is removed only under the session lock and right after checking, in the same critical section, that the journal on disk is the caller's own transaction, so nothing can replace the name between the check and the delete. The retry alone protects nothing: before F-176 the journal was removed outside the lock, and the plan gate of F-170 showed on the real code that a retry there deletes the journal of another reply. A new caller goes into `removeFileCallers` (`internal/security/remove_callers_test.go`) with its reason, and a bare `os.Remove` in `internal/session` is a named exception in `TestBareRemovalsInSessionAreNamedExceptions` (`internal/session/remove_sites_test.go`): both tests fail on one that is not listed.

### Check and act in one critical section (F-176)

When an operation must check something and act on it with nothing in between ("the reply journal on disk is still mine, so I write or remove it"), both go inside ONE `WithSessionLock` closure. Two consecutive sections, one that checks and one that acts, are a check followed by a gamble. A test with another process holding the lock cannot tell the two shapes apart, because it blocks both the same way; the invariant is fixed on the shape of the source instead (`internal/session/replytxn_section_test.go`), and the guard has a test of its own with the shapes it must refuse, so a later rewrite cannot leave it empty.

### Text that goes into a JSON file is valid UTF-8 first (F-176)

`encoding/json` replaces invalid UTF-8 with U+FFFD when it writes, so the copy in memory and the one on disk stop being equal. A text that is frozen into a file and later compared with it (the reply journal) is checked with `utf8.Valid` before any effect, and refused with an explicit error; in the MCP server the check runs on the raw arguments, before `DecodeArgs`, which would convert them by itself. No silent conversion.

### A lock that was not taken is not a failure of the work (F-177)

When an operation needs the session lock, the error of not getting it is told apart from the error of the work done under it, with a type (`lockNotTaken` in `cmd/cab-bridge`), because the advice is opposite: busy means run it again, a retired session means there is nothing to do, any other error of the lock means look at the session folder and do not retry. The work error comes back intact. The text says the cause once: `fmt.Errorf("...(%v)...: %w", err, err)` prints it twice in one line, so a type with its own message and the cause in `Unwrap` is used instead.

### A Makefile variable that runs a tool is asked at first use, and its answer is data (F-174)

A variable that runs a tool (`go env GOEXE`, `go env GOBIN`) is not evaluated while the Makefile is read: a target that does not use the tool must not be able to name it (without Go in the PATH, every `make` used to print `go: Command not found`). The form is `X = $(eval X := $$(shell ...))$(X)`: the first use replaces the recursive variable with its value, so the tool runs once and only if a recipe needs it. The `$$` matters: with a single `$` Make expands the shell output before `eval` reads it, and the answer is parsed again as Makefile source, so a `#` in a path starts a comment and a `$` expands a variable (CRI measured `make lint` refusing a staticcheck that was there, with `GOBIN=.../bin#suffix`). A probe of such a variable includes the characters that are syntax in the layer it crosses (`#`, `$`, a space), not only ordinary paths. Still open, F-181: the recipe of `lint` pastes the path into a double-quoted shell string, so a `$` in it breaks at the shell layer, also before F-174.
