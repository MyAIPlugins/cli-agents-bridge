package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// F-177: "takes the lock" is not the property. A tidy that takes the lock, lets go of it and then
// moves the files passes every test that only asks "was the lock taken?", and crosses a retirement
// just as before. The property is that the lock is HELD while the files move, on every move, and
// let go of after. These tests put a real process on the lock at the instant of each move (the
// move is the seam: tidyWith, ackArchive and driveReplyTxnWith take it as a parameter), and ask it
// again after the operation returned.

// requireLockHeldDuringMove returns a move that asks, from another process, whether the lock of the
// session is free at the instant of the move, and fails the test if it is. count says how many
// moves it saw: a test that sees none proves nothing and says so.
func requireLockHeldDuringMove(t *testing.T, data, sid string, count *int) func(src, processedDir string) error {
	t.Helper()
	return func(src, processedDir string) error {
		*count++
		require.False(t, lockIsFreeFor(t, data, sid), "premise of the property: while %s moves, another process finds the lock of %s held", filepath.Base(src), sid)
		return transportfs.MoveToProcessed(src, processedDir)
	}
}

// B1: tidy holds the lock during every move, and lets go of it afterwards.
func TestTidyWith_HoldsTheLockDuringEveryMoveAndLetsGoAfter(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	const sid = "f177bt01"
	mgr := f177Session(t, data, sid)
	const second = "msg-bbbbbbbbbbbb"
	plantMsg(t, data, sid, "inbox", second, "val12345", "VAL-x", message.TypeNotify, "another shown tell")
	_, err := mgr.CommitWakeCursor(sid, []string{second}, time.Now().UTC(), nil, nil)
	require.NoError(t, err)
	moves := 0

	res, err := tidyWith(mgr, sid, f177SessionDir(data, sid), 65536, requireLockHeldDuringMove(t, data, sid, &moves))

	require.NoError(t, err)
	require.Equal(t, 2, moves, "premise: both messages went through the move")
	assert.Equal(t, 2, res.moved)
	assert.True(t, lockBecomesFreeFor(t, data, sid), "and the lock is let go of once tidy returns")
}

// B2: the same for ack, for both of its phases: the check that validates what will move runs under
// the lock too, otherwise what was validated is not what is moved.
func TestAckArchive_HoldsTheLockForTheCheckAndDuringEveryMoveAndLetsGoAfter(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	const sid = "f177bt02"
	f177Session(t, data, sid)
	const second = "msg-bbbbbbbbbbbb"
	plantMsg(t, data, sid, "inbox", second, "val12345", "VAL-x", message.TypeNotify, "another shown tell")
	mgr := newSessionManager(fixtureConfig(t, data))
	moves, checks := 0, 0
	check := func(*message.Message) error {
		checks++
		require.False(t, lockIsFreeFor(t, data, sid), "the check of ack runs with the lock held")
		return nil
	}

	results, ok, err := ackArchive(mgr, sid, f177SessionDir(data, sid), []string{f177Tell, second}, 65536, check, requireLockHeldDuringMove(t, data, sid, &moves))

	require.NoError(t, err)
	require.True(t, ok, "premise: the batch went through: %v", results)
	require.Equal(t, 2, moves, "premise: both ids went through the move")
	require.Equal(t, 2, checks, "premise: and through the check")
	assert.True(t, lockBecomesFreeFor(t, data, sid), "and the lock is let go of once ack returns")
}

// B3: and for the archiving step of `reply`, once per ask of the reply.
func TestDriveReplyTxnWith_HoldsTheLockDuringEveryMoveAndLetsGoAfter(t *testing.T) {
	mgr, cfg, data, j := twoAsks(t)
	txn, err := beginReply(mgr, replySelf, staticSelector(new(bool), j), nil)
	require.NoError(t, err)
	delivered, err := deliverResponse(cfg, mgr, replySelf, txn)
	require.NoError(t, err)
	require.True(t, delivered, "premise: the response was delivered")
	txn.State = session.ReplyTxnSent
	require.NoError(t, mgr.UpdateReplyTxn(replySelf, txn))
	moves := 0

	err = driveReplyTxnWith(mgr, cfg, replySelf, txn, func(string, ...any) {}, requireLockHeldDuringMove(t, data, replySelf, &moves))

	require.NoError(t, err)
	require.Equal(t, 2, moves, "premise: both asks went through the move")
	assert.Equal(t, 1, askArchivedCount(t, data, rcvAskA))
	assert.Equal(t, 1, askArchivedCount(t, data, rcvAskB))
	assert.True(t, lockBecomesFreeFor(t, data, replySelf), "and the lock is let go of once the step is over")
}

