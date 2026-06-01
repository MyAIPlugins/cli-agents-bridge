package fs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// archivedInProcessed reports whether a message with the given id was archived
// into the processed/ dir that is a sibling of inboxDir (F-30 move target;
// MoveToProcessed prefixes a timestamp, so match on the trailing basename).
func archivedInProcessed(t *testing.T, inboxDir, id string) bool {
	t.Helper()
	processed := filepath.Join(filepath.Dir(inboxDir), "processed")
	entries, err := os.ReadDir(processed)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), id+".json") {
			return true
		}
	}
	return false
}

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

	// F-30: the matched reply is ARCHIVED to processed/, not deleted — gone from
	// inbox but present (timestamp-prefixed) in the sibling processed/ dir, so
	// the receiver keeps a recoverable copy.
	_, sterr := os.Stat(filepath.Join(inbox, reply.ID+".json"))
	assert.True(t, os.IsNotExist(sterr), "matched reply must leave inbox")
	assert.True(t, archivedInProcessed(t, inbox, reply.ID), "matched reply must be archived to processed/")
}

// TestReceiveReply_ArchivedReplyIsNotRematched is the F-30 regression: once a
// reply is consumed (archived to processed/), a second ReceiveReply for the same
// origMsgID must NOT re-match it — it is no longer in inbox.
func TestReceiveReply_ArchivedReplyIsNotRematched(t *testing.T) {
	t.Parallel()

	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	origID := "msg-aaaaaaaaaaaa"
	reply := validReplyMessage(t, "msg-bbbbbbbbbbbb", "esc12345", "val12345", origID)
	writeMessage(t, inbox, reply)

	got, err := ReceiveReply(context.Background(), inbox, origID, 2*time.Second, 30*time.Millisecond, 65536)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, archivedInProcessed(t, inbox, reply.ID), "first receive must archive the reply")

	// Second receive for the same origID must time out — the reply now lives in
	// processed/, not inbox, so it is not re-delivered.
	_, err = ReceiveReply(context.Background(), inbox, origID, 80*time.Millisecond, 30*time.Millisecond, 65536)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTimeout, "an archived reply must not be re-matched by a later receive")
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

// anyMessage builds a valid v2 message of an arbitrary type (no inReplyTo) for
// the ReceiveAny tests.
func anyMessage(t *testing.T, id, msgType string) *message.Message {
	t.Helper()
	return &message.Message{
		ID:            id,
		SchemaVersion: message.SchemaVersionV2,
		From:          "val12345",
		FromRole:      "val",
		FromAgentName: "VAL-test",
		To:            "esc12345",
		ToRole:        "esc",
		Type:          msgType,
		Timestamp:     "2026-05-31T10:00:00Z",
		Status:        message.StatusPending,
		Content:       "x",
		Metadata:      message.Metadata{FromProject: "test", ProcessingState: message.StatusPending},
	}
}

// TestReceiveAny_DrainsBatchExcludingAcks is the F-36 core: --any drains every
// NON-ack pending message in one sweep (archiving to processed/) and LEAVES
// type=ack in inbox as the F-12 signal.
func TestReceiveAny_DrainsBatchExcludingAcks(t *testing.T) {
	t.Parallel()
	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	writeMessage(t, inbox, anyMessage(t, "msg-aaaaaaaaaaaa", message.TypeResponse))
	writeMessage(t, inbox, anyMessage(t, "msg-bbbbbbbbbbbb", message.TypeQuery))
	writeMessage(t, inbox, anyMessage(t, "msg-cccccccccccc", message.TypeAck))

	got, err := ReceiveAny(context.Background(), inbox, 2*time.Second, 30*time.Millisecond, 65536, time.Time{})
	require.NoError(t, err)
	require.Len(t, got, 2, "drains the 2 non-ack messages, not the ack")
	ids := map[string]bool{}
	for _, m := range got {
		ids[m.ID] = true
	}
	assert.True(t, ids["msg-aaaaaaaaaaaa"])
	assert.True(t, ids["msg-bbbbbbbbbbbb"])
	assert.False(t, ids["msg-cccccccccccc"], "ack must not be drained")

	assert.True(t, archivedInProcessed(t, inbox, "msg-aaaaaaaaaaaa"), "non-ack archived to processed/")
	assert.True(t, archivedInProcessed(t, inbox, "msg-bbbbbbbbbbbb"), "non-ack archived to processed/")
	_, ackErr := os.Stat(filepath.Join(inbox, "msg-cccccccccccc.json"))
	assert.NoError(t, ackErr, "ack stays in inbox as the observable F-12 signal")
}

