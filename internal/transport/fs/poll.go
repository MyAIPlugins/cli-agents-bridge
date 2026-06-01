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
// The output channel closes when ctx is canceled. The goroutine respects
// ctx during emit as well — a slow consumer cannot pin a canceled poller
// to the channel.
//
// Non-JSON entries and files prefixed with ".tmp." (the os.CreateTemp
// convention used by atomic.go) are skipped silently — they belong to
// other writers and are not consumable messages.
func PollInbox(ctx context.Context, inboxDir string, interval time.Duration, maxContentBytes int) <-chan *message.Message {
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
				emitInboxOnce(ctx, out, inboxDir, maxContentBytes)
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
func emitInboxOnce(ctx context.Context, out chan<- *message.Message, inboxDir string, maxContentBytes int) {
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
		m, ok := consumeInboxEntry(inboxDir, processedDir, e, maxContentBytes, nil) // nil = accept all (unchanged)
		if !ok {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case out <- m:
		}
	}
}
