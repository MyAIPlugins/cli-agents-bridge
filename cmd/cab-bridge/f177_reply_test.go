package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/cleanup"
	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// F-177, the archive step of `reply`. The ask a reply closes is moved into processed/ by the
// responder's own session, and until now that move ran before the journal was looked at and
// with no lock of that session taken. These tests build the disk of a reply that has delivered its
// response (journal SENT, response in the peer's inbox) and run the step.

const f177Ask = "msg-aaaaaaaaaaaa"

// f177SentReply plants one open ask and drives the first half of a reply to the point where the
// response is delivered and the journal says SENT, which is where the archiving starts. It
// returns the journal as a driver would hold it.
func f177SentReply(t *testing.T) (*session.Manager, config.Config, string, *session.ReplyTxn) {
	t.Helper()
	mgr, cfg, data := newReplyPair(t)
	deliverAndNotify(t, mgr, data, f177Ask, "brief", time.Now().UTC())
	txn, err := beginReply(mgr, replySelf, staticSelector(new(bool), newTxn(replySelf, []string{f177Ask}, "the answer")), nil)
	require.NoError(t, err)
	delivered, err := deliverResponse(cfg, mgr, replySelf, txn)
	require.NoError(t, err)
	require.True(t, delivered, "premise: the response was delivered")
	txn.State = session.ReplyTxnSent
	require.NoError(t, mgr.UpdateReplyTxn(replySelf, txn))
	return mgr, cfg, data, txn
}

func f177AskInInbox(data string) bool { return f177InInbox(data, replySelf, f177Ask) }

// A7: a driver that is behind (it still holds the SENT journal of a reply that another driver
// finished) must not touch a session that was retired in the meantime. Before the fix it recreated
// processed/ in the folder of a session that no longer exists, and only then found out.
func TestDriveReplyTxn_AStaleDriverDoesNotRecreateARetiredSession(t *testing.T) {
	mgr, cfg, data, a := f177SentReply(t)
	// A second driver of the same journal, taken before the first finishes: it will find a journal
	// that no longer exists, and a session that is gone, by the time it runs.
	b, err := beginReply(mgr, replySelf, staticSelector(new(bool), nil), nil)
	require.NoError(t, err)
	require.NotSame(t, a, b)
	require.Equal(t, session.ReplyTxnSent, b.State, "premise: the second driver holds SENT")
	require.Zero(t, b.ArchivedIndex, "premise: and has archived nothing")

	require.NoError(t, driveReplyTxn(mgr, cfg, replySelf, a, func(string, ...any) {}))
	_, found, err := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, err)
	require.False(t, found, "premise: the first driver completed and removed the journal")

	_, err = cleanup.Run(context.Background(), cleanup.Options{DataDir: data, Scope: cleanup.ScopeMySession, OwnSessionID: replySelf})
	require.NoError(t, err)
	sessionDir := f177SessionDir(data, replySelf)
	require.NoDirExists(t, sessionDir, "premise: the real retirement removed the whole session")

	err = driveReplyTxn(mgr, cfg, replySelf, b, func(string, ...any) {})

	require.ErrorIs(t, err, session.ErrSessionGone)
	assert.NoDirExists(t, sessionDir, "the stale driver did not bring the retired session back")
}

// A8: a lock that cannot be opened is not "busy" and not "gone", and the ask does not move.
func TestDriveReplyTxn_ALockThatCannotBeOpenedMovesNoAsk(t *testing.T) {
	mgr, cfg, data, txn := f177SentReply(t)
	mgr.LockWait = 150 * time.Millisecond
	breakLockFile(t, data, replySelf)

	err := driveReplyTxn(mgr, cfg, replySelf, txn, func(string, ...any) {})

	require.Error(t, err)
	assert.NotErrorIs(t, err, session.ErrLockHeld)
	assert.NotErrorIs(t, err, session.ErrSessionGone)
	assert.True(t, f177AskInInbox(data), "the ask stays in the inbox: the step did not get to run")
	assert.Equal(t, 0, askArchivedCount(t, data, f177Ask))
}

// A9: the lock is another process's. The step says so, moves nothing, leaves the journal at the
// index it had, and the next `reply` (the one a recovery runs) finishes the job after the release.
func TestDriveReplyTxn_WhileAnotherProcessHoldsTheLockNothingMovesAndTheNextOneFinishes(t *testing.T) {
	mgr, cfg, data, txn := f177SentReply(t)
	mgr.LockWait = 150 * time.Millisecond
	h := holdLock(t, data, replySelf)
	requireHeldByAnother(t, data, replySelf)

	err := driveReplyTxn(mgr, cfg, replySelf, txn, func(string, ...any) {})

	require.ErrorIs(t, err, session.ErrLockHeld)
	assert.True(t, f177AskInInbox(data), "the ask did not move while the lock was somebody else's")
	assert.Equal(t, 0, askArchivedCount(t, data, f177Ask))

	h.release()
	// Read back from the disk what a restarted `reply` would find: the journal, as it was left.
	left, found, err := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, err)
	require.True(t, found, "the journal stays: the reply is not done")
	assert.Equal(t, session.ReplyTxnSent, left.State)
	assert.Zero(t, left.ArchivedIndex, "and it did not claim progress that did not happen")

	again, err := beginReply(mgr, replySelf, staticSelector(new(bool), nil), nil)
	require.NoError(t, err)
	require.NoError(t, driveReplyTxn(mgr, cfg, replySelf, again, func(string, ...any) {}))
	assert.False(t, f177AskInInbox(data))
	assert.Equal(t, 1, askArchivedCount(t, data, f177Ask), "exactly one copy of the ask is archived")
	_, found, err = mgr.ReadReplyTxn(replySelf)
	require.NoError(t, err)
	assert.False(t, found, "the journal is gone: the reply is complete")
}