// TestHelperProcess_F177Ack is a child: the production core of the ack tool (ackArchive), run as
// another process of the same session, on the ids it is given. It prints what it saw.
func TestHelperProcess_F177Ack(t *testing.T) {
	if os.Getenv(childEnvKind) != "ack" {
		t.Skip("helper process: runs only when re-executed by another test")
	}
	data, sid := os.Getenv(childEnvData), os.Getenv(childEnvSID)
	mgr := session.NewManager(data, time.Second)
	wait, err := strconv.Atoi(os.Getenv(childEnvWaitMs))
	require.NoError(t, err)
	mgr.LockWait = time.Duration(wait) * time.Millisecond
	allow := func(*message.Message) error { return nil }
	results, _, aerr := ackArchive(mgr, sid, filepath.Join(data, "sessions", sid), strings.Split(os.Getenv(childEnvIDs), ","), 65536, allow, transportfs.MoveToProcessed)
	moved := 0
	for _, r := range results {
		if r.Outcome == ackArchived {
			moved++
		}
	}
	fmt.Println(childReport(moved, aerr))
}

const childEnvIDs = "CAB_F177_IDS"

// B4: the window F-178 declared. A delivery looks for the response among what the recipient has
// (inbox and archive), finds it in the inbox, and goes on to write. If the recipient archives it in
// between, the write finds the inbox empty and puts a second copy. Whoever archives for the
// recipient takes the recipient's lock, which the delivery holds from the look to the write: the
// archiver is another process, started at the instant between the two, and finds it held.
func TestDeliverResponse_TheRecipientCannotArchiveBetweenTheLookAndTheWrite(t *testing.T) {
	for _, archiver := range []string{"tidy", "ack"} {
		t.Run(archiver, func(t *testing.T) {
			mgr, cfg, data := newReplyPair(t)
			deliverAndNotify(t, mgr, data, f177Ask, "brief", time.Now().UTC())
			txn, err := beginReply(mgr, replySelf, staticSelector(new(bool), newTxn(replySelf, []string{f177Ask}, "the answer")), nil)
			require.NoError(t, err)
			delivered, err := deliverResponse(cfg, mgr, replySelf, txn)
			require.NoError(t, err)
			require.True(t, delivered, "premise: the first delivery put the response in the recipient's inbox")
			// The recipient has been shown it (a `next` ran), so tidy and ack are allowed to move it.
			_, err = mgr.CommitWakeCursor(replyPeer, []string{txn.ResponseID}, time.Now().UTC(), nil, nil)
			require.NoError(t, err)
			inbox, processed := responseCopies(t, data, txn.ResponseID)
			require.Equal(t, [2]int{1, 0}, [2]int{inbox, processed}, "premise: it is in the inbox, not archived: the look will find nothing")

			var saw childSaw
			ran := false
			created, err := deliverResponseWith(cfg, mgr, replySelf, txn, deliverSeams{afterLook: func() {
				ran = true
				out, code := runF177Child(t, archiver, data, replyPeer,
					childEnvWaitMs+"=300", childEnvIDs+"="+txn.ResponseID)
				require.Zero(t, code, "the archiver child failed: %s", out)
				saw = sawFrom(t, out)
			}})

			require.NoError(t, err)
			require.True(t, ran, "premise: the delivery reached the instant between the look and the write")
			assert.False(t, created, "the response was already there: nothing was written")
			assert.True(t, saw.busy, "the archiver found the recipient's lock held by the delivery: %s", saw.rawOutput)
			assert.Equal(t, 0, saw.moved)
			inbox, processed = responseCopies(t, data, txn.ResponseID)
			assert.Equal(t, 1, inbox+processed, "ONE copy of the response in the recipient's session, wherever it is (inbox %d, processed %d)", inbox, processed)
		})
	}
}
