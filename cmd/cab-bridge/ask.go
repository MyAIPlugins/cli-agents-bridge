package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
)

func runAsk(args []string) error {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	to := fs.String("to", "", "target session ID (required)")
	msgType := fs.String("type", message.TypeQuery, "message type (query|response|ping|notify|event)")
	content := fs.String("content", "", "message content (mutually exclusive with --file)")
	contentFile := fs.String("file", "", "read content from file (avoids shell quoting for large payloads, FRIC-2)")
	inReplyTo := fs.String("in-reply-to", "", "msg-... ID this message replies to (required for type=response)")
	allowMesh := fs.Bool("allow-mesh", false, "allow esc→esc routing (BUG-3 override)")
	sessionIDFlag := fs.String("session-id", "", "sender session ID (default: longest-prefix lookup from cwd)")
	skipDuplicate := fs.Bool("skip-duplicate", false, "if an identical message (same to/type/content within DedupWindowSeconds) was just sent, skip the resend and print the original id instead (F-43)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if *to == "" {
		return errors.New("ask: --to required")
	}
	if err := security.ValidateSessionID(*to); err != nil {
		return fmt.Errorf("ask: --to: %w", err)
	}
	if *content != "" && *contentFile != "" {
		return errors.New("ask: --content and --file are mutually exclusive")
	}

	body := *content
	if *contentFile != "" {
		data, err := os.ReadFile(*contentFile)
		if err != nil {
			return fmt.Errorf("ask: read --file %q: %w", *contentFile, err)
		}
		body = string(data)
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

	// F-43: guard against a degraded agent re-invoking `ask` before the first
	// send returned. A recent identical message (same to/type/content within
	// DedupWindowSeconds) in our own outbox is treated as a duplicate. Default:
	// warn on stderr and send anyway (lose nothing, never block a legitimate
	// repeat). --skip-duplicate: skip the resend and print the ORIGINAL id, so a
	// double-invoke caller still captures a usable id with no duplicate on disk.
	// Disabled when DedupWindowSeconds <= 0.
	if cfg.DedupWindowSeconds > 0 {
		outbox := filepath.Join(cfg.DataDir, "sessions", sid, "outbox")
		dupID, derr := findRecentDuplicate(outbox, *to, *msgType, body, cfg.DedupWindowSeconds, cfg.MaxMessageBytes, time.Now())
		if derr != nil {
			return fmt.Errorf("ask: %w", derr)
		}
		if dupID != "" {
			if *skipDuplicate {
				fmt.Fprintf(os.Stderr, "ask: skipping duplicate of %s (same to/type/content within %ds)\n", dupID, cfg.DedupWindowSeconds)
				fmt.Println(dupID)
				return nil
			}
			fmt.Fprintf(os.Stderr, "ask: warning: looks like a duplicate of %s sent within %ds (same to/type/content); sending anyway — use --skip-duplicate to suppress the resend\n", dupID, cfg.DedupWindowSeconds)
		}
	}

	var inReplyToPtr *string
	if *inReplyTo != "" {
		s := *inReplyTo
		inReplyToPtr = &s
	}

	msgID, err := sendMessage(cfg, mgr, sid, *to, *msgType, body, inReplyToPtr, *allowMesh)
	if err != nil {
		return err
	}

	// Print the message ID on stdout for caller to capture (e.g. for a
	// subsequent `cab-bridge receive --msg-id=<id>`).
	fmt.Println(msgID)
	return nil
}
