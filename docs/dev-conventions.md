# Developer conventions — cli-agents-bridge

Style, commit format, and test pattern conventions in use across Sprint 0-3. Extracted from CLAUDE.md project rules + empirical patterns from the Sprint 1-3 commits.

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

### Discipline rules (CLAUDE.md)

- No emoji in commit messages, code, or docs (unless explicitly requested).
- Commit one logical unit at a time — not one commit per micro-change.
- VAL commits docs separately from ESC code commits for audit clarity.
- No commit until done criteria are green.

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

The CLAUDE.md `## Lessons learned` section captures durable insights from each sprint. Active entries as of Sprint 3:

- **LL-1**: empirically-unverified hypotheses are delayed mines — flag "DA VERIFICARE" explicitly.
- **LL-2**: `/ultraplan` cloud is valuable for kickoff (independence) but echo-chamber for late-stage review.
- **LL-3**: naming triage — verify GitHub/npm/PyPI/cargo collisions before committing.
- **LL-4**: motivated ESC pushback beats VAL deference — code review valued over hierarchy.
- **LL-5**: empirical-first spike for "DA VERIFICARE" hypotheses pays for itself.
- **LL-6**: actual sprint velocity ~1h per sprint vs estimated 3-7h (Sprint 1-3 trend).

When proposing a process change, draft an LL entry for VAL review.
