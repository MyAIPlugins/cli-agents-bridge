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

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// runReceive implements the `cab-bridge receive` subcommand.
//
// Flags:
//
//	--msg-id        original message ID to wait for reply to (required)
//	--max-deadline  max seconds to wait for reply (default 1800 = 30 min)
//	--session-id    target session (default: longest-prefix lookup from cwd)
//
// Output:
//
//	stdout: matched message as indented JSON on success.
//	stderr: error messages on failure (BUG-7 fix: never to stdout).
//
// Exit codes (mapped in main.go from returned error):
//
//	0    success — reply found and written to stdout
//	1    config/validation/IO error
//	124  ErrTimeout — deadline elapsed without a matching reply
func runReceive(args []string) error {
	fs := flag.NewFlagSet("receive", flag.ContinueOnError)
	fs.SetOutput(os.Stderr) // flag errors go to stderr
	msgID := fs.String("msg-id", "", "original message ID to wait for reply to (required)")
	maxDeadlineSec := fs.Int("max-deadline", 1800, "max seconds to wait for reply (default 1800 = 30 min)")
	sessionIDFlag := fs.String("session-id", "", "session ID (default: longest-prefix lookup from cwd)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // help already printed by flag pkg, exit 0 success
		}
		return err
	}

	if *msgID == "" {
		return errors.New("receive: --msg-id required")
	}
	if *maxDeadlineSec <= 0 {
		return fmt.Errorf("receive: --max-deadline must be > 0 (got %d)", *maxDeadlineSec)
	}

	cfg, warnings, err := config.Load()
	if err != nil {
		return fmt.Errorf("receive: load config: %w", err)
	}
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "config warning:", w)
	}
	// SC-7: receive loads config directly (custom warning handling) rather than
	// via loadConfigOrFail, so it runs the base-dir integrity check explicitly.
	if err := bootstrapDataDir(cfg.DataDir); err != nil {
		return fmt.Errorf("receive: %w", err)
	}

	mgr := session.NewManager(cfg.DataDir, time.Duration(cfg.HeartbeatTickMs)*time.Millisecond)

	sid := *sessionIDFlag
	if sid == "" {
		cwd, gerr := os.Getwd()
		if gerr != nil {
			return fmt.Errorf("receive: getwd for session lookup: %w", gerr)
		}
		lookup, lerr := mgr.LongestPrefixLookup(cwd)
		if lerr != nil {
			return fmt.Errorf("receive: --session-id not provided and lookup from cwd %q failed: %w", cwd, lerr)
		}
		sid = lookup
	}

	if err := security.ValidateSessionID(sid); err != nil {
		return fmt.Errorf("receive: %w", err)
	}

	inboxDir := filepath.Join(cfg.DataDir, "sessions", sid, "inbox")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// SIGTERM/SIGINT cancellation. Buffered chan so signal delivery never
	// drops if our goroutine has not yet started receiving.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigs
		cancel()
	}()

	deadline := time.Duration(*maxDeadlineSec) * time.Second
	interval := time.Duration(cfg.PollIntervalMs) * time.Millisecond

	m, err := transportfs.ReceiveReply(ctx, inboxDir, *msgID, deadline, interval, cfg.MaxMessageBytes)
	if err != nil {
		return err
	}

	// F-12: record the matched reply as the last consumed message (best-effort;
	// a failure here must not turn a successful receive into an error).
	if serr := mgr.SetLastConsumed(sid, m.ID); serr != nil {
		fmt.Fprintf(os.Stderr, "receive: record lastConsumed for %s: %v\n", m.ID, serr)
	}

	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("receive: marshal output: %w", err)
	}
	fmt.Println(string(out))
	return nil
}
