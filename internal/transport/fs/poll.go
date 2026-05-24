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
// emitted message is deleted from disk BEFORE being sent on the channel,
// guaranteeing at-most-once delivery to the consumer.
//
// Cleanup policy (Sprint 2 decision per VAL open-question): delete after
// read. Rationale:
//   - audit trail / transcript persistence is a Sprint 3 candidate
//     (PLAN §5 v0.3.0 transcript log feature). MVP keeps inbox lean.
//   - eliminates inbox bloat in long-run sessions where a chat may exchange
//     hundreds of messages.
//   - structurally prevents the Patil-original "double consume" race: a
//     second polling cycle cannot re-emit a message whose file is gone.
//   - move-to-processed/<id>.json was the alternative; rejected because it
//     trades disk pressure for an extra rename(2) per message without
//     enabling any Sprint 2 use case.
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
// removed from disk then sent to out. Errors are swallowed silently: a
// transient read failure should not crash the polling goroutine — the next
// tick will retry. (Persistent failures surface elsewhere via the manifest
// status lifecycle, Sprint 3+.)
func emitInboxOnce(ctx context.Context, out chan<- *message.Message, inboxDir string, maxContentBytes int) {
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// inbox not yet created — fine, manager creates it lazily
			return
		}
		return
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
			// Malformed — leave on disk so a Sprint 3 forensics command
			// (cab-bridge inspect-corrupt) can review it later. Do NOT
			// emit to consumer — at-most-once delivery requires we never
			// hand a partially-decoded payload to the caller.
			continue
		}

		// Delete BEFORE emit. If the consumer is in another process and
		// the channel send blocks, we still guarantee that no second
		// poller sees this file.
		if err := os.Remove(full); err != nil {
			continue
		}

		select {
		case <-ctx.Done():
			return
		case out <- m:
		}
	}
}
