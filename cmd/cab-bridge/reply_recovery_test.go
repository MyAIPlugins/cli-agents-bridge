package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// F-176, the recovery half: a `reply` interrupted at any point is completed by the next one.
//
// What these tests prove is COMPLETION, and they are named for it. Whether a recovery delivers a
// second copy is a separate question, answered by the last test of this file and by
// reply_delivery_archived_test.go (F-178): the delivery looks for the response in the recipient's
// inbox and in what the recipient has archived, so a recovery after the recipient archived it
// delivers nothing. What is NOT excluded is a recipient that archives at the very instant of the
// write: tidy and ack move files without the session lock (F-177).

const (
	rcvAskA    = "msg-aaaaaaaaaaaa"
	rcvAskB    = "msg-bbbbbbbbbbbb"
	rcvFrozen  = "the answer as it was frozen"
	rcvAnother = "a different text typed after the restart"
)

// responseCopies counts every copy of a response id in the recipient's session: the inbox and
// processed/. "One response in the inbox" would pass a double delivery whose first copy was
// archived, which is the very case it has to tell apart.
func responseCopies(t *testing.T, data, responseID string) (inbox, processed int) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(data, "sessions", replyPeer, "inbox", responseID+".json")); err == nil {
		inbox = 1
	}
	entries, err := os.ReadDir(filepath.Join(data, "sessions", replyPeer, "processed"))
	if os.IsNotExist(err) {
		return inbox, 0
	}
	require.NoError(t, err)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "-"+responseID+".json") {
			processed++
		}
	}
	return inbox, processed
}

// askArchivedCount counts the copies of an ask in the responder's own processed/.
func askArchivedCount(t *testing.T, data, askID string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(data, "sessions", replySelf, "processed"))
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "-"+askID+".json") {
			n++
		}
	}
	return n
}

func archiveAsk(t *testing.T, data, askID string) {
	t.Helper()
	require.NoError(t, transportfs.MoveToProcessed(
		filepath.Join(data, "sessions", replySelf, "inbox", askID+".json"),
		filepath.Join(data, "sessions", replySelf, "processed")))
}

// twoAsks plants two open asks from the peer and the journal that closes both.
func twoAsks(t *testing.T) (*session.Manager, config.Config, string, *session.ReplyTxn) {
	t.Helper()
	mgr, cfg, data := newReplyPair(t)
	j := newTxn(replySelf, []string{rcvAskA, rcvAskB}, rcvFrozen)
	deliverAndNotify(t, mgr, data, rcvAskA, "first ask", j.Timestamp)
	deliverAndNotify(t, mgr, data, rcvAskB, "second ask", j.Timestamp)
	return mgr, cfg, data, j
}

