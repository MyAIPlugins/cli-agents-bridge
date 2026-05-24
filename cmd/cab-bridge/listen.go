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
		case <-ctx.Done():
			// Loop continues — the next iteration will hit the closed
			// channel and return via the timeout/clean exit branch above.
		}
	}
}
