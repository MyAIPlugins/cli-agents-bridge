package fs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// PollInbox launches a goroutine that scans inboxDir at the given interval
// and emits every well-formed Message file to the returned channel. Each
// emitted message is moved to a sibling processed/ directory BEFORE being
// sent on the channel, guaranteeing at-most-once delivery to the consumer
// AND preserving an audit trail of consumed messages.
//
// Cleanup policy (Sprint 3 migration A→B from VAL brief): move to processed
// instead of delete. Rationale:
//   - audit trail: Sprint 4 transcript feature (PLAN §5 v0.3.0) reads
//     processed/ directly with chronological lexical order via RFC3339
//     timestamp prefix. Zero re-sort overhead.
//   - GDPR-1 data minimization: cleanup.sh sweeps processed/ -> archive/
//     pre-delete, with RetentionDays default 7. processed/ stays bounded.
//   - structurally prevents the Patil "double consume" race: file is
//     moved before the next poll cycle (same atomicity as old delete path).
//
// The processedDir is computed as a sibling of inboxDir (inboxDir/../processed).
// Both share the same filesystem in any realistic config, so the rename(2)
// inside MoveToProcessed stays atomic. EXDEV surfaces as an explicit
// error from MoveToProcessed and we leave the source file on disk —
// silently rolling back to delete-and-emit would violate "no fallback
// impliciti" (CLAUDE.md).
//
// The output channel closes when ctx is canceled — but a message already MOVED
// out of inbox/ is never dropped: it is sent with a BLOCKING send (B-2 P1,
// "consumed ⇒ delivered"), NOT a select on ctx.Done(). CONSUMER CONTRACT: after
// canceling ctx the consumer MUST keep reading from the channel until it closes.
// A moved message is in flight and the poller goroutine blocks on that send
// until it is received — abandoning the channel after cancel would strand that
// message in processed/ with no consumer AND leak the poller goroutine. (listen's
// default loop honours this: its `case <-ctx.Done()` is a no-op that loops on,
// so it keeps draining the channel to close.)
//
// Non-JSON entries and files prefixed with ".tmp." (the os.CreateTemp
// convention used by atomic.go) are skipped silently — they belong to
// other writers and are not consumable messages.
func PollInbox(ctx context.Context, inboxDir string, interval time.Duration, maxContentBytes int) <-chan *message.Message {
	return pollInbox(ctx, inboxDir, interval, maxContentBytes, nil) // no ownership fence
}

// PollInboxOwned is PollInbox with a B-2 ownership fence: ownerOK is checked
// immediately before each message is moved to processed/, so a listener whose
// ownership was reclaimed stops consuming (the streaming counterpart of
// DrainInboxOnceOwned). The listen default path uses this; receive never does.
// The same drain-to-close consumer contract as PollInbox applies (a moved
// message is sent blocking, so the consumer must drain to close after cancel).
func PollInboxOwned(ctx context.Context, inboxDir string, interval time.Duration, maxContentBytes int, ownerOK func() bool) <-chan *message.Message {
	return pollInbox(ctx, inboxDir, interval, maxContentBytes, ownerOK)
}

func pollInbox(ctx context.Context, inboxDir string, interval time.Duration, maxContentBytes int, ownerOK func() bool) <-chan *message.Message {
	out := make(chan *message.Message)
	go func() {
		defer close(out)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				emitInboxOnce(out, inboxDir, maxContentBytes, ownerOK)
			}
		}
	}()
	return out
}

// emitInboxOnce performs a single sweep of inboxDir. Decoded messages are
// moved to a sibling processed/ directory then sent to out. Errors are
// swallowed silently: a transient read failure should not crash the
// polling goroutine — the next tick will retry. (Persistent failures
// surface elsewhere via the manifest status lifecycle, Sprint 4+.)
func emitInboxOnce(out chan<- *message.Message, inboxDir string, maxContentBytes int, ownerOK func() bool) {
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// inbox not yet created — fine, manager creates it lazily
			return
		}
		return
	}

	processedDir := filepath.Join(filepath.Dir(inboxDir), "processed")

	for _, e := range entries {
		// consumeInboxEntry moves the message to processed/ BEFORE returning
		// it: if the consumer is in another process and the channel send
		// blocks, no second poller can see this file in inbox/ (at-most-once).
		m, ok := consumeInboxEntry(inboxDir, processedDir, e, maxContentBytes, nil, ownerOK) // nil accept = all; ownerOK fences (nil = unfenced)
		if !ok {
			continue
		}
		// B-2 P1: m has now been MOVED out of inbox/ — it MUST reach the consumer
		// ("consumed ⇒ delivered"). A BLOCKING send, NOT a select on ctx.Done():
		// abandoning the send after the move would strand m in processed/ with no
		// consumer (the F3 loss, in the streaming path) if ctx is canceled between
		// the move and the hand-off. This cannot deadlock: the listen default loop
		// keeps reading from the channel even after ctx.Done() (its `case
		// <-ctx.Done()` is a no-op that loops), and PollInbox closes the channel
		// only after this send returns.
		out <- m
	}
}
