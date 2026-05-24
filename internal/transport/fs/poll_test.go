package fs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

func validQueryMessage(t *testing.T, id, from, to string) *message.Message {
	t.Helper()
	return &message.Message{
		ID:            id,
		SchemaVersion: message.SchemaVersionV2,
		From:          from,
		FromRole:      "val",
		FromAgentName: "VAL-test",
		To:            to,
		ToRole:        "esc",
		Type:          message.TypeQuery,
		Timestamp:     "2026-05-24T18:00:00Z",
		Status:        message.StatusPending,
		Content:       "ping",
		Metadata: message.Metadata{
			FromProject:     "test",
			ProcessingState: message.StatusPending,
		},
	}
}

func writeMessage(t *testing.T, inboxDir string, m *message.Message) {
	t.Helper()
	require.NoError(t, os.MkdirAll(inboxDir, 0o700))
	data, err := message.EncodeStrict(m, 65536)
	require.NoError(t, err)
	require.NoError(t, AtomicWriteBytes(filepath.Join(inboxDir, m.ID+".json"), data, 0o600))
}

func TestPollInbox_EmitsExistingMessages(t *testing.T) {
	t.Parallel()

	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	want := validQueryMessage(t, "msg-aaaaaaaaaaaa", "abc123ef", "def456ab")
	writeMessage(t, inbox, want)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := PollInbox(ctx, inbox, 30*time.Millisecond, 65536)

	select {
	case got := <-ch:
		require.NotNil(t, got)
		assert.Equal(t, want.ID, got.ID)
		assert.Equal(t, want.Content, got.Content)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("PollInbox did not emit existing message within 500ms")
	}

	// File must have been moved from inbox/ to sibling processed/
	// (Sprint 3 inbox policy A→B: move-to-processed instead of delete).
	_, err := os.Stat(filepath.Join(inbox, want.ID+".json"))
	assert.True(t, os.IsNotExist(err), "PollInbox must remove consumed messages from inbox/")

	processedDir := filepath.Join(filepath.Dir(inbox), "processed")
	processedEntries, err := os.ReadDir(processedDir)
	require.NoError(t, err, "processed/ must exist after consumption")
	require.Len(t, processedEntries, 1, "processed/ must contain exactly the consumed message")
	assert.Contains(t, processedEntries[0].Name(), want.ID,
		"processed file name must reference original message ID")
}

func TestPollInbox_EmitsMessagesArrivingMidLoop(t *testing.T) {
	t.Parallel()

	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := PollInbox(ctx, inbox, 30*time.Millisecond, 65536)

	// Drop a message after the goroutine has started
	time.Sleep(50 * time.Millisecond)
	want := validQueryMessage(t, "msg-bbbbbbbbbbbb", "abc123ef", "def456ab")
	writeMessage(t, inbox, want)

	select {
	case got := <-ch:
		require.NotNil(t, got)
		assert.Equal(t, want.ID, got.ID)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("PollInbox did not emit mid-loop message within 500ms")
	}
}

func TestPollInbox_ClosesChannelOnContextCancel(t *testing.T) {
	t.Parallel()

	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	ctx, cancel := context.WithCancel(context.Background())
	ch := PollInbox(ctx, inbox, 30*time.Millisecond, 65536)

	cancel()

	select {
	case _, ok := <-ch:
		assert.False(t, ok, "channel must be closed after ctx cancel")
	case <-time.After(300 * time.Millisecond):
		t.Fatal("channel did not close within 300ms of cancel")
	}
}

func TestPollInbox_SkipsMalformedAndTempFiles(t *testing.T) {
	t.Parallel()

	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	// Garbage JSON
	require.NoError(t, os.WriteFile(filepath.Join(inbox, "garbage.json"), []byte("{not valid"), 0o600))
	// Atomic write temp leftover
	require.NoError(t, os.WriteFile(filepath.Join(inbox, ".tmp.abcd"), []byte("{}"), 0o600))
	// Non-json file
	require.NoError(t, os.WriteFile(filepath.Join(inbox, "readme.txt"), []byte("hi"), 0o600))
	// Valid message
	want := validQueryMessage(t, "msg-cccccccccccc", "abc123ef", "def456ab")
	writeMessage(t, inbox, want)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := PollInbox(ctx, inbox, 30*time.Millisecond, 65536)

	select {
	case got := <-ch:
		assert.Equal(t, want.ID, got.ID, "PollInbox must skip garbage/tmp/non-json and emit only valid messages")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("PollInbox did not emit valid message within 500ms")
	}

	// Malformed file MUST remain on disk for forensics
	_, err := os.Stat(filepath.Join(inbox, "garbage.json"))
	assert.NoError(t, err, "PollInbox must leave malformed messages on disk")
}

func TestPollInbox_MissingInboxDir_DoesNotPanic(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Inbox path that doesn't exist — goroutine must keep ticking gracefully
	ch := PollInbox(ctx, filepath.Join(t.TempDir(), "nonexistent-inbox"), 30*time.Millisecond, 65536)

	time.Sleep(100 * time.Millisecond)
	cancel()

	// Drain (should close without emitting)
	for range ch {
	}
}
