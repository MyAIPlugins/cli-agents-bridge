package main

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// These tests exercise beginReply directly. They exist because the older
// TestReply_SecondInitializerResumesTheFirstJournal goes through replyRun, which
// resolves the session from the real cwd, fails to find one in a temp dir and
// passes without ever reaching the journal (F-148): the precedence of the
// journal was therefore defended by a test that could not fail.

func staticSelector(called *bool, txn *session.ReplyTxn) replySelector {
	return func() (*session.ReplyTxn, error) {
		*called = true
		return txn, nil
	}
}

// The journal that is already in flight wins over whatever the selector would
// build: finishing a delivered reply must not depend on how the retry is spelled.
func TestBeginReply_InFlightJournalOutranksTheSelector(t *testing.T) {
	mgr, _, _ := newReplyPair(t)
	first := newTxn(replySelf, []string{"msg-aaaaaaaaaaaa"}, "the first answer")
	require.NoError(t, mgr.WriteReplyTxn(replySelf, first))

	var called bool
	other := newTxn(replySelf, []string{"msg-bbbbbbbbbbbb"}, "a completely different answer")
	got, err := beginReply(mgr, replySelf, staticSelector(&called, other))
	require.NoError(t, err)

	assert.False(t, called, "the selector must not even run while a journal is in flight")
	assert.Equal(t, first.ResponseID, got.ResponseID)
	assert.Equal(t, "the first answer", got.Content, "the frozen content wins over the retry's")

	onDisk, found, rerr := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, rerr)
	require.True(t, found)
	assert.Equal(t, first.ResponseID, onDisk.ResponseID, "the journal on disk is untouched")
	assert.Equal(t, "the first answer", onDisk.Content)
}

// The selection is persisted before beginReply returns, and a second initializer
// resumes it instead of replacing it (CRI diff-gate P0-1).
func TestBeginReply_PersistsTheSelectionAndASecondInitializerResumesIt(t *testing.T) {
	mgr, _, _ := newReplyPair(t)

	var firstCalled, secondCalled bool
	mine := newTxn(replySelf, []string{"msg-aaaaaaaaaaaa"}, "mine")
	got, err := beginReply(mgr, replySelf, staticSelector(&firstCalled, mine))
	require.NoError(t, err)
	require.True(t, firstCalled)
	assert.Equal(t, "mine", got.Content)

	onDisk, found, rerr := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, rerr)
	require.True(t, found, "the journal is written before beginReply returns")
	assert.Equal(t, mine.ResponseID, onDisk.ResponseID)

	rival := newTxn(replySelf, []string{"msg-bbbbbbbbbbbb"}, "rival")
	resumed, err := beginReply(mgr, replySelf, staticSelector(&secondCalled, rival))
	require.NoError(t, err)
	assert.False(t, secondCalled, "a second initializer resumes J1, it never builds J2")
	assert.Equal(t, mine.ResponseID, resumed.ResponseID)
}

// A selector that refuses leaves no journal behind: nothing was frozen.
func TestBeginReply_SelectorErrorLeavesNoJournal(t *testing.T) {
	mgr, _, _ := newReplyPair(t)
	got, err := beginReply(mgr, replySelf, func() (*session.ReplyTxn, error) {
		return nil, errNothingToReplyTo
	})
	require.ErrorIs(t, err, errNothingToReplyTo)
	assert.Nil(t, got)

	_, found, rerr := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, rerr)
	assert.False(t, found)
}

// The CLI selector, moved out of replyRun unchanged, still turns a NOTIFIED ask
// into the frozen transaction the verb has always built.
func TestCliReplySelector_BuildsTheTransactionFromTheNotifiedAsk(t *testing.T) {
	mgr, cfg, dataDir := newReplyPair(t)
	deliverAndNotify(t, mgr, dataDir, "msg-aaaaaaaaaaaa", "brief", time.Now().UTC())

	got, err := beginReply(mgr, replySelf, cliReplySelector(mgr, cfg, replySelf, []string{"the report"}, strings.NewReader("")))
	require.NoError(t, err)

	assert.Equal(t, replyPeer, got.To)
	assert.Equal(t, []string{"msg-aaaaaaaaaaaa"}, got.CloseIDs)
	assert.Equal(t, "msg-aaaaaaaaaaaa", got.Anchor)
	assert.Equal(t, "the report", got.Content)
	assert.Equal(t, session.ReplyTxnPending, got.State)
	assert.Equal(t, session.DeterministicResponseID(replySelf, "msg-aaaaaaaaaaaa"), got.ResponseID)

	onDisk, found, rerr := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, rerr)
	require.True(t, found)
	assert.Equal(t, got.ResponseID, onDisk.ResponseID)
}

// The CLI selector keeps its two refusals: nothing to answer, and something to
// answer that has not been shown yet. The state the agent is in decides which.
func TestCliReplySelector_KeepsItsTwoRefusals(t *testing.T) {
	mgr, cfg, dataDir := newReplyPair(t)

	_, err := beginReply(mgr, replySelf, cliReplySelector(mgr, cfg, replySelf, []string{"x"}, strings.NewReader("")))
	require.ErrorIs(t, err, errNothingToReplyTo, "an empty inbox has nothing to answer")

	plantInboxAt(t, dataDir, replySelf, "msg-cccccccccccc", replyPeer, "query", "never shown", time.Now().UTC())
	_, err = beginReply(mgr, replySelf, cliReplySelector(mgr, cfg, replySelf, []string{"x"}, strings.NewReader("")))
	require.ErrorIs(t, err, errUndeliveredWaiting, "an ask that was never delivered is not answerable through the CLI")

	_, found, rerr := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, rerr)
	assert.False(t, found, "neither refusal leaves a journal")
}
