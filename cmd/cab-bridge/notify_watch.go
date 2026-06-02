package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

const (
	notifyWatchDefaultPoll   = 15 * time.Second
	notifyWatchDefaultHookTO = 30 * time.Second
	notifyWatchDir           = "notify-watch"
	notifyWatchDefaultName   = "default"
)

// errHookFailed wraps a non-zero hook exit so the watch loop can distinguish a
// (handled, backed-off) hook failure from a genuine I/O error on our own state.
var errHookFailed = errors.New("notify-watch hook failed")

// watchConfig is the resolved, validated configuration of one notify-watch run.
type watchConfig struct {
	pollInterval    time.Duration
	hookTimeout     time.Duration
	shell           bool
	hookArgv        []string
	exitOnHookError bool
}

// hookRunner executes the hook for a batch, given the CAB_* env to inject. It is
// injected into watchTick so the tick logic is testable without spawning a real
// process (production uses execHookRunner).
type hookRunner func(ctx context.Context, env []string) error

// runNotifyWatch implements `cab-bridge notify-watch`: an EXTERNAL watcher (a Go
// process, immune to a peer's torn-down background terminal) that polls a
// session's inbox/ and runs a hook when new messages arrive — the wake path for
// peers with no native push (Codex, F-59/F-66). It does NOT consume: the peer
// still receives the real message via receive/listen; notify-watch only says
// "there is something", typically injecting into the peer TUI via
// `screen -X stuff`. The design is hardened against the naive-loop failure modes
// the CRI flagged (spam, mini remote-exec, false-wake, restart re-spam) — see
// the six non-negotiables in the package docs / brief F-66.
func runNotifyWatch(args []string) error {
	fs_ := flag.NewFlagSet("notify-watch", flag.ContinueOnError)
	fs_.SetOutput(os.Stderr)
	sessionIDFlag := fs_.String("session-id", "", "session ID to watch (default: longest-prefix lookup from cwd)")
	watchName := fs_.String("watch-name", notifyWatchDefaultName, "name for this watcher's lock+state files, so multiple watchers on one session do not collide ([a-z0-9][a-z0-9_-]{0,31})")
	pollInterval := fs_.Duration("poll-interval", notifyWatchDefaultPoll, "how often to scan the inbox (Go duration, e.g. 15s)")
	hookTimeout := fs_.Duration("hook-timeout", notifyWatchDefaultHookTO, "max wall-clock for one hook invocation before it is killed (Go duration)")
	ignoreExisting := fs_.Bool("ignore-existing", false, "mark the messages already pending at startup as seen WITHOUT notifying — only wake on messages arriving afterwards")
	allowConcurrent := fs_.Bool("allow-concurrent-consumer", false, "proceed even if the session looks like it is actively in `listen` (a watcher + a listener on one inbox is a double consumer)")
	shell := fs_.Bool("shell", false, "run the hook via `sh -c <joined argv>` instead of executing argv directly (opt-in; argv-direct is the safe default)")
	dryRun := fs_.Bool("dry-run", false, "do ONE scan, print the hook that would run for the current pending batch, and exit — no state change, no lock")
	exitOnHookError := fs_.Bool("exit-on-hook-error", false, "exit non-zero on the first hook failure instead of backing off and staying operational")
	if err := fs_.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	hookArgv := fs_.Args()
	if len(hookArgv) == 0 {
		return errors.New("notify-watch: a hook command is required after `--` (e.g. notify-watch --session-id=X -- echo new-message)")
	}
	if *pollInterval <= 0 {
		return fmt.Errorf("notify-watch: --poll-interval must be positive, got %v", *pollInterval)
	}
	if *hookTimeout <= 0 {
		return fmt.Errorf("notify-watch: --hook-timeout must be positive, got %v", *hookTimeout)
	}
	// --watch-name is a filesystem path component (lock + state file), so it must
	// be validated against traversal/odd chars. ValidateTeamID's charset
	// ([a-z0-9][a-z0-9_-]{0,31}) is exactly the right shape for a short logical name.
	if err := security.ValidateTeamID(*watchName); err != nil {
		return fmt.Errorf("notify-watch: --watch-name %q invalid (want [a-z0-9][a-z0-9_-]{0,31}): %w", *watchName, err)
	}

	cfg, err := loadConfigOrFail()
	if err != nil {
		return err
	}
	mgr := newSessionManager(cfg)
	sid, err := resolveSessionID(mgr, *sessionIDFlag)
	if err != nil {
		return err
	}
	sessionDir := filepath.Join(cfg.DataDir, "sessions", sid)

	mf, err := mgr.LoadManifest(sid)
	if err != nil {
		return fmt.Errorf("notify-watch: load manifest: %w", err)
	}

	logw := os.Stderr
	// Non-negotiable #6 startup-target print: who is being watched and what runs.
	fmt.Fprintf(logw, "notify-watch: watching session %s (agent %q, role %s, scope %s)\n", sid, mf.AgentName, mf.Role, mf.Scope)
	fmt.Fprintf(logw, "notify-watch: hook %v (shell=%v) poll=%v hook-timeout=%v watch-name=%s\n", hookArgv, *shell, *pollInterval, *hookTimeout, *watchName)

	wcfg := watchConfig{
		pollInterval:    *pollInterval,
		hookTimeout:     *hookTimeout,
		shell:           *shell,
		hookArgv:        hookArgv,
		exitOnHookError: *exitOnHookError,
	}

	// --dry-run: one scan + print, no lock, no state mutation.
	if *dryRun {
		return notifyWatchDryRun(sessionDir, sid, wcfg, cfg.MaxMessageBytes, logw)
	}

	// Non-negotiable #5 (guardrail): a watcher + a live listener on the same inbox
	// double-consume — notify-watch injects "you have mail" but listen may already
	// have moved the message to processed/. Reuse the F-81 liveness check.
	if session.IsProcessAlive(mf.PID) && mf.ListenUntil != nil && mf.ListenUntil.After(time.Now()) {
		warn := fmt.Sprintf("session %s looks actively in listen (PID %d, window until %s) — a watcher + a listener on one inbox is a double consumer",
			sid, mf.PID, mf.ListenUntil.UTC().Format(time.RFC3339))
		if !*allowConcurrent {
			return fmt.Errorf("notify-watch: %s; pass --allow-concurrent-consumer to proceed anyway", warn)
		}
		fmt.Fprintf(logw, "notify-watch: WARN %s; proceeding (--allow-concurrent-consumer)\n", warn)
	}

	// Non-negotiable #5 (lock): one watcher per (session, watch-name). Reuses the
	// session lock primitive (O_EXCL + PID stale recovery, SC-6).
	watchDir := filepath.Join(sessionDir, notifyWatchDir)
	if err := os.MkdirAll(watchDir, 0o700); err != nil {
		return fmt.Errorf("notify-watch: create watch dir: %w", err)
	}
	lockPath := filepath.Join(watchDir, *watchName+".lock")
	release, err := session.AcquireLock(lockPath, false)
	if err != nil {
		return fmt.Errorf("notify-watch: %w", err)
	}
	defer func() { _ = release() }()

	statePath := filepath.Join(watchDir, *watchName+".json")
	st, err := loadWatchState(statePath)
	if err != nil {
		return err
	}

	warned := map[string]bool{}

	// Non-negotiable startup behaviour: by default notify the messages already
	// pending at startup (the no-push case — the watcher is started AFTER the
	// message arrived). --ignore-existing instead marks them seen without firing
	// the hook, so only later arrivals wake.
	if *ignoreExisting {
		pending, perr := collectPendingForNotify(sessionDir, cfg.MaxMessageBytes, logw, warned)
		if perr != nil {
			return perr
		}
		now := time.Now().UTC()
		for _, e := range pending {
			st.markSuccess(e.MsgID, now)
		}
		if err := st.save(statePath); err != nil {
			return err
		}
		fmt.Fprintf(logw, "notify-watch: --ignore-existing: %d pending message(s) marked seen without notifying\n", len(pending))
	}

	runner := execHookRunner(wcfg, logw)

	ctx, cancel := notifyWatchSignalContext()
	defer cancel()
	ticker := time.NewTicker(*pollInterval)
	defer ticker.Stop()

	fmt.Fprintln(logw, "notify-watch: watching (Ctrl-C to stop)")
	for {
		if err := watchTick(ctx, sessionDir, sid, st, statePath, wcfg, runner, time.Now().UTC(), cfg.MaxMessageBytes, logw, warned); err != nil {
			if errors.Is(err, errHookFailed) {
				if wcfg.exitOnHookError {
					return err
				}
				// already logged + backed off in watchTick; stay operational.
			} else {
				// I/O error on our own scan/state: log and keep watching rather
				// than crashing the watcher on a transient FS hiccup.
				fmt.Fprintf(logw, "notify-watch: tick error (continuing): %v\n", err)
			}
		}
		select {
		case <-ctx.Done():
			fmt.Fprintln(logw, "notify-watch: shutting down")
			return nil
		case <-ticker.C:
		}
	}
}

