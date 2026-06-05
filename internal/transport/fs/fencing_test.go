package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// inProcessed reports whether a message id landed in the sibling processed/ dir
// (processed files carry a MoveToProcessed timestamp prefix, so match on substring).
func inProcessed(t *testing.T, inbox, id string) bool {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(inbox), "processed"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") && strings.Contains(e.Name(), id) {
			return true
		}
	}
	return false
}

func inInbox(t *testing.T, inbox, id string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(inbox, id+".json"))
	return err == nil
}

// TestDrainInboxOnceOwned_OwnerLost_LeavesInInbox: a fence that always reports
// "not current" consumes nothing — the message stays in inbox/ for the new owner.
func TestDrainInboxOnceOwned_OwnerLost_LeavesInInbox(t *testing.T) {
	t.Parallel()
	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))
	m := validQueryMessage(t, "msg-aaaaaaaaaaaa", "abc123ef", "def456ab")
	writeMessage(t, inbox, m)

	msgs, err := DrainInboxOnceOwned(inbox, 65536, func() bool { return false })
	require.NoError(t, err)
	assert.Empty(t, msgs, "a revoked listener consumes nothing")
	assert.True(t, inInbox(t, inbox, m.ID), "the message stays in inbox for the new owner")
	assert.False(t, inProcessed(t, inbox, m.ID), "and must NOT be moved to processed/")
}

// TestDrainInboxOnceOwned_OwnerHeld_ConsumesAll: a fence that stays current
// behaves exactly like the unfenced DrainInboxOnce.
func TestDrainInboxOnceOwned_OwnerHeld_ConsumesAll(t *testing.T) {
	t.Parallel()
	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))
	writeMessage(t, inbox, validQueryMessage(t, "msg-aaaaaaaaaaaa", "abc123ef", "def456ab"))
	writeMessage(t, inbox, validQueryMessage(t, "msg-bbbbbbbbbbbb", "abc123ef", "def456ab"))

	msgs, err := DrainInboxOnceOwned(inbox, 65536, func() bool { return true })
	require.NoError(t, err)
	assert.Len(t, msgs, 2, "an owner that stays current consumes the whole batch")
}

// TestDrainInboxOnceOwned_ReclaimMidSweep_LeavesRemainderInInbox is the B-2 F3
// CORRECTNESS test: the fence is re-checked immediately before EACH move, so a
// reclaim mid-sweep (owner true for the first move, revoked thereafter) consumes
// the first message but leaves the rest in inbox for the new owner — proving the
// check is per-entry at the latest point, not a single gate at the start.
func TestDrainInboxOnceOwned_ReclaimMidSweep_LeavesRemainderInInbox(t *testing.T) {
	t.Parallel()
	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))
	// ReadDir order is lexical by filename → aaa is processed before bbb.
	writeMessage(t, inbox, validQueryMessage(t, "msg-aaaaaaaaaaaa", "abc123ef", "def456ab"))
	writeMessage(t, inbox, validQueryMessage(t, "msg-bbbbbbbbbbbb", "abc123ef", "def456ab"))

	calls := 0
	ownerOK := func() bool {
		calls++
		return calls == 1 // owner for the first move; reclaimed before the second
	}

	msgs, err := DrainInboxOnceOwned(inbox, 65536, ownerOK)
	require.NoError(t, err)
	require.Len(t, msgs, 1, "only the message moved while still owner is consumed")
	assert.Equal(t, "msg-aaaaaaaaaaaa", msgs[0].ID)

	assert.True(t, inProcessed(t, inbox, "msg-aaaaaaaaaaaa"), "the first (still-owned) message is consumed")
	assert.True(t, inInbox(t, inbox, "msg-bbbbbbbbbbbb"), "the second is LEFT in inbox for the new owner (F3)")
	assert.False(t, inProcessed(t, inbox, "msg-bbbbbbbbbbbb"))
}

// TestPollInboxOwned_OwnerLost_EmitsNothing: the streaming fence leaves a
// revoked listener consuming nothing on the channel either.
func TestPollInboxOwned_OwnerLost_EmitsNothing(t *testing.T) {
	t.Parallel()
	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))
	m := validQueryMessage(t, "msg-aaaaaaaaaaaa", "abc123ef", "def456ab")
	writeMessage(t, inbox, m)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	ch := PollInboxOwned(ctx, inbox, 20*time.Millisecond, 65536, func() bool { return false })

	select {
	case got, ok := <-ch:
		if ok {
			t.Fatalf("a revoked listener must emit nothing, got %v", got)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("channel should close on ctx timeout")
	}
	assert.True(t, inInbox(t, inbox, m.ID), "the message stays in inbox")
}
