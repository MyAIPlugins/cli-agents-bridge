package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// plantOutboxAt writes a v2 message into dataDir/sessions/<sid>/outbox/ with a
// caller-controlled id, to/type/content and Timestamp, so the dedup window logic
// can be exercised deterministically (plantMsg hardcodes a fixed timestamp).
func plantOutboxAt(t *testing.T, dataDir, sid, id, to, msgType, content string, ts time.Time) {
	t.Helper()
	dir := filepath.Join(dataDir, "sessions", sid, "outbox")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	m := message.Message{
		ID:            id,
		SchemaVersion: message.SchemaVersionV2,
		From:          sid,
		FromRole:      "val",
		FromAgentName: "VAL-x",
		To:            to,
		ToRole:        "esc",
		Type:          msgType,
		Timestamp:     ts.UTC().Format(time.RFC3339Nano),
		Status:        message.StatusPending,
		Content:       content,
		Metadata:      message.Metadata{FromProject: "test", ProcessingState: message.StatusPending},
	}
	data, err := json.Marshal(&m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".json"), data, 0o600))
}

func TestFindRecentDuplicate_MatchWithinWindow(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sid := "dedupw01"
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	plantOutboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "esc12345", message.TypeQuery, "same body", base)

	outbox := filepath.Join(dataDir, "sessions", sid, "outbox")
	dup, err := findRecentDuplicate(outbox, "esc12345", message.TypeQuery, "same body", 10, 65536, base.Add(5*time.Second))
	require.NoError(t, err)
	assert.Equal(t, "msg-aaaaaaaaaaaa", dup, "an identical send 5s ago is a duplicate within a 10s window")
}

func TestFindRecentDuplicate_OutsideWindow(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sid := "dedupw02"
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	plantOutboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "esc12345", message.TypeQuery, "same body", base)

	outbox := filepath.Join(dataDir, "sessions", sid, "outbox")
	dup, err := findRecentDuplicate(outbox, "esc12345", message.TypeQuery, "same body", 10, 65536, base.Add(20*time.Second))
	require.NoError(t, err)
	assert.Equal(t, "", dup, "an identical send 20s ago is outside a 10s window")
}

func TestFindRecentDuplicate_DifferentToTypeContent(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sid := "dedupw03"
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	plantOutboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "esc12345", message.TypeQuery, "body", base)
	outbox := filepath.Join(dataDir, "sessions", sid, "outbox")
	now := base.Add(2 * time.Second)

	d1, _ := findRecentDuplicate(outbox, "other999", message.TypeQuery, "body", 10, 65536, now)
	assert.Equal(t, "", d1, "different to → no match")
	d2, _ := findRecentDuplicate(outbox, "esc12345", message.TypeNotify, "body", 10, 65536, now)
	assert.Equal(t, "", d2, "different type → no match")
	d3, _ := findRecentDuplicate(outbox, "esc12345", message.TypeQuery, "other body", 10, 65536, now)
	assert.Equal(t, "", d3, "different content → no match")
}

func TestFindRecentDuplicate_MissingOutbox(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	outbox := filepath.Join(dataDir, "sessions", "nope1234", "outbox")
	dup, err := findRecentDuplicate(outbox, "esc12345", message.TypeQuery, "body", 10, 65536, time.Now())
	require.NoError(t, err, "a missing outbox is not an error")
	assert.Equal(t, "", dup)
}

func TestFindRecentDuplicate_MostRecentAmongMatches(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sid := "dedupw04"
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	plantOutboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "esc12345", message.TypeQuery, "body", base)
	plantOutboxAt(t, dataDir, sid, "msg-bbbbbbbbbbbb", "esc12345", message.TypeQuery, "body", base.Add(3*time.Second))

	outbox := filepath.Join(dataDir, "sessions", sid, "outbox")
	dup, err := findRecentDuplicate(outbox, "esc12345", message.TypeQuery, "body", 10, 65536, base.Add(5*time.Second))
	require.NoError(t, err)
	assert.Equal(t, "msg-bbbbbbbbbbbb", dup, "the most recent matching send is returned")
}

func TestFindRecentDuplicate_MalformedTimestampSkipped(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sid := "dedupw05"
	dir := filepath.Join(dataDir, "sessions", sid, "outbox")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	m := message.Message{
		ID: "msg-aaaaaaaaaaaa", SchemaVersion: message.SchemaVersionV2,
		From: sid, FromRole: "val", FromAgentName: "VAL-x",
		To: "esc12345", ToRole: "esc", Type: message.TypeQuery,
		Timestamp: "not-a-timestamp", Status: message.StatusPending, Content: "body",
		Metadata: message.Metadata{FromProject: "test", ProcessingState: message.StatusPending},
	}
	data, err := json.Marshal(&m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "msg-aaaaaaaaaaaa.json"), data, 0o600))

	outbox := filepath.Join(dataDir, "sessions", sid, "outbox")
	dup, derr := findRecentDuplicate(outbox, "esc12345", message.TypeQuery, "body", 10, 65536, time.Now())
	require.NoError(t, derr)
	assert.Equal(t, "", dup, "a message with an unparseable timestamp is not a usable duplicate signal")
}
