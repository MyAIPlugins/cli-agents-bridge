package fs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// consumeInboxEntry processes a single directory entry from inboxDir: it skips
// non-message files (.tmp.* atomic-write leftovers, non-.json), reads and
// lenient-decodes the payload, and on success moves it to processedDir BEFORE
// returning it. Returns (msg, true) when a message was consumed (and moved), or
// (nil, false) when the entry was skipped — not a message, unreadable,
// malformed, or move failed. Malformed files are deliberately left on disk for
// forensics.
//
// This is the single source of truth for the inbox consume policy shared by the
// streaming PollInbox path and the synchronous DrainInboxOnce path, so the two
// never drift on what counts as a consumable message.
func consumeInboxEntry(inboxDir, processedDir string, e os.DirEntry, maxContentBytes int) (*message.Message, bool) {
	if e.IsDir() {
		return nil, false
	}
	name := e.Name()
	if strings.HasPrefix(name, ".tmp.") || !strings.HasSuffix(name, ".json") {
		return nil, false
	}

	full := filepath.Join(inboxDir, name)
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, false
	}

	m, err := message.DecodeLenient(data, maxContentBytes)
	if err != nil {
		// Malformed — leave on disk so a forensics command can review it
		// later. Never hand a partially-decoded payload to the caller.
		return nil, false
	}

	if err := MoveToProcessed(full, processedDir); err != nil {
		// EXDEV or permission issue — leave file in inbox; the caller's next
		// sweep retries. Same silent-retry policy as the prior delete path.
		return nil, false
	}
	return m, true
}

// DrainInboxOnce performs ONE synchronous sweep of inboxDir, moving every
// well-formed message to a sibling processed/ directory and returning them in
// os.ReadDir order (lexical by filename — identical ordering to PollInbox).
//
// Unlike PollInbox it spawns no goroutine and never blocks on a channel: every
// consumed message is in the returned slice, so a caller that stops after a
// single sweep (listen --wait-one) can never leave a message consumed-but-unseen.
// This is the property that makes --wait-one lossless: with the channel-based
// poller, a message moved to processed/ just before the consumer exits would be
// gone forever; here a moved message is always in the returned slice.
//
// Returns a nil slice (no error) when the inbox is empty or absent. A genuine
// read error (not ErrNotExist) is surfaced to the caller; skipped entries
// (.tmp.*, non-.json, unreadable, malformed) are left on disk silently.
func DrainInboxOnce(inboxDir string, maxContentBytes int) ([]*message.Message, error) {
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// inbox not yet created — manager creates it lazily, not an error.
			return nil, nil
		}
		return nil, fmt.Errorf("read inbox %q: %w", inboxDir, err)
	}

	processedDir := filepath.Join(filepath.Dir(inboxDir), "processed")

	var msgs []*message.Message
	for _, e := range entries {
		if m, ok := consumeInboxEntry(inboxDir, processedDir, e, maxContentBytes); ok {
			msgs = append(msgs, m)
		}
	}
	return msgs, nil
}
