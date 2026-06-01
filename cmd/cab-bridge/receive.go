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

// runReceive implements the `cab-bridge receive` subcommand. Two modes:
//
//	--msg-id   wait for a reply to a SPECIFIC message (one message out).
//	--any      wake on the first batch of ANY non-ack message, no id (F-36) —
//	           the id-less wake for an orchestrator that has nothing to wait ON.
//
// The two are mutually exclusive; exactly one is required.
//
// Flags:
//
//	--msg-id        message ID to wait for a reply to (required unless --any)
//	--any           wake on any non-ack message, without a msg-id
//	--max-deadline  max seconds to wait (default 1800 = 30 min)
//	--session-id    target session (default: longest-prefix lookup from cwd)
//
// Output:
//
//	stdout: --msg-id → the matched message as indented JSON; --any → the drained
//	        batch as NDJSON (one indented object per message).
//	stderr: error messages on failure (BUG-7 fix: never to stdout).
//
// Exit codes (mapped in main.go from returned error):
//
//	0    success — reply/batch written to stdout; OR a --any timeout (exit 0 with
//	     a {"status":"timeout"} payload, the deliberate asymmetry with --msg-id)
//	1    config/validation/IO error
//	124  --msg-id ErrTimeout — deadline elapsed without a matching reply
func runReceive(args []string) error {
	fs := flag.NewFlagSet("receive", flag.ContinueOnError)
	fs.SetOutput(os.Stderr) // flag errors go to stderr
	msgID := fs.String("msg-id", "", "message ID to wait for a reply to (required unless --any)")
	maxDeadlineSec := fs.Int("max-deadline", 1800, "max seconds to wait (default 1800 = 30 min)")
	sessionIDFlag := fs.String("session-id", "", "session ID (default: longest-prefix lookup from cwd)")
	anyFlag := fs.Bool("any", false, "wake on the first batch of any non-ack message, without a msg-id; mutually exclusive with --msg-id (F-36)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil // help already printed by flag pkg, exit 0 success
		}
		return err
	}

	switch {
	case *anyFlag && *msgID != "":
		return errors.New("receive: --any and --msg-id are mutually exclusive — choose one")
	case !*anyFlag && *msgID == "":
		return errors.New("receive: pass --msg-id (wait for a specific reply) or --any (wake on any non-ack message)")
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

	// F-36: --any wakes on the first batch of any non-ack message, with no msg-id.
	if *anyFlag {
		return receiveAny(ctx, mgr, sid, inboxDir, deadline, interval, cfg.MaxMessageBytes)
	}

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

// receiveAny implements `receive --any` (F-36): wake on the first batch of any
// non-ack message, drained and archived, emitted as NDJSON (one indented object
// per message). On an empty-window timeout it exits 0 with a {"status":"timeout"}
// payload — the DELIBERATE asymmetry with --msg-id (which exits 124, F-24): a
// run-in-background wake loop reads success, not "command failed", every idle
// cycle. SetLastConsumed records the last drained message for F-12 observability
// (the batch is consumed in os.ReadDir order, so this is "consumed up to here",
// not a temporal resume cursor — which --any does not need). Best-effort.
func receiveAny(ctx context.Context, mgr *session.Manager, sid, inboxDir string, deadline, interval time.Duration, maxBytes int) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	msgs, err := transportfs.ReceiveAny(ctx, inboxDir, deadline, interval, maxBytes)
	if err != nil {
		if errors.Is(err, transportfs.ErrTimeout) {
			if eerr := enc.Encode(waitOneTimeout{Status: "timeout", Messages: []any{}}); eerr != nil {
				return fmt.Errorf("receive --any: encode timeout payload: %w", eerr)
			}
			return nil
		}
		return err
	}

	for _, m := range msgs {
		if eerr := enc.Encode(m); eerr != nil {
			return fmt.Errorf("receive --any: encode message: %w", eerr)
		}
	}
	if len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		if serr := mgr.SetLastConsumed(sid, last.ID); serr != nil {
			fmt.Fprintf(os.Stderr, "receive --any: record lastConsumed for %s: %v\n", last.ID, serr)
		}
	}
	return nil
}
