package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// F-178, criterion 3 on copies that are SEVERAL, found in the gate of the diff (Major 1): the
// response of a reply can sit more than once under its id (two copies, an artifact of the defect
// this lot closes), and "already delivered" must not depend on which copy the delivery looks at
// first. These go through the driver both doors share, with two valid responses in the wire format
// (not unreadable files, not symlinks): the same set of bytes, with the names in both orders.

// anotherValidResponse is the response of the journal with one field changed (its content): valid,
// owned by us, under the same id, and not the bytes that were delivered.
func anotherValidResponse(t *testing.T, cfg config.Config, delivered []byte) []byte {
	t.Helper()
	var m message.Message
	require.NoError(t, json.Unmarshal(delivered, &m))
	m.Content = "another valid answer under the same id"
	other, err := message.EncodeStrict(&m, cfg.MaxMessageBytes)
	require.NoError(t, err)
	require.False(t, bytes.Equal(delivered, other), "premise: the second response has other bytes")
	return other
}

// archivedCopies replaces the one archived copy of deliveredThenArchived with two, whose names say
// which is the older and which the newer.
func archivedCopies(t *testing.T, archived, responseID string, older, newer []byte) {
	t.Helper()
	require.NoError(t, os.Remove(archived))
	dir := filepath.Dir(archived)
	// planted newest first: the order of the names decides, not the order of creation
	require.NoError(t, os.WriteFile(filepath.Join(dir, "20260102T000000.000000000Z-"+responseID+".json"), newer, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "20260101T000000.000000000Z-"+responseID+".json"), older, 0o600))
}

func TestDriveReplyTxn_SeveralArchivedCopiesAreAllJudgedWhateverTheOrderOfTheNames(t *testing.T) {
	for _, identicalIsOlder := range []bool{true, false} {
		name := "the identical copy has the later name"
		if identicalIsOlder {
			name = "the identical copy has the earlier name"
		}
		t.Run(name, func(t *testing.T) {
			mgr, cfg, data, j, archived := deliveredThenArchived(t)
			good, err := os.ReadFile(archived)
			require.NoError(t, err)
			other := anotherValidResponse(t, cfg, good)
			older, newer := other, good
			if identicalIsOlder {
				older, newer = good, other
			}
			archivedCopies(t, archived, j.ResponseID, older, newer)
			before, err := os.ReadFile(replyJournalPath(data))
			require.NoError(t, err)

			resumed, err := beginReply(mgr, replySelf, staticSelector(new(bool), nil), nil)
			require.NoError(t, err)
			derr := driveReplyTxn(mgr, cfg, replySelf, resumed, func(string, ...any) {})

			require.Error(t, derr, "a copy with other bytes is a collision, whichever name it has")
			assert.Contains(t, derr.Error(), "different content")
			inbox, processed := responseCopies(t, data, j.ResponseID)
			assert.Equal(t, 0, inbox, "nothing was written")
			assert.Equal(t, 2, processed, "the two copies are as they were")
			assert.Equal(t, 0, askArchivedCount(t, data, rcvAskA), "the ask stays open")
			after, err := os.ReadFile(replyJournalPath(data))
			require.NoError(t, err)
			assert.Equal(t, string(before), string(after), "the journal is as it was: pending, to be looked at by hand")
		})
	}
}

// Two archived copies that are the same bytes are not a collision: the response was delivered, the
// recipient archived it, and the copy is there twice. The delivery writes nothing and the reply is
// completed.
func TestDriveReplyTxn_TwoIdenticalArchivedCopiesAreAlreadyDelivered(t *testing.T) {
	mgr, cfg, data, j, archived := deliveredThenArchived(t)
	good, err := os.ReadFile(archived)
	require.NoError(t, err)
	archivedCopies(t, archived, j.ResponseID, good, good)

	resumed, err := beginReply(mgr, replySelf, staticSelector(new(bool), nil), nil)
	require.NoError(t, err)
	require.NoError(t, driveReplyTxn(mgr, cfg, replySelf, resumed, func(string, ...any) {}))

	inbox, processed := responseCopies(t, data, j.ResponseID)
	assert.Equal(t, 0, inbox)
	assert.Equal(t, 2, processed)
	assert.Equal(t, 1, askArchivedCount(t, data, rcvAskA), "the ask was closed")
	_, found, rerr := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, rerr)
	assert.False(t, found, "the journal is complete and gone")
}

// The copy in the recipient's inbox/ and the one it archived, with the bytes crossed both ways. The
// archived copy decides "already delivered" only when nothing is in inbox/: a copy there is judged by
// the create-if-absent, so an inbox/ copy with other bytes is refused even when the archived one is
// identical, and an archived copy with other bytes is refused even when the inbox/ one is identical.
// The first is what a delivery that stops at "something is archived" would let pass.
func TestDriveReplyTxn_ACopyInTheInboxAndAnArchivedOneAreBothJudged(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		inboxIsSame, archivedIs bool // true = the bytes that were delivered
	}{
		{"the inbox copy differs, the archived one is the same", false, true},
		{"the inbox copy is the same, the archived one differs", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, cfg, data, j, archived := deliveredThenArchived(t)
			good, err := os.ReadFile(archived)
			require.NoError(t, err)
			other := anotherValidResponse(t, cfg, good)
			pick := func(same bool) []byte {
				if same {
					return good
				}
				return other
			}
			require.NoError(t, os.WriteFile(archived, pick(tc.archivedIs), 0o600))
			inboxPath := filepath.Join(data, "sessions", replyPeer, "inbox", j.ResponseID+".json")
			require.NoError(t, os.WriteFile(inboxPath, pick(tc.inboxIsSame), 0o600))

			resumed, err := beginReply(mgr, replySelf, staticSelector(new(bool), nil), nil)
			require.NoError(t, err)
			derr := driveReplyTxn(mgr, cfg, replySelf, resumed, func(string, ...any) {})

			require.Error(t, derr)
			assert.Contains(t, derr.Error(), "different content")
			assert.Equal(t, 0, askArchivedCount(t, data, rcvAskA), "the ask stays open")
			_, found, rerr := mgr.ReadReplyTxn(replySelf)
			require.NoError(t, rerr)
			assert.True(t, found, "the journal stays")
		})
	}
}
