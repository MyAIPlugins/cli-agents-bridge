package main

import (
	"bytes"
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

// F-178: the delivery of a response judges "already delivered" on the recipient's inbox AND on what
// the recipient has archived. These tests are the criteria of the BAR on the paths that reach
// deliverResponse: the command line (replyRun), the driver both doors share, and a reply that is
// NEW (a journal that was lost after the delivery), because the check runs on every delivery and
// not only on a recovery.

// deliveredThenArchived is the scene of the defect: the response of a reply was delivered, the
// sender died before recording SENT (the journal is still pending), and the recipient archived the
// response. It returns the journal and where the archived copy is now.
func deliveredThenArchived(t *testing.T) (mgr *session.Manager, cfg config.Config, data string, j *session.ReplyTxn, archivedPath string) {
	t.Helper()
	mgr, cfg, data = newReplyPair(t)
	t.Setenv("CAB_SESSION_ID", replySelf)
	deliverAndNotify(t, mgr, data, rcvAskA, "ask", newTxn(replySelf, []string{rcvAskA}, "x").Timestamp)
	selected := false
	j, err := beginReply(mgr, replySelf, staticSelector(&selected, newTxn(replySelf, []string{rcvAskA}, "the answer")), nil)
	require.NoError(t, err)
	delivered, derr := deliverResponse(cfg, mgr, replySelf, j)
	require.NoError(t, derr)
	require.True(t, delivered, "premise: the first attempt delivered")
	require.NoError(t, transportfs.MoveToProcessed(
		filepath.Join(data, "sessions", replyPeer, "inbox", j.ResponseID+".json"),
		filepath.Join(data, "sessions", replyPeer, "processed")))
	inbox, processed := responseCopies(t, data, j.ResponseID)
	require.Equal(t, 0, inbox, "premise: the recipient archived it")
	require.Equal(t, 1, processed)
	return mgr, cfg, data, j, archivedCopy(t, data, j.ResponseID)
}

// archivedCopy is the path of the one copy of the response in the recipient's processed/.
func archivedCopy(t *testing.T, data, responseID string) string {
	t.Helper()
	dir := filepath.Join(data, "sessions", replyPeer, "processed")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var found []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "-"+responseID+".json") {
			found = append(found, filepath.Join(dir, e.Name()))
		}
	}
	require.Len(t, found, 1, "premise: exactly one archived copy")
	return found[0]
}

// The command line, whole: replyRun resumes the pending journal, finds the response among the
// recipient's archived messages and delivers nothing; the ask is closed and the journal is gone.
func TestReplyRun_ARecoveryAfterTheRecipientArchivedTheResponseDeliversNothing(t *testing.T) {
	_, _, data, j, _ := deliveredThenArchived(t)

	var stdout, stderr bytes.Buffer
	require.NoError(t, replyRun(nil, strings.NewReader(""), &stdout, &stderr))

	inbox, processed := responseCopies(t, data, j.ResponseID)
	assert.Equal(t, 0, inbox, "the recipient's inbox is not given the response again")
	assert.Equal(t, 1, processed, "one copy: the archived one")
	assert.Equal(t, 1, askArchivedCount(t, data, rcvAskA), "the ask was closed")
	assert.NoFileExists(t, replyJournalPath(data), "the journal is complete and gone")
	assert.Contains(t, stderr.String(), "was already delivered", "it says why nothing was sent")
	assert.Contains(t, stderr.String(), "the recipient has already archived it", "and that it is the recipient's archive that has it, not only its inbox")
}

