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

// lastSentTimeTo returns the Timestamp of the most recent message in outboxDir
// addressed to `to`, or the zero time if none was ever sent there. It is the
// F-34 cutoff: a peer message older than our last send to that peer is treated
// as already-superseded and does not warn. A missing outbox is not an error.
// Unreadable, malformed, .tmp.*, non-.json files and unparseable timestamps are
// skipped.
func lastSentTimeTo(outboxDir, to string, maxContentBytes int) (time.Time, error) {
	entries, err := os.ReadDir(outboxDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return time.Time{}, nil // never sent anything — zero cutoff
		}
		return time.Time{}, fmt.Errorf("unread: read outbox: %w", err)
	}
	var last time.Time
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
		if m.To != to {
			continue
		}
		ts, perr := time.Parse(time.RFC3339Nano, m.Timestamp)
		if perr != nil {
			continue
		}
		if ts.After(last) {
			last = ts
		}
	}
	return last, nil
}

// unreadFromPeer returns the id of the most recent non-ack message in inboxDir
// from `peer` whose Timestamp is strictly after `after`, or "" if none. It is
// the F-34 unread signal: a still-pending (un-consumed) message the peer sent
// AFTER our last message to them — the cross we would make by replying without
// having read it. type=ack is excluded (a delivery receipt, not content, F-12).
// A missing inbox is not an error; unreadable, malformed, .tmp.*, non-.json
// files and unparseable timestamps are skipped.
func unreadFromPeer(inboxDir, peer string, after time.Time, maxContentBytes int) (string, error) {
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil // no inbox yet — nothing unread
		}
		return "", fmt.Errorf("unread: read inbox: %w", err)
	}
	var bestID string
	var bestTime time.Time
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".tmp.") || !strings.HasSuffix(name, ".json") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(inboxDir, name))
		if rerr != nil {
			continue
		}
		m, derr := message.DecodeLenient(data, maxContentBytes)
		if derr != nil {
			continue
		}
		if m.From != peer || m.Type == message.TypeAck {
			continue
		}
		ts, perr := time.Parse(time.RFC3339Nano, m.Timestamp)
		if perr != nil {
			continue
		}
		if !ts.After(after) {
			continue // older than (or equal to) our last send — already superseded
		}
		if bestID == "" || ts.After(bestTime) {
			bestID = m.ID
			bestTime = ts
		}
	}
	return bestID, nil
}