// TestReceiveAny_OnlyAcks_TimesOut: a batch of only acks does NOT wake --any; it
// times out and the acks remain in inbox.
func TestReceiveAny_OnlyAcks_TimesOut(t *testing.T) {
	t.Parallel()
	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))
	writeMessage(t, inbox, anyMessage(t, "msg-aaaaaaaaaaaa", message.TypeAck))

	got, err := ReceiveAny(context.Background(), inbox, 80*time.Millisecond, 30*time.Millisecond, 65536, time.Time{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTimeout)
	assert.Nil(t, got)
	_, ackErr := os.Stat(filepath.Join(inbox, "msg-aaaaaaaaaaaa.json"))
	assert.NoError(t, ackErr, "the ack must stay in inbox after a timeout")
}

func TestReceiveAny_TimeoutEmptyInbox(t *testing.T) {
	t.Parallel()
	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	got, err := ReceiveAny(context.Background(), inbox, 80*time.Millisecond, 30*time.Millisecond, 65536, time.Time{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTimeout)
	assert.Nil(t, got)
}

func TestReceiveAny_BatchArrivingMidWait(t *testing.T) {
	t.Parallel()
	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	go func() {
		time.Sleep(80 * time.Millisecond)
		writeMessage(t, inbox, anyMessage(t, "msg-dddddddddddd", message.TypeResponse))
	}()

	got, err := ReceiveAny(context.Background(), inbox, 2*time.Second, 30*time.Millisecond, 65536, time.Time{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "msg-dddddddddddd", got[0].ID)
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

// anyMessageAt is anyMessage with a caller-controlled Timestamp, for the F-49
// --unseen window tests.
func anyMessageAt(t *testing.T, id, msgType string, ts time.Time) *message.Message {
	t.Helper()
	m := anyMessage(t, id, msgType)
	m.Timestamp = ts.UTC().Format(time.RFC3339Nano)
	return m
}

// TestReceiveAny_Unseen_IgnoresPreExistingWakesOnNew is the F-49 core: with a
// non-zero `since`, the pending already present (Timestamp <= since) is ignored
// and LEFT in the inbox, and only a newer message wakes ReceiveAny.
func TestReceiveAny_Unseen_IgnoresPreExistingWakesOnNew(t *testing.T) {
	t.Parallel()
	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	writeMessage(t, inbox, anyMessageAt(t, "msg-aaaaaaaaaaaa", message.TypeResponse, base))
	writeMessage(t, inbox, anyMessageAt(t, "msg-bbbbbbbbbbbb", message.TypeResponse, base.Add(time.Minute)))

	since := base.Add(30 * time.Second)
	got, err := ReceiveAny(context.Background(), inbox, 2*time.Second, 30*time.Millisecond, 65536, since)
	require.NoError(t, err)
	require.Len(t, got, 1, "only the message newer than `since` wakes")
	assert.Equal(t, "msg-bbbbbbbbbbbb", got[0].ID)

	_, oldErr := os.Stat(filepath.Join(inbox, "msg-aaaaaaaaaaaa.json"))
	assert.NoError(t, oldErr, "--unseen leaves the pre-existing pending in inbox")
	assert.True(t, archivedInProcessed(t, inbox, "msg-bbbbbbbbbbbb"), "the new message is consumed")
}

// TestReceiveAny_Unseen_AllOldTimesOut: a `since` newer than the only pending
// message → timeout, message left in inbox.
func TestReceiveAny_Unseen_AllOldTimesOut(t *testing.T) {
	t.Parallel()
	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	writeMessage(t, inbox, anyMessageAt(t, "msg-aaaaaaaaaaaa", message.TypeResponse, base))

	since := base.Add(time.Hour)
	got, err := ReceiveAny(context.Background(), inbox, 80*time.Millisecond, 30*time.Millisecond, 65536, since)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTimeout)
	assert.Nil(t, got)
	_, oldErr := os.Stat(filepath.Join(inbox, "msg-aaaaaaaaaaaa.json"))
	assert.NoError(t, oldErr, "the pre-existing pending stays in inbox on an --unseen timeout")
}
