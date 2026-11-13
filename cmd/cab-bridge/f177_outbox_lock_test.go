package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// F-177, the copy in the sender's outbox (phase 2). The copy is a courtesy: the delivery has
// already happened when it is written, so a copy that cannot be written (the sender's lock does not
// come, the session is gone) leaves the delivery as it is and writes nothing. It is made under the
// SENDER's session lock, after the recipient's lock is let go of.
//
// This file uses only names that exist on main before F-177, so that its red can be measured there.

func f177OutboxCopy(data, sid, id string) string {
	return filepath.Join(f177SessionDir(data, sid), "outbox", id+".json")
}

// C1 (reply): the sender's lock is another process's. The response is delivered, no copy is
// written, and the reply says so on stderr instead of dropping the error as it did. Before F-177
// the copy was written without asking for the lock.
func TestDeliverResponse_WhenTheSendersLockDoesNotComeTheDeliveryStillSucceedsAndNoCopyIsWritten(t *testing.T) {
	mgr, cfg, data := newReplyPair(t)
	deliverAndNotify(t, mgr, data, f177Ask, "brief", time.Now().UTC())
	txn, err := beginReply(mgr, replySelf, staticSelector(new(bool), newTxn(replySelf, []string{f177Ask}, "the answer")), nil)
	require.NoError(t, err)
	mgr.LockWait = 150 * time.Millisecond
	holdLock(t, data, replySelf)
	requireHeldByAnother(t, data, replySelf)

	var delivered bool
	_, stderr := captureStdoutStderr(t, func() {
		delivered, err = deliverResponse(cfg, mgr, replySelf, txn)
	})

	require.NoError(t, err, "a copy that cannot be written does not fail the delivery")
	assert.True(t, delivered)
	assert.FileExists(t, filepath.Join(f177SessionDir(data, replyPeer), "inbox", txn.ResponseID+".json"), "the response is in the recipient's inbox")
	assert.NoFileExists(t, f177OutboxCopy(data, replySelf, txn.ResponseID), "and no copy was written without the sender's lock")
	assert.Contains(t, stderr, "outbox copy for "+txn.ResponseID+" (non-fatal)", "the copy that was not written is said, not dropped")
}

// C1 (send): the same for the ordinary send. Not parallel: it reads what the command wrote on stderr.
func TestSendMessage_WhenTheSendersLockDoesNotComeTheMessageIsStillDeliveredAndNoCopyIsWritten(t *testing.T) {
	cfg, mgr, data := testCfgMgr(t)
	plantRoleSession(t, data, "valsess1", session.RoleVal)
	plantRoleSession(t, data, "escsess1", session.RoleEsc)
	mgr.LockWait = 150 * time.Millisecond
	holdLock(t, data, "valsess1")
	requireHeldByAnother(t, data, "valsess1")

	var id string
	var err error
	_, stderr := captureStdoutStderr(t, func() {
		id, err = sendMessage(cfg, mgr, "valsess1", "escsess1", message.TypeQuery, "hi", nil, false)
	})

	require.NoError(t, err, "a copy that cannot be written does not fail the send")
	require.NotEmpty(t, id)
	assert.FileExists(t, filepath.Join(f177SessionDir(data, "escsess1"), "inbox", id+".json"))
	assert.NoFileExists(t, f177OutboxCopy(data, "valsess1", id), "no copy without the sender's lock")
	assert.Contains(t, stderr, "outbox copy for "+id+" (non-fatal)")
}
