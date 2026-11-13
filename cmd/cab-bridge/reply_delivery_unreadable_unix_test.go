//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The cost of looking, as the BAR declares it: a delivery that cannot read the recipient's
// processed/ refuses instead of guessing "not delivered". Before F-178 such a delivery went
// through; now it is an error, for a reply that is new and for one that resumes, and nothing is
// written either time. A retry looks again.
func TestDeliverResponse_AProcessedFolderThatCannotBeReadRefusesToDeliver(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads a folder with no permission")
	}
	unreadable := func(t *testing.T, data string) {
		t.Helper()
		dir := filepath.Join(data, "sessions", replyPeer, "processed")
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.Chmod(dir, 0o000))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	}

	t.Run("a new reply", func(t *testing.T) {
		mgr, cfg, data := newReplyPair(t)
		deliverAndNotify(t, mgr, data, rcvAskA, "ask", newTxn(replySelf, []string{rcvAskA}, "x").Timestamp)
		selected := false
		fresh, err := beginReply(mgr, replySelf, staticSelector(&selected, newTxn(replySelf, []string{rcvAskA}, "the answer")), nil)
		require.NoError(t, err)
		require.True(t, selected)
		unreadable(t, data)

		delivered, derr := deliverResponse(cfg, mgr, replySelf, fresh)
		require.Error(t, derr)
		assert.False(t, delivered)
		assert.Contains(t, derr.Error(), "look for the response among the recipient's messages")
		assert.Empty(t, peerInbox(t, data), "nothing was delivered")
	})
	t.Run("a recovery, through the driver", func(t *testing.T) {
		data, _ := journalInFlight(t)
		cfg := nextTestConfig(t, data)
		mgr := newSessionManager(cfg)
		unreadable(t, data)

		resumed, err := beginReply(mgr, replySelf, staticSelector(new(bool), nil), nil)
		require.NoError(t, err)
		derr := driveReplyTxn(mgr, cfg, replySelf, resumed, func(string, ...any) {})
		require.Error(t, derr)
		assert.Empty(t, peerInbox(t, data), "nothing was delivered")
		assert.Equal(t, 0, askArchivedCount(t, data, rcvAskA), "the ask stays open")
		_, found, rerr := mgr.ReadReplyTxn(replySelf)
		require.NoError(t, rerr)
		assert.True(t, found, "the journal stays, to be resumed")
	})
}
