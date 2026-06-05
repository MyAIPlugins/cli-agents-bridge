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
	strictReply := fs.Bool("strict-reply", false, "reject (instead of warn) when --in-reply-to points at a message id not found in your inbox/ or processed/ — an existence check against hallucinated ids (F-37)")

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

	// F-34: warn if the recipient has sent us a still-unread (un-consumed) non-ack
	// message AFTER our last message to them — the cross we would make by replying
	// on a stale snapshot (the dominant cause of VAL/ESC message crossings). Always
	// on, never blocks (sends anyway); the warning cites the id so the caller can
	// `cab-bridge read <id>` before replying. Symmetric: it fires for any sender.
	sessionDir := filepath.Join(cfg.DataDir, "sessions", sid)
	lastSent, lserr := lastSentTimeTo(filepath.Join(sessionDir, "outbox"), *to, cfg.MaxMessageBytes)
	if lserr != nil {
		return fmt.Errorf("ask: %w", lserr)
	}
	unreadID, uerr := unreadFromPeer(filepath.Join(sessionDir, "inbox"), *to, lastSent, cfg.MaxMessageBytes)
	if uerr != nil {
		return fmt.Errorf("ask: %w", uerr)
	}
	if unreadID != "" {
		// A-1 (F-34): the suggested command must be executable as-is. In a shared
		// scope (VAL@root + ESC@worktree on the same repo) a bare `read <id>` would
		// resolve the wrong session by cwd lookup and fail with "message not found".
		// The unread message lives in OUR (the sender's) inbox, so the recipient of
		// this warning reads it with OUR id — sid, already resolved above. The
		// --session-id flag must come BEFORE the positional (Go flag parsing).
		fmt.Fprintf(os.Stderr, "ask: warning: %s sent %s after your last message to them and it is unread in your inbox — read it before replying (cab-bridge read --session-id=%s %s)\n", *to, unreadID, sid, unreadID)
	}

	var inReplyToPtr *string
	if *inReplyTo != "" {
		// F-39: "last" is a SYMBOLIC reference, resolved here BEFORE the format
		// check (it does not match ^msg-). It becomes the id of the most recent
		// non-ack message we received from --to — the message being replied to —
		// so the agent never transcribes an opaque msg-id (the LL-13
		// hallucination surface). The resolved id exists by construction, so
		// ValidateMessageID and the F-37 existence check below both pass.
		if *inReplyTo == "last" {
			resolved, lerr := lastReceivedFrom(sessionDir, *to, cfg.MaxMessageBytes)
			if lerr != nil {
				if errors.Is(lerr, ErrNoMessageFromPeer) {
					return fmt.Errorf("ask: --in-reply-to=last: no message received from %s to derive the reference from", *to)
				}
				return fmt.Errorf("ask: --in-reply-to=last: %w", lerr)
			}
			*inReplyTo = resolved
		}
		// Validate the FORMAT first: a clean "invalid message id" beats a
		// confusing "not found" warning on a malformed id (sendMessage re-checks
		// the format at the write gateway, but failing here is clearer).
		if err := message.ValidateMessageID(*inReplyTo); err != nil {
			return fmt.Errorf("ask: --in-reply-to: %w", err)
		}
		s := *inReplyTo
		inReplyToPtr = &s
		// F-37: validate the id EXISTS, not just that it is well-formed — a
		// hallucinated id (LL-13) passes the format check but points at no real
		// message. The reply target was RECEIVED, so look in our own inbox/ +
		// processed/ (reusing findMessage from F-48). Default: warn and send —
		// cleanup/auto-gc may have legitimately removed an older message, so
		// rejecting by default would be a false positive. --strict-reply rejects.
		if _, _, ferr := findMessage(sessionDir, s, cfg.MaxMessageBytes); ferr != nil {
			if errors.Is(ferr, ErrMessageNotFound) {
				if *strictReply {
					return fmt.Errorf("ask: --in-reply-to %s not found in your inbox/ or processed/ (drop --strict-reply to send anyway)", s)
				}
				fmt.Fprintf(os.Stderr, "ask: warning: --in-reply-to %s not found in your inbox/ or processed/ — possibly hallucinated or already cleaned up; sending anyway\n", s)
			} else {
				return fmt.Errorf("ask: in-reply-to existence check: %w", ferr)
			}
		}
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
