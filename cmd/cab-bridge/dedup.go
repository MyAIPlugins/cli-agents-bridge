package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// findRecentDuplicate scans the sender's outbox for a message with the same
// (to, type, content) sent within the last windowSeconds (measured from each
// message's own Timestamp) and returns the id of the most recent such match, or
// "" if none. It is the F-43 guard against a degraded agent re-invoking `ask`
// before the first send's stdout returns: a near-identical resend within the
// window is flagged so the caller can warn or skip it.
//
// Content is compared by direct string equality, not a hash: the match is
// in-memory over an outbox bounded by MaxInboxSize (~100) with messages capped
// at MaxMessageBytes (64 KB), so equality is exact, dependency-free and
// short-circuits on length — a content hash would buy nothing at this scale.
//
// now is injected for testability. A missing outbox is not an error ("", nil).
// Unreadable, malformed, .tmp.* or non-.json files are skipped, as is any file
// whose Timestamp does not parse — none is a usable duplicate signal.
func findRecentDuplicate(outboxDir, to, msgType, content string, windowSeconds, maxContentBytes int, now time.Time) (string, error) {
	entries, err := os.ReadDir(outboxDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil // no outbox yet — nothing sent, no duplicate
		}
		return "", fmt.Errorf("dedup: read outbox: %w", err)
	}

	cutoff := now.Add(-time.Duration(windowSeconds) * time.Second)
	var bestID string
	var bestTime time.Time
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".tmp.") || !strings.HasSuffix(name, ".json") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(outboxDir, name))
		if rerr != nil {
			continue
		}
		m, derr := message.DecodeLenient(data, maxContentBytes)
		if derr != nil {
			continue
		}
		if m.To != to || m.Type != msgType || m.Content != content {
			continue
		}
		ts, perr := time.Parse(time.RFC3339Nano, m.Timestamp)
		if perr != nil {
			continue
		}
		if ts.Before(cutoff) {
			continue // identical but older than the window — not a recent duplicate
		}
		if bestID == "" || ts.After(bestTime) {
			bestID = m.ID
			bestTime = ts
		}
	}
	return bestID, nil
}
