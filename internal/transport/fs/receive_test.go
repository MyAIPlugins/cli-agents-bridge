package fs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

func validReplyMessage(t *testing.T, id, from, to, replyTo string) *message.Message {
	t.Helper()
	r := replyTo
	return &message.Message{
		ID:            id,
		SchemaVersion: message.SchemaVersionV2,
		From:          from,
		FromRole:      "esc",
		FromAgentName: "ESC-test",
		To:            to,
		ToRole:        "val",
		Type:          message.TypeResponse,
		Timestamp:     "2026-05-24T18:00:00Z",
		Status:        message.StatusCompleted,
		Content:       "pong",
		InReplyTo:     &r,
		Metadata: message.Metadata{
			FromProject:     "test",
			ProcessingState: message.StatusCompleted,
		},
	}
}

func TestReceiveReply_FindsExistingReply(t *testing.T) {
	t.Parallel()

	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	origID := "msg-aaaaaaaaaaaa"
	reply := validReplyMessage(t, "msg-bbbbbbbbbbbb", "esc12345", "val12345", origID)
	writeMessage(t, inbox, reply)

	got, err := ReceiveReply(context.Background(), inbox, origID, 2*time.Second, 30*time.Millisecond, 65536)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, reply.ID, got.ID)
	assert.Equal(t, "pong", got.Content)

	// Consumed reply must be deleted (at-most-once consumption)
	_, sterr := os.Stat(filepath.Join(inbox, reply.ID+".json"))
	assert.True(t, os.IsNotExist(sterr), "matched reply must be deleted post-consumption")
}

func TestReceiveReply_FindsReplyArrivingMidWait(t *testing.T) {
	t.Parallel()

	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	origID := "msg-aaaaaaaaaaaa"

	go func() {
		time.Sleep(80 * time.Millisecond)
		reply := validReplyMessage(t, "msg-cccccccccccc", "esc12345", "val12345", origID)
		writeMessage(t, inbox, reply)
	}()

	got, err := ReceiveReply(context.Background(), inbox, origID, 2*time.Second, 30*time.Millisecond, 65536)
	require.NoError(t, err)
	assert.Equal(t, "msg-cccccccccccc", got.ID)
}

func TestReceiveReply_TimeoutReturnsErrTimeout(t *testing.T) {
	t.Parallel()

	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	got, err := ReceiveReply(context.Background(), inbox, "msg-aaaaaaaaaaaa", 80*time.Millisecond, 30*time.Millisecond, 65536)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTimeout)
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "msg-aaaaaaaaaaaa", "error must mention origMsgID for debuggability")
}

func TestReceiveReply_ContextCancel_ReturnsWrappedErr(t *testing.T) {
	t.Parallel()

	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := ReceiveReply(ctx, inbox, "msg-aaaaaaaaaaaa", 10*time.Second, 30*time.Millisecond, 65536)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "context.Canceled must be wrapped in the returned error")
}

func TestReceiveReply_IgnoresNonMatchingMessages(t *testing.T) {
	t.Parallel()

	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	// Plant a non-matching reply (different inReplyTo)
	otherID := "msg-zzzzzzzzzzzz"
	other := validReplyMessage(t, "msg-dddddddddddd", "esc12345", "val12345", otherID)
	writeMessage(t, inbox, other)

	// Wait for our origID — must timeout (other reply doesn't match)
	_, err := ReceiveReply(context.Background(), inbox, "msg-aaaaaaaaaaaa", 80*time.Millisecond, 30*time.Millisecond, 65536)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTimeout)

	// Non-matching reply MUST still be in inbox (not consumed by us)
	_, sterr := os.Stat(filepath.Join(inbox, other.ID+".json"))
	assert.NoError(t, sterr, "non-matching messages must NOT be consumed by ReceiveReply")
}

// TestReceiveReply_IgnoresAckReply is the F-12 §3.3 regression: an auto-ack
// carries inReplyTo=<origID> for correlation, but it is NOT the response a
// receive is waiting for. ReceiveReply must skip type=ack — leaving the ack in
// inbox as the observable F-12 state signal — and keep waiting (timing out here
// since no real reply ever arrives).
func TestReceiveReply_IgnoresAckReply(t *testing.T) {
	t.Parallel()

	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	origID := "msg-aaaaaaaaaaaa"
	ack := validReplyMessage(t, "msg-bbbbbbbbbbbb", "esc12345", "val12345", origID)
	ack.Type = message.TypeAck // an auto-ack, not the awaited response
	ack.Content = "ACK msg-aaaaaaaaaaaa: received"
	writeMessage(t, inbox, ack)

	_, err := ReceiveReply(context.Background(), inbox, origID, 80*time.Millisecond, 30*time.Millisecond, 65536)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTimeout, "ReceiveReply must ignore an ack and keep waiting for the real reply")

	// The ack must remain in inbox (not consumed) as the observable signal.
	_, sterr := os.Stat(filepath.Join(inbox, ack.ID+".json"))
	assert.NoError(t, sterr, "an ack reply must NOT be consumed by ReceiveReply")
}

func TestReceiveReply_MissingInboxDir_TimesOutCleanly(t *testing.T) {
	t.Parallel()

	_, err := ReceiveReply(context.Background(), filepath.Join(t.TempDir(), "no-such-dir"), "msg-aaaaaaaaaaaa", 80*time.Millisecond, 30*time.Millisecond, 65536)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTimeout, "missing inbox dir must produce a clean timeout, not a panic")
}
