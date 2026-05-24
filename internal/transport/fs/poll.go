package fs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
			// Malformed — leave on disk so a forensics command can review
			// it later. Do NOT emit to consumer — at-most-once delivery
			// requires we never hand a partially-decoded payload to the
			// caller.
			continue
		}

		// Move BEFORE emit. If the consumer is in another process and
		// the channel send blocks, we still guarantee that no second
		// poller sees this file in inbox/.
		if err := MoveToProcessed(full, processedDir); err != nil {
			// EXDEV or permission issue — leave file in inbox, the next
			// poll cycle will retry. Surfacing the error to the caller
			// would require a richer channel type; for Sprint 3 silent
			// retry matches the prior delete-error behavior.
			continue
		}

		select {
		case <-ctx.Done():
			return
		case out <- m:
		}
	}
}
