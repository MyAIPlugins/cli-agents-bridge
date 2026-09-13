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
