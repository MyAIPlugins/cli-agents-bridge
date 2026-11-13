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

// F-177, the copy in the sender's outbox (phase 2). The copy is a courtesy: the delivery has
// already happened when it is written, so a copy that cannot be written (the sender's lock does not
// come, the session is gone) leaves the delivery as it is and writes nothing. It is made under the
// SENDER's session lock, after the recipient's lock is let go of.

// C2: a sender whose session no longer exists gets nothing back: no folder, no outbox, and the
// writer is not even called. Reached directly, because deliverResponse and sendMessage refuse
// earlier, at the load of the sender's manifest, and so never get here for a session that is gone.
func TestCopyToOutbox_ASenderThatNoLongerExistsGetsNothingBack(t *testing.T) {
	t.Parallel()
	cfg, mgr, data := testCfgMgr(t)
	const gone = "gonesess"
	require.NoDirExists(t, f177SessionDir(data, gone), "premise: there is no such session")
	called := false
	write := func(string, []byte, os.FileMode) error { called = true; return nil }

	err := copyToOutbox(mgr, cfg, gone, "msg-aaaaaaaaaaaa", []byte("x"), write, nil)

	require.ErrorIs(t, err, session.ErrSessionGone)
	assert.False(t, called, "nothing was written")
	assert.NoDirExists(t, f177SessionDir(data, gone), "and the folder of a retired session was not recreated")
}

// C3: each caller keeps the primitive it had. A send overwrites a copy that is there; a reply never
// rewrites one. The old content is planted at the instant before the copy, through the same seam
// that B5 uses.
func TestCopyToOutbox_EachCallerKeepsItsOwnPrimitive(t *testing.T) {
	const old, fresh = "old", "fresh"
	t.Run("send overwrites", func(t *testing.T) {
		cfg, mgr, data := testCfgMgr(t)
		plantRoleSession(t, data, "valsess1", session.RoleVal)
		plantRoleSession(t, data, "escsess1", session.RoleEsc)
		var path string
		id, err := sendMessageWith(cfg, mgr, "valsess1", "escsess1", message.TypeQuery, fresh, nil, false, sendSeams{beforeOutbox: func(id string) {
			path = f177OutboxCopy(data, "valsess1", id)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte(old), 0o600))
		}})
		require.NoError(t, err)
		require.Equal(t, f177OutboxCopy(data, "valsess1", id), path, "premise: the seam saw this message's id")
		got, rerr := os.ReadFile(path)
		require.NoError(t, rerr)
		assert.Contains(t, string(got), fresh, "send replaced the copy that was there")
		assert.NotEqual(t, old, string(got))
	})
	t.Run("reply keeps", func(t *testing.T) {
		mgr, cfg, data := newReplyPair(t)
		deliverAndNotify(t, mgr, data, f177Ask, "brief", time.Now().UTC())
		txn, err := beginReply(mgr, replySelf, staticSelector(new(bool), newTxn(replySelf, []string{f177Ask}, fresh)), nil)
		require.NoError(t, err)
		path := f177OutboxCopy(data, replySelf, txn.ResponseID)
		_, err = deliverResponseWith(cfg, mgr, replySelf, txn, deliverSeams{beforeOutbox: func() {
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte(old), 0o600))
		}})
		require.NoError(t, err)
		got, rerr := os.ReadFile(path)
		require.NoError(t, rerr)
		assert.Equal(t, old, string(got), "reply does not rewrite a copy that is already there")
	})
}

// B5: the copy starts only after the recipient's lock was let go of. Never two session locks at
// once: a session that sends to another while that one sends back would otherwise take the same two
// locks in opposite orders. The probe is another process, asked at the instant before the copy.
func TestCopyToOutbox_StartsOnlyAfterTheRecipientsLockIsReleased(t *testing.T) {
	t.Run("reply", func(t *testing.T) {
		mgr, cfg, data := newReplyPair(t)
		deliverAndNotify(t, mgr, data, f177Ask, "brief", time.Now().UTC())
		txn, err := beginReply(mgr, replySelf, staticSelector(new(bool), newTxn(replySelf, []string{f177Ask}, "the answer")), nil)
		require.NoError(t, err)
		ran := false
		_, err = deliverResponseWith(cfg, mgr, replySelf, txn, deliverSeams{beforeOutbox: func() {
			ran = true
			assert.True(t, lockBecomesFreeFor(t, data, replyPeer), "the recipient's lock is already let go of")
		}})
		require.NoError(t, err)
		assert.True(t, ran, "premise: the seam ran")
	})
	t.Run("send", func(t *testing.T) {
		cfg, mgr, data := testCfgMgr(t)
		plantRoleSession(t, data, "valsess1", session.RoleVal)
		plantRoleSession(t, data, "escsess1", session.RoleEsc)
		ran := false
		_, err := sendMessageWith(cfg, mgr, "valsess1", "escsess1", message.TypeQuery, "hi", nil, false, sendSeams{beforeOutbox: func(string) {
			ran = true
			assert.True(t, lockBecomesFreeFor(t, data, "escsess1"), "the recipient's lock is already let go of")
		}})
		require.NoError(t, err)
		assert.True(t, ran, "premise: the seam ran")
	})
}

// C4: while the copy is written the sender's lock is held, and after it is let go of: the write is
// inside the section, not after it.
func TestCopyToOutbox_HoldsTheSendersLockWhileWritingAndLetsGoAfter(t *testing.T) {
	t.Parallel()
	cfg, mgr, data := testCfgMgr(t)
	plantRoleSession(t, data, "valsess1", session.RoleVal)
	wrote := false
	write := func(path string, body []byte, perm os.FileMode) error {
		wrote = true
		require.False(t, lockIsFreeFor(t, data, "valsess1"), "another process finds the sender's lock held while the copy is written")
		return writeOutboxOverwrite(path, body, perm)
	}

	err := copyToOutbox(mgr, cfg, "valsess1", "msg-aaaaaaaaaaaa", []byte("x"), write, nil)

	require.NoError(t, err)
	require.True(t, wrote, "premise: the writer ran")
	assert.FileExists(t, f177OutboxCopy(data, "valsess1", "msg-aaaaaaaaaaaa"))
	assert.True(t, lockBecomesFreeFor(t, data, "valsess1"), "and the lock is let go of after")
}
