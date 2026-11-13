//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
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

// The same refusal when the folder CAN be listed but the copy that is there cannot be opened (found
// in the gate of the diff, Nit 2). The tests above make the whole folder unreadable, which the
// permissive look also fails on; this one tells the two apart: a delivery that skipped the entry
// ("not ours", as the readers do) would find nothing and write a second copy of a response that was
// delivered and archived. It must refuse, and leave the journal and the recipient's inbox as they
// were. The second case puts the unopenable copy AFTER an identical one: every copy is looked at,
// not only the first.
func TestDeliverResponse_AnArchivedCopyThatCannotBeOpenedRefusesToDeliver(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root opens a file with no permission")
	}
	for _, tc := range []struct {
		name        string
		extraCopyOK bool // a second, identical copy sits before the unopenable one
	}{
		{"the only archived copy", false},
		{"a later copy, after an identical one", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, cfg, data, j, archived := deliveredThenArchived(t)
			target := archived
			if tc.extraCopyOK {
				good, err := os.ReadFile(archived)
				require.NoError(t, err)
				archivedCopies(t, archived, j.ResponseID, good, good)
				target = filepath.Join(filepath.Dir(archived), "20260102T000000.000000000Z-"+j.ResponseID+".json")
			}
			before, err := os.ReadFile(replyJournalPath(data))
			require.NoError(t, err)
			require.NoError(t, os.Chmod(target, 0o000))
			t.Cleanup(func() { _ = os.Chmod(target, 0o600) })
			_, premise := security.ReadOwnedFile(target)
			require.ErrorIs(t, premise, os.ErrPermission, "premise: the refusal is the real permission one")
			_, listing := os.ReadDir(filepath.Dir(target))
			require.NoError(t, listing, "premise: the folder itself can be listed")

			created, derr := deliverResponse(cfg, mgr, replySelf, j)

			require.Error(t, derr)
			assert.False(t, created)
			assert.ErrorIs(t, derr, os.ErrPermission)
			assert.Empty(t, peerInbox(t, data), "no second copy was delivered")
			after, err := os.ReadFile(replyJournalPath(data))
			require.NoError(t, err)
			assert.Equal(t, string(before), string(after), "the journal is as it was")
		})
	}
}