// TestReply_AnInterruptedReplyCompletesAtAnyPoint builds the disk as a crash leaves it at each
// point of the transaction, and runs the next `reply` the way the CLI does: beginReply with the
// real selector (the journal outranks what is typed) and then driveReplyTxn.
func TestReply_AnInterruptedReplyCompletesAtAnyPoint(t *testing.T) {
	type point struct {
		name  string
		build func(t *testing.T, mgr *session.Manager, cfg config.Config, data string, j *session.ReplyTxn)
	}
	deliver := func(t *testing.T, mgr *session.Manager, cfg config.Config, j *session.ReplyTxn) {
		t.Helper()
		delivered, err := deliverResponse(cfg, mgr, replySelf, j)
		require.NoError(t, err)
		require.True(t, delivered, "premise: the response was delivered by the first attempt")
	}
	write := func(t *testing.T, mgr *session.Manager, j *session.ReplyTxn) {
		t.Helper()
		require.NoError(t, mgr.WriteReplyTxn(replySelf, j))
	}
	points := []point{
		{"P0 pending, nothing delivered", func(t *testing.T, mgr *session.Manager, cfg config.Config, data string, j *session.ReplyTxn) {
			write(t, mgr, j)
		}},
		{"P1 delivered, still pending", func(t *testing.T, mgr *session.Manager, cfg config.Config, data string, j *session.ReplyTxn) {
			write(t, mgr, j)
			deliver(t, mgr, cfg, j)
		}},
		{"P2 sent, nothing archived", func(t *testing.T, mgr *session.Manager, cfg config.Config, data string, j *session.ReplyTxn) {
			write(t, mgr, j)
			deliver(t, mgr, cfg, j)
			j.State = session.ReplyTxnSent
			write(t, mgr, j)
		}},
		{"P3 sent, first ask archived and recorded", func(t *testing.T, mgr *session.Manager, cfg config.Config, data string, j *session.ReplyTxn) {
			write(t, mgr, j)
			deliver(t, mgr, cfg, j)
			j.State = session.ReplyTxnSent
			archiveAsk(t, data, rcvAskA)
			j.ArchivedIndex = 1
			write(t, mgr, j)
		}},
		{"P4 sent, first ask archived but the index was not written", func(t *testing.T, mgr *session.Manager, cfg config.Config, data string, j *session.ReplyTxn) {
			write(t, mgr, j)
			deliver(t, mgr, cfg, j)
			j.State = session.ReplyTxnSent
			write(t, mgr, j)
			archiveAsk(t, data, rcvAskA)
		}},
		{"P5 every ask archived, journal still there", func(t *testing.T, mgr *session.Manager, cfg config.Config, data string, j *session.ReplyTxn) {
			write(t, mgr, j)
			deliver(t, mgr, cfg, j)
			j.State = session.ReplyTxnSent
			archiveAsk(t, data, rcvAskA)
			archiveAsk(t, data, rcvAskB)
			j.ArchivedIndex = 2
			write(t, mgr, j)
		}},
	}

	for _, p := range points {
		t.Run(p.name, func(t *testing.T) {
			mgr, cfg, data, j := twoAsks(t)
			p.build(t, mgr, cfg, data, j)

			// The next reply of the session. What is typed is not what was frozen.
			next, err := beginReply(mgr, replySelf, cliReplySelector(mgr, cfg, replySelf, []string{rcvAnother}, nil), nil)
			require.NoError(t, err)
			require.Equal(t, rcvFrozen, next.Content, "premise: the journal outranked what was typed")
			require.NoError(t, driveReplyTxn(mgr, cfg, replySelf, next, func(string, ...any) {}))

			_, found, rerr := mgr.ReadReplyTxn(replySelf)
			require.NoError(t, rerr)
			assert.False(t, found, "the journal is still on disk")
			assert.Equal(t, 1, askArchivedCount(t, data, rcvAskA), "ask A must be archived once")
			assert.Equal(t, 1, askArchivedCount(t, data, rcvAskB), "ask B must be archived once")
			inbox, processed := responseCopies(t, data, j.ResponseID)
			assert.Equal(t, 1, inbox+processed, "one copy of the response in all (inbox %d, processed %d): the recipient has not archived it, so a recovery must not deliver it again", inbox, processed)
			assert.Equal(t, rcvFrozen, readDelivered(t, data, j.ResponseID).Content, "the response that was delivered has the first attempt's text")
		})
	}
}

// TestReply_TwoDriversOfTheSameTransactionBothFinish is the concurrency the journal still
// allows: B holds a copy that is behind the disk, A advances an ask, and B then runs. Progress
// that goes backwards is walked again without harm, so the transaction ends once and without
// an error.
func TestReply_TwoDriversOfTheSameTransactionBothFinish(t *testing.T) {
	mgr, cfg, data, j := twoAsks(t)
	require.NoError(t, mgr.WriteReplyTxn(replySelf, j))
	unused := false
	a, err := beginReply(mgr, replySelf, staticSelector(&unused, nil), nil)
	require.NoError(t, err)
	b, err := beginReply(mgr, replySelf, staticSelector(&unused, nil), nil)
	require.NoError(t, err)
	require.False(t, unused)
	require.NotSame(t, a, b)

	// A delivers and archives the first ask, and stops there.
	delivered, derr := deliverResponse(cfg, mgr, replySelf, a)
	require.NoError(t, derr)
	require.True(t, delivered)
	a.State = session.ReplyTxnSent
	archiveAsk(t, data, rcvAskA)
	a.ArchivedIndex = 1
	require.NoError(t, mgr.WriteReplyTxn(replySelf, a))
	require.Equal(t, session.ReplyTxnPending, b.State, "premise: B is behind the disk")

	require.NoError(t, driveReplyTxn(mgr, cfg, replySelf, b, func(string, ...any) {}))

	_, found, rerr := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, rerr)
	assert.False(t, found)
	assert.Equal(t, 1, askArchivedCount(t, data, rcvAskA))
	assert.Equal(t, 1, askArchivedCount(t, data, rcvAskB))
	inbox, processed := responseCopies(t, data, j.ResponseID)
	assert.Equal(t, 1, inbox+processed)
}

