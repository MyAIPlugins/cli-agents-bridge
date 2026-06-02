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

// plantInboxAt writes a v2 message into dataDir/sessions/<sid>/inbox/ from
// `from`, with a caller-controlled Timestamp, so the F-34 unread/cutoff logic
// can be exercised deterministically (plantMsg hardcodes a fixed timestamp).
func plantInboxAt(t *testing.T, dataDir, sid, id, from, msgType, content string, ts time.Time) {
	t.Helper()
	dir := filepath.Join(dataDir, "sessions", sid, "inbox")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	m := message.Message{
		ID:            id,
		SchemaVersion: message.SchemaVersionV2,
		From:          from,
		FromRole:      "esc",
		FromAgentName: "ESC-y",
		To:            sid,
		ToRole:        "val",
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

func TestLastSentTimeTo_NeverSent(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	outbox := filepath.Join(dataDir, "sessions", "valsess1", "outbox")
	got, err := lastSentTimeTo(outbox, "escsess1", 65536)
	require.NoError(t, err, "a missing outbox is not an error")
	assert.True(t, got.IsZero(), "never sent → zero time")
}

func TestLastSentTimeTo_ReturnsMostRecentToRecipient(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	plantOutboxAt(t, dataDir, "valsess1", "msg-aaaaaaaaaaaa", "escsess1", message.TypeQuery, "first", base)
	plantOutboxAt(t, dataDir, "valsess1", "msg-bbbbbbbbbbbb", "escsess1", message.TypeQuery, "second", base.Add(5*time.Second))
	plantOutboxAt(t, dataDir, "valsess1", "msg-cccccccccccc", "other999", message.TypeQuery, "to other", base.Add(99*time.Second))

	outbox := filepath.Join(dataDir, "sessions", "valsess1", "outbox")
	got, err := lastSentTimeTo(outbox, "escsess1", 65536)
	require.NoError(t, err)
	assert.True(t, got.Equal(base.Add(5*time.Second)), "most recent send to escsess1, ignoring sends to other peers")
}

func TestUnreadFromPeer_PendingAfterCutoff(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	plantInboxAt(t, dataDir, "valsess1", "msg-aaaaaaaaaaaa", "escsess1", message.TypeResponse, "report", base.Add(10*time.Second))

	inbox := filepath.Join(dataDir, "sessions", "valsess1", "inbox")
	got, err := unreadFromPeer(inbox, "escsess1", base, 65536)
	require.NoError(t, err)
	assert.Equal(t, "msg-aaaaaaaaaaaa", got, "a non-ack from the peer after the cutoff is unread")
}

func TestUnreadFromPeer_PendingBeforeCutoffIgnored(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	plantInboxAt(t, dataDir, "valsess1", "msg-aaaaaaaaaaaa", "escsess1", message.TypeResponse, "old", base)

	inbox := filepath.Join(dataDir, "sessions", "valsess1", "inbox")
	got, err := unreadFromPeer(inbox, "escsess1", base.Add(20*time.Second), 65536)
	require.NoError(t, err)
	assert.Equal(t, "", got, "a peer message older than our last send is superseded, not unread")
}

func TestUnreadFromPeer_AckExcluded(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	plantInboxAt(t, dataDir, "valsess1", "msg-aaaaaaaaaaaa", "escsess1", message.TypeAck, "ack", base.Add(10*time.Second))

	inbox := filepath.Join(dataDir, "sessions", "valsess1", "inbox")
	got, err := unreadFromPeer(inbox, "escsess1", base, 65536)
	require.NoError(t, err)
	assert.Equal(t, "", got, "an ack is a delivery receipt, not unread content")
}

func TestUnreadFromPeer_OtherPeerIgnored(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	plantInboxAt(t, dataDir, "valsess1", "msg-aaaaaaaaaaaa", "other999", message.TypeResponse, "from other", base.Add(10*time.Second))

	inbox := filepath.Join(dataDir, "sessions", "valsess1", "inbox")
	got, err := unreadFromPeer(inbox, "escsess1", base, 65536)
	require.NoError(t, err)
	assert.Equal(t, "", got, "only the --to peer counts for this ask")
}

func TestUnreadFromPeer_MissingInbox(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	inbox := filepath.Join(dataDir, "sessions", "valsess1", "inbox")
	got, err := unreadFromPeer(inbox, "escsess1", time.Time{}, 65536)
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

func TestUnreadFromPeer_MostRecentAmongMatches(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	plantInboxAt(t, dataDir, "valsess1", "msg-aaaaaaaaaaaa", "escsess1", message.TypeResponse, "older", base.Add(5*time.Second))
	plantInboxAt(t, dataDir, "valsess1", "msg-bbbbbbbbbbbb", "escsess1", message.TypeResponse, "newer", base.Add(15*time.Second))

	inbox := filepath.Join(dataDir, "sessions", "valsess1", "inbox")
	got, err := unreadFromPeer(inbox, "escsess1", base, 65536)
	require.NoError(t, err)
	assert.Equal(t, "msg-bbbbbbbbbbbb", got, "the most recent unread is returned")
}

// plantProcessedAt writes a v2 message into processed/ using the REAL
// MoveToProcessed naming (<timestamp>-<id>.json, process.go:35) with a
// caller-controlled From/Type/Timestamp, so lastReceivedFrom can be exercised on
// already-consumed messages (the common F-39 case: the brief being replied to is
// in processed/). plantProcessedTimestamped (read_test.go) hardcodes those fields.
func plantProcessedAt(t *testing.T, dataDir, sid, id, from, msgType, content string, ts time.Time) {
	t.Helper()
	dir := filepath.Join(dataDir, "sessions", sid, "processed")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	m := message.Message{
		ID:            id,
		SchemaVersion: message.SchemaVersionV2,
		From:          from,
		FromRole:      "esc",
		FromAgentName: "ESC-y",
		To:            sid,
		ToRole:        "val",
		Type:          msgType,
		Timestamp:     ts.UTC().Format(time.RFC3339Nano),
		Status:        message.StatusPending,
		Content:       content,
		Metadata:      message.Metadata{FromProject: "test", ProcessingState: message.StatusPending},
	}
	data, err := json.Marshal(&m)
	require.NoError(t, err)
	name := ts.UTC().Format("20060102T150405.000000000Z") + "-" + id + ".json"
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o600))
}

func TestLastReceivedFrom_FromInbox(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	plantInboxAt(t, dataDir, "valsess1", "msg-aaaaaaaaaaaa", "escsess1", message.TypeQuery, "brief", base)

	got, err := lastReceivedFrom(filepath.Join(dataDir, "sessions", "valsess1"), "escsess1", 65536)
	require.NoError(t, err)
	assert.Equal(t, "msg-aaaaaaaaaaaa", got, "a still-pending message from the peer resolves")
}

// The common F-39 case: the brief being replied to is already consumed → it
// lives in processed/, named with a timestamp prefix. lastReceivedFrom matches
// on the decoded m.From/m.Timestamp, not the filename.
func TestLastReceivedFrom_FromProcessed(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	plantProcessedAt(t, dataDir, "valsess1", "msg-bbbbbbbbbbbb", "escsess1", message.TypeQuery, "consumed brief", base)

	got, err := lastReceivedFrom(filepath.Join(dataDir, "sessions", "valsess1"), "escsess1", 65536)
	require.NoError(t, err)
	assert.Equal(t, "msg-bbbbbbbbbbbb", got, "an already-consumed message in processed/ resolves")
}

// The most recent wins across BOTH boxes — here the newest is in processed/, so
// the scan must not stop at inbox/.
func TestLastReceivedFrom_MostRecentAcrossBoxes(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	plantInboxAt(t, dataDir, "valsess1", "msg-aaaaaaaaaaaa", "escsess1", message.TypeResponse, "pending older", base.Add(5*time.Second))
	plantProcessedAt(t, dataDir, "valsess1", "msg-bbbbbbbbbbbb", "escsess1", message.TypeQuery, "processed newer", base.Add(15*time.Second))

	got, err := lastReceivedFrom(filepath.Join(dataDir, "sessions", "valsess1"), "escsess1", 65536)
	require.NoError(t, err)
	assert.Equal(t, "msg-bbbbbbbbbbbb", got, "the most recent by timestamp wins, even if it is in processed/")
}

// An ack is a delivery receipt, not content to reply to: when the most recent
// message from the peer is an ack, the previous NON-ack is used.
func TestLastReceivedFrom_SkipsAckUsesPreviousNonAck(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	plantInboxAt(t, dataDir, "valsess1", "msg-aaaaaaaaaaaa", "escsess1", message.TypeQuery, "the real brief", base.Add(5*time.Second))
	plantInboxAt(t, dataDir, "valsess1", "msg-bbbbbbbbbbbb", "escsess1", message.TypeAck, "ACK", base.Add(15*time.Second))

	got, err := lastReceivedFrom(filepath.Join(dataDir, "sessions", "valsess1"), "escsess1", 65536)
	require.NoError(t, err)
	assert.Equal(t, "msg-aaaaaaaaaaaa", got, "the newest is an ack → fall back to the previous non-ack")
}

func TestLastReceivedFrom_NoMessageFromPeer(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	// inbox exists but is empty: the peer never wrote to us.
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "sessions", "valsess1", "inbox"), 0o700))

	_, err := lastReceivedFrom(filepath.Join(dataDir, "sessions", "valsess1"), "escsess1", 65536)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoMessageFromPeer, "an empty inbox → sentinel error")
}

func TestLastReceivedFrom_OtherPeerIgnored(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	plantInboxAt(t, dataDir, "valsess1", "msg-aaaaaaaaaaaa", "other999", message.TypeQuery, "from other", base)

	_, err := lastReceivedFrom(filepath.Join(dataDir, "sessions", "valsess1"), "escsess1", 65536)
	require.ErrorIs(t, err, ErrNoMessageFromPeer, "only the --to peer counts")
}

func TestLastReceivedFrom_MissingBoxes(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	_, err := lastReceivedFrom(filepath.Join(dataDir, "sessions", "valsess1"), "escsess1", 65536)
	require.ErrorIs(t, err, ErrNoMessageFromPeer, "no inbox/ or processed/ → sentinel, not a scan error")
}