// Criterion 3 on the recovery: the response is archived under its id, but with other bytes. It is
// not "already delivered": it is two messages that collided, and that is an error that writes
// nothing and archives nothing.
func TestDriveReplyTxn_ADifferentMessageArchivedUnderTheSameIdIsAnErrorAndWritesNothing(t *testing.T) {
	mgr, cfg, data, j, archived := deliveredThenArchived(t)
	require.NoError(t, os.WriteFile(archived, []byte(`{"not":"the response"}`), 0o600))
	before, rerr := os.ReadFile(replyJournalPath(data))
	require.NoError(t, rerr)

	resumed, err := beginReply(mgr, replySelf, staticSelector(new(bool), nil), nil)
	require.NoError(t, err)
	derr := driveReplyTxn(mgr, cfg, replySelf, resumed, func(string, ...any) {})

	require.Error(t, derr)
	assert.Contains(t, derr.Error(), "different content")
	inbox, _ := responseCopies(t, data, j.ResponseID)
	assert.Equal(t, 0, inbox, "nothing was written")
	assert.Equal(t, 0, askArchivedCount(t, data, rcvAskA), "the ask stays open")
	after, rerr := os.ReadFile(replyJournalPath(data))
	require.NoError(t, rerr)
	assert.Equal(t, string(before), string(after), "the journal is as it was: pending, to be looked at by hand")
}

// The same collision while the response is still in the inbox is what it always was: the
// create-if-absent refuses. A guard that the new check did not change it.
func TestDriveReplyTxn_ADifferentMessageInTheInboxUnderTheSameIdIsStillAnError(t *testing.T) {
	mgr, cfg, data, j, archived := deliveredThenArchived(t)
	require.NoError(t, os.Remove(archived))
	require.NoError(t, os.WriteFile(filepath.Join(data, "sessions", replyPeer, "inbox", j.ResponseID+".json"), []byte(`{"not":"the response"}`), 0o600))

	resumed, err := beginReply(mgr, replySelf, staticSelector(new(bool), nil), nil)
	require.NoError(t, err)
	derr := driveReplyTxn(mgr, cfg, replySelf, resumed, func(string, ...any) {})

	require.Error(t, derr)
	assert.Contains(t, derr.Error(), "different content")
	assert.Equal(t, 0, askArchivedCount(t, data, rcvAskA), "the ask stays open")
}

// The check does not depend on how the journal came to be: a NEW reply to the same ask, after the
// first journal was lost, meets the response the recipient archived. With other bytes it is an
// error (criterion 3 holds for a new reply too); with the same bytes it is already delivered.
// Both would fail if the check ran only on a journal found on disk.
func TestDeliverResponse_ANewJournalMeetsTheResponseTheRecipientArchived(t *testing.T) {
	t.Run("other bytes: an error, nothing delivered", func(t *testing.T) {
		mgr, cfg, data, j, _ := deliveredThenArchived(t)
		require.NoError(t, os.Remove(replyJournalPath(data)), "the first journal is lost")

		selected := false
		fresh, err := beginReply(mgr, replySelf, staticSelector(&selected, newTxn(replySelf, []string{rcvAskA}, "another answer typed later")), nil)
		require.NoError(t, err)
		require.True(t, selected, "premise: this is a NEW journal, not a resumed one")
		require.Equal(t, j.ResponseID, fresh.ResponseID, "premise: the id derives from (responder, anchor), so it is the same")

		delivered, derr := deliverResponse(cfg, mgr, replySelf, fresh)
		require.Error(t, derr)
		assert.False(t, delivered)
		assert.Contains(t, derr.Error(), "different content")
		inbox, processed := responseCopies(t, data, j.ResponseID)
		assert.Equal(t, 0, inbox)
		assert.Equal(t, 1, processed)
	})
	t.Run("same bytes: already delivered, nothing written", func(t *testing.T) {
		mgr, cfg, data, j, _ := deliveredThenArchived(t)
		require.NoError(t, os.Remove(replyJournalPath(data)), "the first journal is lost")

		selected := false
		fresh, err := beginReply(mgr, replySelf, staticSelector(&selected, newTxn(replySelf, []string{rcvAskA}, "the answer")), nil)
		require.NoError(t, err)
		require.True(t, selected)

		delivered, derr := deliverResponse(cfg, mgr, replySelf, fresh)
		require.NoError(t, derr)
		assert.False(t, delivered, "the recipient already has it")
		inbox, processed := responseCopies(t, data, j.ResponseID)
		assert.Equal(t, 0, inbox)
		assert.Equal(t, 1, processed)
	})
}