// TestDriveReplyTxn_AnAskThatIsAlreadyOutOfTheInboxIsNotAFailure is the state a crash leaves
// between the move of an ask and the write of the archived index: the ask is in processed/,
// the journal still says it has to archive it. The step wants an end state (the ask is not in
// the inbox) and it is there, so the reply has to finish. The loop meant to tolerate exactly
// this with os.IsNotExist, which does not see through the error MoveToProcessed wraps.
func TestDriveReplyTxn_AnAskThatIsAlreadyOutOfTheInboxIsNotAFailure(t *testing.T) {
	mgr, cfg, data := newReplyPair(t)
	j := newTxn(replySelf, []string{rcvAskA}, "J1 payload")
	j.State = session.ReplyTxnSent
	require.NoError(t, mgr.WriteReplyTxn(replySelf, j))
	_, err := os.Stat(filepath.Join(data, "sessions", replySelf, "inbox", rcvAskA+".json"))
	require.True(t, os.IsNotExist(err), "premise: the ask is not in the inbox")

	assert.NoError(t, driveReplyTxn(mgr, cfg, replySelf, j, func(string, ...any) {}))
	_, found, rerr := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, rerr)
	assert.False(t, found, "the journal is still on disk: the reply cannot complete")
}

// TestReply_RecoveryAfterTheRecipientArchivedItDoesNotDeliverASecondCopy is F-178. The sender
// dies after delivering and before recording SENT, the recipient reads the response and
// archives it, and the sender recovers the journal that is still pending: the recovery must
// see that the recipient already has the response (in processed/) and deliver nothing, so
// there is ONE copy, the archived one. Before F-178 this test asserted TWO copies and carried
// the name of the defect; the fixture is the same.
func TestReply_RecoveryAfterTheRecipientArchivedItDoesNotDeliverASecondCopy(t *testing.T) {
	mgr, cfg, data := newReplyPair(t)
	deliverAndNotify(t, mgr, data, rcvAskA, "ask", newTxn(replySelf, []string{rcvAskA}, "x").Timestamp)
	selected := false
	j, err := beginReply(mgr, replySelf, staticSelector(&selected, newTxn(replySelf, []string{rcvAskA}, "answer")), nil)
	require.NoError(t, err)
	require.True(t, selected)
	delivered, derr := deliverResponse(cfg, mgr, replySelf, j)
	require.NoError(t, derr)
	require.True(t, delivered, "premise: the first attempt delivered")
	onDisk, found, rerr := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, rerr)
	require.True(t, found)
	require.Equal(t, session.ReplyTxnPending, onDisk.State, "premise: the sender died before recording SENT")

	// The recipient reads the response and archives it.
	require.NoError(t, transportfs.MoveToProcessed(
		filepath.Join(data, "sessions", replyPeer, "inbox", j.ResponseID+".json"),
		filepath.Join(data, "sessions", replyPeer, "processed")))
	inbox, processed := responseCopies(t, data, j.ResponseID)
	require.Equal(t, 0, inbox, "premise: the recipient consumed it")
	require.Equal(t, 1, processed)

	// The sender recovers.
	resumed, err := beginReply(mgr, replySelf, staticSelector(&selected, nil), nil)
	require.NoError(t, err)
	assert.NoError(t, driveReplyTxn(mgr, cfg, replySelf, resumed, func(string, ...any) {}),
		"the recovery completes: the response is already where the recipient wanted it")

	inbox, processed = responseCopies(t, data, j.ResponseID)
	assert.Equal(t, 1, inbox+processed, "one copy of the response, not a second one (inbox %d, processed %d)", inbox, processed)
	assert.Equal(t, 0, inbox, "and it is the archived one: the recipient's inbox is not given the response again")
}
