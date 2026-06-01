package fs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// ErrTimeout is returned by ReceiveReply when deadline expires before a
// matching reply appears in the inbox. cmd/cab-bridge maps this sentinel
// to exit code 124 — the conventional Unix timeout exit code from
// coreutils timeout(1) — and writes the error message to stderr (BUG-7
// fix: Patil's bridge-receive.sh wrote "No response received after Ns" to
// stdout, polluting any process substitution capture).
var ErrTimeout = errors.New("receive timeout: no reply within deadline")

// ReceiveReply waits up to deadline for a message in inboxDir whose
// inReplyTo equals origMsgID. Polls at pollInterval.
//
// BUG-2 fix vs Patil bridge-receive.sh:15-43:
//   - deadline is the MAX wait, not a hard cut that loses late-arriving
//     replies. If the matching reply lands after deadline, it stays in
//     inboxDir (we do not consume non-matching messages here). A
//     subsequent ReceiveReply call (or list-peers inspection) finds it.
//   - Patil's strict < loop comparison could exit one tick before the
//     final scan. ReceiveReply does an initial scan BEFORE the first
//     ticker fire and a final time.Now check before each scan, so a
//     deadline-equal arrival is always seen.
//   - Non-matching messages in inbox are NOT consumed by ReceiveReply
//     (PollInbox owns the broader fan-out path). We only delete the
//     specific reply we matched.
//
// Returns (msg, nil) on match, (nil, ErrTimeout) on deadline, or
// (nil, ctx.Err()-wrapped) on cancellation.
func ReceiveReply(
	ctx context.Context,
	inboxDir, origMsgID string,
	deadline, pollInterval time.Duration,
	maxContentBytes int,
) (*message.Message, error) {
	deadlineTime := time.Now().Add(deadline)

	// Initial scan: the reply may already be present when ReceiveReply is
	// called (sender wrote it during the gap between send and receive).
	if m, err := scanForReply(inboxDir, origMsgID, maxContentBytes); err != nil {
		return nil, err
	} else if m != nil {
		return m, nil
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("receive canceled: %w", ctx.Err())
		case <-ticker.C:
			// Check deadline BEFORE the scan so a tick that lands right at
			// deadline still gets the scan chance (matches the BUG-2
			// fix-intent: never lose a reply that exists on disk).
			now := time.Now()

			m, err := scanForReply(inboxDir, origMsgID, maxContentBytes)
			if err != nil {
				return nil, err
			}
			if m != nil {
				return m, nil
			}

			if now.After(deadlineTime) {
				return nil, fmt.Errorf("%w: origMsgID=%s waited=%v",
					ErrTimeout, origMsgID, deadline)
			}
		}
	}
}

