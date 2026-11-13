package session

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-177: WithReplyTxn is the check of UpdateReplyTxn and CompleteReplyTxn opened to an action of the
// caller's. What it promises is stated here from the outside: the action does not run unless the
// journal on disk is the caller's and the session exists, it runs with the session lock held, and
// what it returns comes back as it is.

// S1: the action does not run when the journal is gone, is another reply's, or the session was
// retired, and the error says which. Nothing on disk changes.
func TestWithReplyTxn_TheActionDoesNotRunWhenTheJournalIsNotTheCallersOrTheSessionIsGone(t *testing.T) {
	t.Run("the journal is gone", func(t *testing.T) {
		mgr, sid := registeredSession(t)
		ran := false

		err := mgr.WithReplyTxn(sid, frozenTxn(), func() error { ran = true; return nil })

		require.ErrorIs(t, err, ErrReplyTxnGone)
		assert.False(t, ran)
		assert.NoFileExists(t, mgr.replyTxnPath(sid), "and the journal was not recreated")
	})
	t.Run("the journal is another reply's", func(t *testing.T) {
		mgr, sid := registeredSession(t)
		other := frozenTxn()
		other.ResponseID = "msg-ffffffffffff"
		other.Anchor = "msg-cccccccccccc"
		other.CloseIDs = []string{"msg-cccccccccccc"}
		require.NoError(t, mgr.WriteReplyTxn(sid, other))
		before, rerr := os.ReadFile(mgr.replyTxnPath(sid))
		require.NoError(t, rerr)
		ran := false

		err := mgr.WithReplyTxn(sid, frozenTxn(), func() error { ran = true; return nil })

		require.ErrorIs(t, err, ErrReplyTxnNotYours)
		assert.False(t, ran)
		after, rerr := os.ReadFile(mgr.replyTxnPath(sid))
		require.NoError(t, rerr)
		assert.Equal(t, string(before), string(after), "the other reply's journal is as it was")
	})
	t.Run("the session was retired", func(t *testing.T) {
		mgr, sid := registeredSession(t)
		require.NoError(t, mgr.WriteReplyTxn(sid, frozenTxn()))
		require.NoError(t, os.RemoveAll(mgr.sessionDir(sid)))
		ran := false

		err := mgr.WithReplyTxn(sid, frozenTxn(), func() error { ran = true; return nil })

		require.ErrorIs(t, err, ErrSessionGone)
		assert.False(t, ran)
		assert.NoDirExists(t, mgr.sessionDir(sid), "and the session was not brought back")
	})
}

// S2: the action runs with the lock held, and the lock is let go of when WithReplyTxn returns.
// "Held" is asked of another process, at the instant of the action.
func TestWithReplyTxn_TheActionRunsWithTheSessionLockHeldAndItIsReleasedAfter(t *testing.T) {
	mgr, sid := registeredSession(t)
	txn := frozenTxn()
	require.NoError(t, mgr.WriteReplyTxn(sid, txn))
	ran := false

	err := mgr.WithReplyTxn(sid, txn, func() error {
		ran = true
		assert.False(t, probeLock(t, mgr.lockBase(sid)), "another process finds the lock held while the action runs")
		return nil
	})

	require.NoError(t, err)
	require.True(t, ran, "premise: the action ran")
	assert.True(t, probeBecomesFree(t, mgr.lockBase(sid)), "and it is let go of once WithReplyTxn returns")
}

// S3: an error of the action comes back as it is, and the journal is left alone: WithReplyTxn does
// not roll back what the action did and does not invent progress it did not report.
func TestWithReplyTxn_AnErrorOfTheActionComesBackUntouchedAndTheJournalIsLeftAlone(t *testing.T) {
	mgr, sid := registeredSession(t)
	txn := frozenTxn()
	require.NoError(t, mgr.WriteReplyTxn(sid, txn))
	before, rerr := os.ReadFile(mgr.replyTxnPath(sid))
	require.NoError(t, rerr)
	boom := errors.New("the move failed")

	err := mgr.WithReplyTxn(sid, txn, func() error { return boom })

	require.ErrorIs(t, err, boom)
	assert.NotErrorIs(t, err, ErrReplyTxnGone)
	assert.NotErrorIs(t, err, ErrReplyTxnNotYours)
	after, rerr := os.ReadFile(mgr.replyTxnPath(sid))
	require.NoError(t, rerr)
	assert.Equal(t, string(before), string(after))
}

// S4: the action may record its progress through UpdateReplyTxn (the lock is re-entrant inside the
// process), and when another process holds the lock the action does not run, after waiting no
// longer than the manager's LockWait.
func TestWithReplyTxn_TheActionCanRecordProgressAndWaitsForTheLockNoLongerThanItIsToldTo(t *testing.T) {
	t.Run("progress", func(t *testing.T) {
		mgr, sid := registeredSession(t)
		txn := frozenTxn()
		require.NoError(t, mgr.WriteReplyTxn(sid, txn))

		err := mgr.WithReplyTxn(sid, txn, func() error {
			txn.State = ReplyTxnSent
			txn.ArchivedIndex = 1
			return mgr.UpdateReplyTxn(sid, txn)
		})

		require.NoError(t, err)
		got, found, rerr := mgr.ReadReplyTxn(sid)
		require.NoError(t, rerr)
		require.True(t, found)
		assert.Equal(t, 1, got.ArchivedIndex)
		assert.Equal(t, ReplyTxnSent, got.State)
	})
	t.Run("the lock is another process's", func(t *testing.T) {
		dataDir, sid := exclSession(t)
		mgr := NewManager(dataDir, time.Second)
		mgr.LockWait = 300 * time.Millisecond // the second argument of NewManager is the heartbeat, not the wait
		txn := frozenTxn()
		require.NoError(t, mgr.WriteReplyTxn(sid, txn))
		holder := startExclHolder(t, dataDir, sid, 0)
		ran := false
		start := time.Now()

		err := mgr.WithReplyTxn(sid, txn, func() error { ran = true; return nil })

		require.ErrorIs(t, err, ErrLockHeld)
		assert.False(t, ran)
		assert.Less(t, time.Since(start), 5*time.Second, "it waited for the manager's LockWait and no longer")
		holder.release(t)
	})
}
