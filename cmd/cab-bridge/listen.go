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

func runListen(args []string) error {
	fs := flag.NewFlagSet("listen", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	sessionIDFlag := fs.String("session-id", "", "session ID (default: longest-prefix lookup from cwd)")
	noAutoAck := fs.Bool("no-auto-ack", false, "disable the automatic delivery receipt sent to a query's sender on consume (F-12)")
	waitOne := fs.Bool("wait-one", false, "exit (code 0) after delivering the first non-empty batch of messages, instead of blocking until the MaxBlocking timeout (F-10: wake-on-arrival for run-in-background callers; default off)")
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

	// MaxBlockingSeconds bounds the wall-clock duration of listen so the
	// Claude Code agent harness 10-min subprocess timeout never kills us
	// silently. On hit we exit 124 — the same convention as receive — so
	// the harness wrapper can re-launch us. Default 540s = 9 min.
	maxBlocking := time.Duration(cfg.MaxBlockingSeconds) * time.Second
	if maxBlocking <= 0 {
		maxBlocking = 9 * time.Minute
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
					return fmt.Errorf("listen --wait-one: max-blocking timeout %v reached, exit for harness re-run: %w",
						maxBlocking, transportfs.ErrTimeout)
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