// watchTick performs ONE scan+notify pass: peek the pending inbox, prune markers
// for consumed ids, select the not-yet-notified (or retry-due) candidates,
// coalesce them into a SINGLE batch, fire the hook once, and mark the batch on
// exit-0 (or record a failure + backoff otherwise). Pure except for the injected
// runner, so it is fully unit-testable. Returns errHookFailed (wrapped) on a hook
// non-zero exit, or a raw error on a scan/state I/O failure.
func watchTick(ctx context.Context, sessionDir, sid string, st *watchState, statePath string, cfg watchConfig, run hookRunner, now time.Time, maxBytes int, logw io.Writer, warned map[string]bool) error {
	pending, err := collectPendingForNotify(sessionDir, maxBytes, logw, warned)
	if err != nil {
		return err
	}

	// Prune markers for ids no longer pending (consumed by the peer) so state
	// does not grow without bound.
	present := make(map[string]bool, len(pending))
	for _, e := range pending {
		present[e.MsgID] = true
	}
	st.prune(present)

	// Coalesce candidates into ONE batch (non-negotiable #3: never one hook per
	// message → no 10× spam).
	var batch []inboxEntry
	for _, e := range pending {
		if st.shouldNotify(e.MsgID, cfg.pollInterval, now) {
			batch = append(batch, e)
		}
	}
	if len(batch) == 0 {
		return st.save(statePath) // prune may have changed state; persist it
	}

	ids := make([]string, len(batch))
	for i, e := range batch {
		ids[i] = e.MsgID
	}
	fmt.Fprintf(logw, "notify-watch: hook start: %d message(s) ids=%s\n", len(batch), strings.Join(ids, ","))

	runErr := run(ctx, buildHookEnv(sid, batch))
	if runErr != nil {
		for _, e := range batch {
			st.markFailure(e.MsgID, runErr.Error(), now)
		}
		_ = st.save(statePath)
		fmt.Fprintf(logw, "notify-watch: hook FAILED for ids=%s: %v (will back off)\n", strings.Join(ids, ","), runErr)
		return fmt.Errorf("%w: ids=%s: %v", errHookFailed, strings.Join(ids, ","), runErr)
	}

	// Non-negotiable #4: mark ONLY after exit-0.
	for _, e := range batch {
		st.markSuccess(e.MsgID, now)
	}
	fmt.Fprintf(logw, "notify-watch: hook OK: notified %d message(s) ids=%s\n", len(batch), strings.Join(ids, ","))
	return st.save(statePath)
}

