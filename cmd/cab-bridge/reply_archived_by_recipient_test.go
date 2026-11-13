package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// F-178: how a RESPONSE leaves the inbox of the agent that asked. Not a test of the fix: it is the
// evidence for the claim the fix rests on, that the response a `reply` delivers ends up in the
// recipient's processed/ through the same file move (transportfs.MoveToProcessed) whichever door
// archives it, and that `reply` itself never does it (it closes asks only, which is read from the
// code: collectOpenAsks and the MCP selector both require message.TypeQuery).
//
// The two archivers that can touch a response are `inbox --tidy` (tidyInbox, command line) and the
// `ack` tool of Claude Desktop (archiveBatch with the move of production). Both are driven here
// on a response that a real, completed `reply` delivered.

// completedReply runs a whole `reply` from replySelf to replyPeer (journal, delivery, archive of
// the ask, journal removed) and returns the id of the response now sitting in the peer's inbox,
// marked as shown to the peer, as it is after a `next`.
func completedReply(t *testing.T) (mgr *session.Manager, data, responseID string) {
	t.Helper()
	mgr, cfg, data := newReplyPair(t)
	deliverAndNotify(t, mgr, data, rcvAskA, "ask", newTxn(replySelf, []string{rcvAskA}, "x").Timestamp)
	selected := false
	txn, err := beginReply(mgr, replySelf, staticSelector(&selected, newTxn(replySelf, []string{rcvAskA}, "the answer")), nil)
	require.NoError(t, err)
	require.NoError(t, driveReplyTxn(mgr, cfg, replySelf, txn, func(string, ...any) {}))
	_, found, rerr := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, rerr)
	require.False(t, found, "premise: the reply is complete, the journal is gone")

	inbox, processed := responseCopies(t, data, txn.ResponseID)
	require.Equal(t, 1, inbox, "premise: the response is in the peer's inbox")
	require.Equal(t, 0, processed)
	_, cerr := mgr.CommitWakeCursor(replyPeer, []string{txn.ResponseID}, time.Now().UTC(), nil, nil)
	require.NoError(t, cerr)
	return mgr, data, txn.ResponseID
}

func TestRecipientArchivers_TheResponseOfAReplyLeavesTheInboxThroughMoveToProcessed(t *testing.T) {
	peerDir := func(data string) string { return filepath.Join(data, "sessions", replyPeer) }

	t.Run("inbox --tidy, on the command line", func(t *testing.T) {
		mgr, data, responseID := completedReply(t)
		res, err := tidyInbox(mgr, replyPeer, peerDir(data), 65536)
		require.NoError(t, err)
		assert.Equal(t, 1, res.moved, "tidy sweeps a response that was shown")
		inbox, processed := responseCopies(t, data, responseID)
		assert.Equal(t, 0, inbox)
		assert.Equal(t, 1, processed, "and it is in processed/ as <instant>-<id>.json, the name the lookup of F-178 relies on")
	})

	t.Run("the ack tool, in Claude Desktop", func(t *testing.T) {
		_, data, responseID := completedReply(t)
		notAnAsk := func(m *message.Message) error {
			if m.Type == message.TypeQuery {
				return os.ErrInvalid
			}
			return nil
		}
		results, ok := archiveBatch(peerDir(data), []string{responseID}, 65536, notAnAsk, moveToProcessed)
		require.True(t, ok, "%+v", results)
		assert.Equal(t, ackArchived, results[0].Outcome)
		inbox, processed := responseCopies(t, data, responseID)
		assert.Equal(t, 0, inbox)
		assert.Equal(t, 1, processed)
	})
}
