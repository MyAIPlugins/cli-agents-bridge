//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// F-176 on the Claude Desktop door. `toolReply` runs the very driver the CLI runs, so the fix is
// there by construction; what is its own is the TEXT it hands back, which used to say, for every
// failure of the driver, that the reply "is on record and the next `reply` finishes it". That is
// false when the journal is gone or has become another reply's.
//
// The tests are integrated: the real tool, the real server, the real disk. The seam
// afterBeginReply runs between the moment the call has taken its copy of the journal and the
// moment it drives it, which is where another driver overtakes it; it is nil in production.

// overtakenCall plants a pending journal J1 for the first ask, makes the call resume it, and
// lets `overtaken` run in the seam. It returns the result of the call and the id of J1's response.
func overtakenCall(t *testing.T, j *joinedDemo, overtaken func(j1 *session.ReplyTxn)) (res toolResult, j1Response string) {
	t.Helper()
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeQuery, "ask A")
	j.put("msg-cccccccccccc", j.valSID, message.TypeQuery, "ask C")

	selected := false
	j1, err := beginReply(j.e.manager(), j.arcSID,
		j.app.replySelector(j.app.lookup(projectKey("/repo/demo", surfaceCowork)), newAuthorizer(j.e.manager(), "/repo/demo", j.e.clock.now()), "msg-aaaaaaaaaaaa", "the answer to A", &selected), nil)
	require.NoError(t, err)
	require.Equal(t, session.ReplyTxnPending, j1.State, "premise: J1 is on disk, pending")

	seamRan := false
	j.app.afterBeginReply = func(taken *session.ReplyTxn) {
		seamRan = true
		require.Equal(t, j1.ResponseID, taken.ResponseID, "premise: the call took J1, not a journal of its own")
		overtaken(taken)
	}
	res = j.reply("msg-aaaaaaaaaaaa", "typed again after the crash")
	require.True(t, seamRan, "premise: the call reached the point where it drives its copy")
	return res, j1.ResponseID
}

// assertTruthfulMCPText is what the text may and may not say: the check wrote nothing, the
// response may already be there, nobody is blamed, and no text of any journal is shown.
func assertTruthfulMCPText(t *testing.T, res toolResult) {
	t.Helper()
	require.True(t, res.IsError, "the call must be an error, got: %s", res.Text())
	text := res.Text()
	assert.Contains(t, text, "this check did not rewrite or delete the journal it found")
	assert.Contains(t, text, "may already have been delivered")
	assert.NotContains(t, text, "nothing else")
	assert.NotContains(t, text, "finishes it without sending it twice", "that line is false once the journal is not the call's")
	assert.NotContains(t, strings.ToLower(text), "removed by")
	assert.NotContains(t, res.Raw, "the answer to A", "what a journal held is never shown")
}

func valCopiesOf(t *testing.T, j *joinedDemo, responseID string) int {
	t.Helper()
	n := 0
	for _, name := range j.valInboxFiles() {
		if name == responseID+".json" {
			n++
		}
	}
	processed, err := os.ReadDir(filepath.Join(j.e.dataDir, "sessions", j.valSID, "processed"))
	if err == nil {
		for _, e := range processed {
			if e.Name() == responseID+".json" {
				n++
			}
		}
	}
	return n
}

// The journal is gone when the call comes to write its progress: another driver completed it.
func TestMCPReply_AJournalThatIsGoneIsSaidTruthfullyAndNotRecreated(t *testing.T) {
	j := newJoinedDemo(t)
	res, j1Response := overtakenCall(t, j, func(taken *session.ReplyTxn) {
		other := *taken // the other driver's own copy, taken before the call moved
		require.NoError(t, driveReplyTxn(j.e.manager(), j.app.cfg, j.arcSID, &other, func(string, ...any) {}))
	})

	assertTruthfulMCPText(t, res)
	_, found := j.journal()
	assert.False(t, found, "no journal: it was completed and the call did not bring it back")
	assert.Equal(t, 1, valCopiesOf(t, j, j1Response), "the response exists once, in the inbox of the val")
	assert.True(t, j.archived("msg-aaaaaaaaaaaa"), "the ask was archived")
	assert.True(t, j.inInbox("msg-cccccccccccc"), "the other ask was not touched")
}

// The journal on disk is another reply's when the call comes to write its progress: J1 was
// completed and a reply to ask C created J2 and delivered it, then stopped.
func TestMCPReply_AJournalThatIsAnotherRepliesIsSaidTruthfullyAndLeftAlone(t *testing.T) {
	j := newJoinedDemo(t)
	var j2Raw []byte
	res, j1Response := overtakenCall(t, j, func(taken *session.ReplyTxn) {
		other := *taken
		require.NoError(t, driveReplyTxn(j.e.manager(), j.app.cfg, j.arcSID, &other, func(string, ...any) {}))

		selected := false
		j2, err := beginReply(j.e.manager(), j.arcSID,
			j.app.replySelector(j.app.lookup(projectKey("/repo/demo", surfaceCowork)), newAuthorizer(j.e.manager(), "/repo/demo", j.e.clock.now()), "msg-cccccccccccc", "the answer to C", &selected), nil)
		require.NoError(t, err)
		require.True(t, selected, "premise: a journal of its own was created for ask C")
		delivered, derr := deliverResponse(j.app.cfg, j.e.manager(), j.arcSID, j2)
		require.NoError(t, derr)
		require.True(t, delivered, "premise: C's response is in the inbox of the val")
		raw, rerr := os.ReadFile(filepath.Join(j.e.dataDir, "sessions", j.arcSID, "reply-txn.json"))
		require.NoError(t, rerr)
		j2Raw = raw
	})

	assertTruthfulMCPText(t, res)
	onDisk, err := os.ReadFile(filepath.Join(j.e.dataDir, "sessions", j.arcSID, "reply-txn.json"))
	require.NoError(t, err, "the other reply's journal is still there")
	assert.Equal(t, string(j2Raw), string(onDisk), "byte for byte what it was")
	assert.Equal(t, 1, valCopiesOf(t, j, j1Response), "the response of J1 exists once")
	assert.Equal(t, 1, valCopiesOf(t, j, session.DeterministicResponseID(j.arcSID, "msg-cccccccccccc")), "and C's is there too")
	assert.True(t, j.archived("msg-aaaaaaaaaaaa"))
	assert.True(t, j.inInbox("msg-cccccccccccc"), "the ask C answered stays open until its own reply is finished")
}
