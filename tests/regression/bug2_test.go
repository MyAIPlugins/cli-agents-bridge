package regression

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// TestBUG2_LateReplyNotLost reproduces BUG-2 (Patil bridge-receive.sh:15-43
// strict-less-than loop exits one tick before deadline-equal arrivals AND
// silently drops replies that land after the deadline — the JSON file stays
// in inbox as "pending" but is never consumed, and the caller never sees it).
//
// cli-agents-bridge fix per PLAN §3.2 deliverable 2:
//   - deadline is the MAX wait, not a hard cut.
//   - ReceiveReply does NOT consume non-matching messages.
//   - A late reply (arriving after the first ReceiveReply timed out) remains
//     in inbox and is found by a subsequent ReceiveReply call.
//
// Test scenario from PLAN §7.2 row BUG-2, compressed to test-scale:
//   - Original: VAL send timeout=10s, ESC reply at 30s. After Patil's bug
//     the reply was permanently lost.
//   - Compressed: first ReceiveReply deadline=80ms, reply arrives at 150ms
//     → first call returns ErrTimeout. Second ReceiveReply called after
//     the reply has landed → finds it and returns it.
//
// The structural property under test is "no silent drop of late replies",
// not the absolute wall-clock timing.
func TestBUG2_LateReplyNotLost(t *testing.T) {
	t.Parallel()

	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	origID := "msg-aaaaaaaaaaaa"

	// Drop the reply mid-wait but AFTER the first ReceiveReply has
	// already returned ErrTimeout. The scheduling tolerance is ~50ms.
	replyArrived := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		writeReplyMessage(t, inbox, "msg-bbbbbbbbbbbb", origID, "pong-late")
		close(replyArrived)
	}()

	// First call: deadline 80ms, reply will arrive at 150ms → ErrTimeout
	_, err := transportfs.ReceiveReply(
		context.Background(),
		inbox,
		origID,
		80*time.Millisecond,
		30*time.Millisecond,
		65536,
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, transportfs.ErrTimeout,
		"first ReceiveReply must time out (reply not yet on disk)")

	// Wait for the goroutine to land the reply
	<-replyArrived

	// Second call: reply now exists, must find it. This is the BUG-2 fix
	// in action — Patil would have silently lost this reply forever.
	got, err := transportfs.ReceiveReply(
		context.Background(),
		inbox,
		origID,
		300*time.Millisecond,
		30*time.Millisecond,
		65536,
	)
	require.NoError(t, err,
		"BUG-2 regression: late reply must NOT be lost — second ReceiveReply must find it on disk")
	require.NotNil(t, got)
	assert.Equal(t, "msg-bbbbbbbbbbbb", got.ID)
	assert.Equal(t, "pong-late", got.Content)
}

// writeReplyMessage is a regression-test helper: emits a valid v2 response
// message into inbox with inReplyTo set. Mirrors the shape produced by a
// real peer's ask+reply roundtrip.
func writeReplyMessage(t *testing.T, inbox, id, replyTo, content string) {
	t.Helper()
	r := replyTo
	m := &message.Message{
		ID:            id,
		SchemaVersion: message.SchemaVersionV2,
		From:          "esc12345",
		FromRole:      "esc",
		FromAgentName: "ESC-bug2",
		To:            "val12345",
		ToRole:        "val",
		Type:          message.TypeResponse,
		Timestamp:     "2026-05-24T18:00:00Z",
		Status:        message.StatusCompleted,
		Content:       content,
		InReplyTo:     &r,
		Metadata: message.Metadata{
			FromProject:     "test-bug2",
			ProcessingState: message.StatusCompleted,
		},
	}
	data, err := message.EncodeStrict(m, 65536)
	require.NoError(t, err)
	require.NoError(t, transportfs.AtomicWriteBytes(filepath.Join(inbox, id+".json"), data, 0o600))
}