// collectPendingForNotify scans ONLY the session's inbox/ (pending) as a pure
// read, returning the non-ack messages. Unlike collectInbox it (a) ignores
// processed/ — a watcher cares about what is still pending — and (b) LOGS an
// unreadable/malformed file once per filename (the warned set), since a 24h
// unattended watcher must not silently drop a message (non-negotiable #6, the
// F-51 concern scoped to the watcher's own scan). type=ack and the usual
// dir/.tmp.*/non-.json entries are skipped silently (expected, not anomalies).
func collectPendingForNotify(sessionDir string, maxBytes int, logw io.Writer, warned map[string]bool) ([]inboxEntry, error) {
	dir := filepath.Join(sessionDir, "inbox")
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil // inbox not created yet — nothing pending
		}
		return nil, fmt.Errorf("notify-watch: read inbox: %w", err)
	}
	var out []inboxEntry
	for _, de := range dirEntries {
		name := de.Name()
		if de.IsDir() || strings.HasPrefix(name, ".tmp.") || !strings.HasSuffix(name, ".json") {
			continue
		}
		full := filepath.Join(dir, name)
		data, rerr := os.ReadFile(full)
		if rerr != nil {
			if !warned[name] {
				fmt.Fprintf(logw, "notify-watch: WARN skipping unreadable inbox file %q: %v\n", name, rerr)
				warned[name] = true
			}
			continue
		}
		m, derr := message.DecodeLenient(data, maxBytes)
		if derr != nil {
			if !warned[name] {
				fmt.Fprintf(logw, "notify-watch: WARN skipping malformed inbox file %q: %v\n", name, derr)
				warned[name] = true
			}
			continue
		}
		if m.Type == message.TypeAck {
			continue
		}
		out = append(out, inboxEntry{
			Box:           "inbox",
			MsgID:         m.ID,
			From:          m.From,
			FromAgentName: m.FromAgentName,
			Type:          m.Type,
			Timestamp:     m.Timestamp,
		})
	}
	return out, nil
}

