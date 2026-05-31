package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// waitOneTimeout is the F-24 sentinel payload emitted when a --wait-one window
// expires on an empty inbox: a single JSON object whose "status" field lets a
// caller distinguish an exit-0 timeout from a delivered batch (the delivery path
// emits per-message NDJSON with no envelope). Messages is always the empty array.
type waitOneTimeout struct {
	Status   string `json:"status"`
	Messages []any  `json:"messages"`
}

// resolveMaxBlocking computes the listen window with precedence
// flag > env > default (F-26). flagVal, when non-empty, is a Go duration string
// (--until-deadline) and wins; an invalid or non-positive value is a hard error.
// Otherwise cfgSeconds — already the CAB_MAX_BLOCKING_SECONDS env or its config
// default — is used, falling back to 540s (9 min) when unset/non-positive.
func resolveMaxBlocking(flagVal string, cfgSeconds int) (time.Duration, error) {
	if flagVal != "" {
		d, err := time.ParseDuration(flagVal)
		if err != nil {
			return 0, fmt.Errorf("invalid --until-deadline %q (want a Go duration like 2h or 30m): %w", flagVal, err)
		}
		if d <= 0 {
			return 0, fmt.Errorf("--until-deadline must be positive, got %q", flagVal)
		}
		return d, nil
	}
	d := time.Duration(cfgSeconds) * time.Second
	if d <= 0 {
		d = 9 * time.Minute
	}
	return d, nil
}

func runListen(args []string) error {
	fs := flag.NewFlagSet("listen", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	sessionIDFlag := fs.String("session-id", "", "session ID (default: longest-prefix lookup from cwd)")
	noAutoAck := fs.Bool("no-auto-ack", false, "disable the automatic delivery receipt sent to a query's sender on consume (F-12)")
	waitOne := fs.Bool("wait-one", false, "exit 0 after delivering the first non-empty batch of messages; on an empty-window timeout also exit 0, emitting a {\"status\":\"timeout\",\"messages\":[]} payload instead of failing (F-10/F-24: wake-on-arrival for run-in-background callers; default off)")
	untilDeadline := fs.String("until-deadline", "", "explicit listen window as a Go duration (e.g. 2h, 30m); overrides CAB_MAX_BLOCKING_SECONDS and the 540s default for this run (F-26)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
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

	// BUG-A: take ownership of the session for this long-running listen
	// process. register wrote an ephemeral PID that has already died; without
	// this, BUG-6 collision detection and stale detection never observe a live
	// owner. The heartbeat goroutine below keeps lastHeartbeat fresh thereafter.
	if err := mgr.AdoptPID(sid); err != nil {
		return fmt.Errorf("listen: adopt session PID: %w", err)
	}

	// MaxBlocking bounds the wall-clock duration of listen so the Claude Code
	// agent harness 10-min subprocess timeout never kills us silently. On hit the
	// default path exits 124 — the same convention as receive — so the harness
	// wrapper can re-launch us; --wait-one instead exits 0 with a timeout payload
	// (F-24). Window precedence (F-26): --until-deadline flag > the
	// CAB_MAX_BLOCKING_SECONDS env (already folded into cfg) > 540s default.
	maxBlocking, err := resolveMaxBlocking(*untilDeadline, cfg.MaxBlockingSeconds)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), maxBlocking)
	defer cancel()

	// SIGTERM/SIGINT cancellation overrides the timeout if the user hits Ctrl-C.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		select {
		case <-sigs:
			cancel()
		case <-ctx.Done():
		}
	}()

	// Heartbeat goroutine (BUG-1 fix exercised in cmd).
	hbDone := mgr.StartHeartbeat(ctx, sid)
	defer func() { <-hbDone }()

	inboxDir := filepath.Join(cfg.DataDir, "sessions", sid, "inbox")
	pollInterval := time.Duration(cfg.PollIntervalMs) * time.Millisecond

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	// F-10: --wait-one exits as soon as the first non-empty batch is delivered,
	// so a run-in-background caller is woken the instant a message arrives
	// instead of only at the MaxBlocking timeout. DrainInboxOnce returns the
	// WHOLE batch present at the sweep (not literally one message), so no
	// message is left consumed-but-unseen — the loss the channel-based poller
	// would risk if we exited mid-stream. Default off → PollInbox path below.
	if *waitOne {
		for {
			msgs, err := transportfs.DrainInboxOnce(inboxDir, cfg.MaxMessageBytes)
			if err != nil {
				fmt.Fprintf(os.Stderr, "cab-bridge: listen --wait-one: drain inbox: %v\n", err)
			}
			if len(msgs) > 0 {
				for _, m := range msgs {
					if err := enc.Encode(m); err != nil {
						cancel()
						return fmt.Errorf("listen: encode message: %w", err)
					}
					// F-12: record consumption then auto-ack the sender, BEFORE
					// the exit below, exactly as the default path does.
					if err := mgr.SetLastConsumed(sid, m.ID); err != nil {
						fmt.Fprintf(os.Stderr, "cab-bridge: listen: record lastConsumed for %s: %v\n", m.ID, err)
					}
					if !*noAutoAck {
						maybeAutoAck(cfg, mgr, sid, m)
					}
				}
				// Explicit cancel before returning with a still-live ctx: the
				// deferred `<-hbDone` (registered after `defer cancel`, so it
				// runs FIRST under LIFO) blocks until the heartbeat goroutine
				// sees ctx.Done. The default path returns only once ctx is
				// already Done, so it needs no explicit cancel; --wait-one does.
				cancel()
				return nil
			}
			select {
			case <-ctx.Done():
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					// F-24: an empty --wait-one window that expires is a valid
					// result, not a failure. Emit a timeout payload and exit 0 so a
					// run-in-background harness reads success (not "command failed")
					// every idle cycle; the caller tells this timeout from a
					// delivered batch by the "status" field. The default PollInbox
					// path below keeps exit 124 — a bash until-loop relies on it.
					if err := enc.Encode(waitOneTimeout{Status: "timeout", Messages: []any{}}); err != nil {
						return fmt.Errorf("listen --wait-one: encode timeout payload: %w", err)
					}
					return nil
				}
				return nil // SIGINT/clean cancel — exit 0, same as the default path.
			case <-time.After(pollInterval):
			}
		}
	}

	ch := transportfs.PollInbox(ctx, inboxDir, pollInterval, cfg.MaxMessageBytes)
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				// Channel closed by poller (ctx canceled). Decide between
				// timeout-exit-124 and clean-exit based on ctx.Err.
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return fmt.Errorf("listen: max-blocking timeout %v reached, exit for harness re-run: %w",
						maxBlocking, transportfs.ErrTimeout)
				}
				return nil
			}
			// Emit one JSON object per line (newline-delimited JSON, easy to
			// pipe to jq -c or process with a loop on the caller side).
			if err := enc.Encode(m); err != nil {
				return fmt.Errorf("listen: encode message: %w", err)
			}
			// F-12: record consumption (observability) then auto-ack the
			// sender. Both are best-effort — a failure here must not break the
			// listen loop, so we log and continue.
			if err := mgr.SetLastConsumed(sid, m.ID); err != nil {
				fmt.Fprintf(os.Stderr, "cab-bridge: listen: record lastConsumed for %s: %v\n", m.ID, err)
			}
			if !*noAutoAck {
				maybeAutoAck(cfg, mgr, sid, m)
			}
		case <-ctx.Done():
			// Loop continues — the next iteration will hit the closed
			// channel and return via the timeout/clean exit branch above.
		}
	}
}