// scanForReply does a single pass of inboxDir looking for a message whose
// inReplyTo equals origMsgID. On match it ARCHIVES the file to the sibling
// processed/ dir (F-30) and returns the message — symmetric with
// DrainInboxOnce/consumeInboxEntry, so a background receive whose caller missed
// the stdout can recover an already-consumed reply from its OWN processed/ dir
// instead of digging it out of the sender's outbox. Previously it deleted the
// file (os.Remove), which left a consumed reply nowhere on the receiver's side.
//
// At-most-once is preserved across concurrent callers: if the archive move loses
// a race (source already gone, ErrNotExist) the reply was consumed by another
// caller, so this one keeps scanning. Any OTHER move failure (EXDEV, permission)
// leaves the file in inbox AND still returns the message — the receive caller is
// blocking on exactly this reply and must not lose it to an archive error.
//
// Returns (nil, nil) when no match yet — the polling loop in ReceiveReply
// handles retry. Returns non-nil error only for unexpected I/O failures
// that the caller should propagate (missing inbox dir is NOT one of these:
// it just means no messages yet).
func scanForReply(inboxDir, origMsgID string, maxContentBytes int) (*message.Message, error) {
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan inbox %q: %w", inboxDir, err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".tmp.") || !strings.HasSuffix(name, ".json") {
			continue
		}

		full := filepath.Join(inboxDir, name)
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}

		m, err := message.DecodeLenient(data, maxContentBytes)
		if err != nil {
			continue
		}

		if m.InReplyTo == nil || *m.InReplyTo != origMsgID {
			continue
		}

		// F-12 §3.3: an auto-ack carries inReplyTo=<origMsgID> for correlation,
		// but it is NOT the response a receive is waiting for. Skip type=ack so
		// the ack stays in inbox as the observable F-12 state signal and receive
		// keeps waiting for the real reply. Any other reply type still matches
		// (e.g. a legitimate notify reply), per the agreed semantics.
		if m.Type == message.TypeAck {
			continue
		}

		// Match. Archive to processed/ (F-30) instead of deleting, so the
		// receiver keeps a recoverable copy. processedDir is the inbox sibling,
		// derived exactly as DrainInboxOnce does.
		processedDir := filepath.Join(filepath.Dir(inboxDir), "processed")
		if err := MoveToProcessed(full, processedDir); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// Lost a race: a concurrent ReceiveReply already consumed (moved)
				// this reply. Preserve at-most-once — do NOT hand it out twice;
				// keep scanning for our own match.
				continue
			}
			// EXDEV/permission: the file is still in inbox and the caller is
			// blocking on exactly this reply — never lose it. Return it anyway
			// and log; a later scan or `inbox --list` can still surface it.
			fmt.Fprintf(os.Stderr, "cab-bridge: receive matched reply %q but archiving to processed/ failed (non-fatal): %v\n", full, err)
		}
		return m, nil
	}
	return nil, nil
}

// ReceiveAny waits up to deadline for the first non-empty batch of NON-ack
// messages in inboxDir, draining and archiving them to processed/ (F-30) in one
// sweep, and returns the batch. It is the id-less wake primitive behind
// `receive --any` (F-36): an orchestrator wakes on "anything arrived" WITHOUT
// fabricating a msg-id to wait on (the LL-13 hallucination root).
//
// type=ack messages are NEVER drained — they are left in inbox as the observable
// F-12 delivery signal, exactly as scanForReply skips them — so a batch of only
// acks does NOT wake ReceiveAny (it times out with the acks still in inbox).
//
// Unlike listen, receive does NOT AdoptPID: this is a one-shot wake, not a
// long-running listener (the delta that keeps receive and listen distinct).
// Polling mirrors ReceiveReply (initial scan + ticker + deadline-before-scan).
// Returns (batch, nil) on the first non-empty sweep, (nil, ErrTimeout) on
// deadline, or (nil, ctx-wrapped) on cancellation.
func ReceiveAny(
	ctx context.Context,
	inboxDir string,
	deadline, pollInterval time.Duration,
	maxContentBytes int,
) ([]*message.Message, error) {
	notAck := func(m *message.Message) bool { return m.Type != message.TypeAck }
	deadlineTime := time.Now().Add(deadline)

	// Initial sweep: a batch may already be waiting when ReceiveAny is called.
	if msgs, err := drainInbox(inboxDir, maxContentBytes, notAck); err != nil {
		return nil, err
	} else if len(msgs) > 0 {
		return msgs, nil
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("receive canceled: %w", ctx.Err())
		case <-ticker.C:
			// Deadline checked BEFORE the sweep so a tick at deadline still scans
			// (same BUG-2 fix-intent as ReceiveReply: never lose a batch on disk).
			now := time.Now()

			msgs, err := drainInbox(inboxDir, maxContentBytes, notAck)
			if err != nil {
				return nil, err
			}
			if len(msgs) > 0 {
				return msgs, nil
			}

			if now.After(deadlineTime) {
				return nil, fmt.Errorf("%w: waited=%v (--any)", ErrTimeout, deadline)
			}
		}
	}
}