// buildHookEnv constructs the safe environment passed to the hook: METADATA
// ONLY, never message content/preview (non-negotiable #1). Comma-separated lists
// are parallel (ids[i] / froms[i] / types[i] describe the same message).
func buildHookEnv(sid string, batch []inboxEntry) []string {
	ids := make([]string, len(batch))
	froms := make([]string, len(batch))
	types := make([]string, len(batch))
	for i, e := range batch {
		ids[i] = e.MsgID
		froms[i] = e.From
		types[i] = e.Type
	}
	return []string{
		"CAB_SESSION_ID=" + sid,
		"CAB_MSG_IDS=" + strings.Join(ids, ","),
		"CAB_MSG_COUNT=" + strconv.Itoa(len(batch)),
		"CAB_FROM_IDS=" + strings.Join(froms, ","),
		"CAB_TYPES=" + strings.Join(types, ","),
	}
}

// execHookRunner returns the production hookRunner: it executes the configured
// hook with the per-batch env appended to the current environment, bounded by
// --hook-timeout. argv-direct by default (no shell, no interpolation); --shell
// opts into `sh -c <joined argv>`. Hook stdout/stderr are forwarded to the
// watcher's log so the operator sees what the hook did.
func execHookRunner(cfg watchConfig, logw io.Writer) hookRunner {
	return func(ctx context.Context, env []string) error {
		hctx, cancel := context.WithTimeout(ctx, cfg.hookTimeout)
		defer cancel()

		var cmd *exec.Cmd
		if cfg.shell {
			cmd = exec.CommandContext(hctx, "sh", "-c", strings.Join(cfg.hookArgv, " "))
		} else {
			cmd = exec.CommandContext(hctx, cfg.hookArgv[0], cfg.hookArgv[1:]...)
		}
		cmd.Env = append(os.Environ(), env...)
		cmd.Stdout = logw
		cmd.Stderr = logw
		return cmd.Run()
	}
}

// notifyWatchDryRun does a single scan and prints the batch + the hook that
// WOULD run, without acquiring the lock or touching state. A preview to validate
// the configuration before launching the real watcher.
func notifyWatchDryRun(sessionDir, sid string, cfg watchConfig, maxBytes int, logw io.Writer) error {
	warned := map[string]bool{}
	pending, err := collectPendingForNotify(sessionDir, maxBytes, logw, warned)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		fmt.Fprintln(logw, "notify-watch: [dry-run] no pending non-ack messages; hook would not run")
		return nil
	}
	env := buildHookEnv(sid, pending)
	fmt.Fprintf(logw, "notify-watch: [dry-run] %d pending message(s); hook would run once:\n", len(pending))
	if cfg.shell {
		fmt.Fprintf(logw, "  sh -c %q\n", strings.Join(cfg.hookArgv, " "))
	} else {
		fmt.Fprintf(logw, "  %v\n", cfg.hookArgv)
	}
	for _, kv := range env {
		fmt.Fprintf(logw, "  env %s\n", kv)
	}
	return nil
}

// notifyWatchSignalContext returns a context cancelled on SIGINT/SIGTERM, for a
// clean watcher shutdown (the deferred lock release then runs).
func notifyWatchSignalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigs:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
