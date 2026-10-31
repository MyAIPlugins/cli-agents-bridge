//go:build !windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

func (j *joinedDemo) reply(askID, text string) toolResult {
	j.e.t.Helper()
	return j.c.call("reply", map[string]any{"ask_id": askID, "message": text})
}

func (j *joinedDemo) valInboxFiles() []string {
	entries, err := os.ReadDir(filepath.Join(j.e.dataDir, "sessions", j.valSID, "inbox"))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func (j *joinedDemo) journal() (*session.ReplyTxn, bool) {
	txn, found, err := j.e.manager().ReadReplyTxn(j.arcSID)
	require.NoError(j.e.t, err)
	return txn, found
}

func TestMCPReply_AnswersTheNamedAskAndClosesOnlyThat(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeQuery, "first ask")
	j.put("msg-bbbbbbbbbbbb", j.valSID, message.TypeQuery, "second ask")

	res := j.reply("msg-bbbbbbbbbbbb", "the answer to the second")
	require.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "Reply delivered to VAL-demo")
	assert.Contains(t, res.Text(), "Ask msg-bbbbbbbbbbbb is closed")

	assert.True(t, j.archived("msg-bbbbbbbbbbbb"))
	assert.True(t, j.inInbox("msg-aaaaaaaaaaaa"), "the other ask is untouched: an answer closes the ask it names")

	files := j.valInboxFiles()
	require.Len(t, files, 1)
	data, err := os.ReadFile(filepath.Join(j.e.dataDir, "sessions", j.valSID, "inbox", files[0]))
	require.NoError(t, err)
	var got message.Message
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, message.TypeResponse, got.Type)
	assert.Equal(t, "the answer to the second", got.Content)
	assert.Equal(t, j.arcSID, got.From)
	assert.Equal(t, session.RoleArchitect, got.FromRole)
	assert.Equal(t, session.RoleVal, got.ToRole)
	require.NotNil(t, got.InReplyTo)
	assert.Equal(t, "msg-bbbbbbbbbbbb", *got.InReplyTo)
	assert.Equal(t, []string{"msg-bbbbbbbbbbbb"}, got.Closes)

	_, found := j.journal()
	assert.False(t, found, "the journal is gone once the reply is complete")
	assert.NoFileExists(t, filepath.Join(j.e.dataDir, "sessions", j.arcSID, "wake-cursor.json"), "no cursor was involved")
}

func TestMCPReply_RefusesWhatIsNotAnAnswerableAskAndWritesNothing(t *testing.T) {
	j := newJoinedDemo(t)
	plantOverviewSession(t, j.e.dataDir, "escsecret", session.RoleEsc, "ESC-SECRET-NAME", "/repo/demo", "", "working")
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, "a tell")
	j.put("msg-bbbbbbbbbbbb", j.valSID, message.TypeResponse, "a response")
	j.put("msg-cccccccccccc", "escsecret", message.TypeQuery, "SECRET-BODY an ask from an executor")
	j.put("msg-dddddddddddd", j.valSID, message.TypeQuery, "answered already")
	require.False(t, j.reply("msg-dddddddddddd", "done").IsError)
	before := j.valInboxFiles()

	cases := map[string]string{
		"msg-aaaaaaaaaaaa": "only an ask",
		"msg-bbbbbbbbbbbb": "only an ask",
		"msg-cccccccccccc": "held back",
		"msg-dddddddddddd": "already answered",
		"msg-eeeeeeeeeeee": "no such ask",
		"../../etc/passwd": "not a valid message id",
		"":                 "not a valid message id",
	}
	for id, want := range cases {
		res := j.reply(id, "an answer")
		require.True(t, res.IsError, "id %q", id)
		assert.Contains(t, res.Text(), want, "id %q", id)
		assert.NotContains(t, res.Raw, "SECRET-BODY")
		assert.NotContains(t, res.Raw, "ESC-SECRET-NAME")
	}
	assert.Equal(t, before, j.valInboxFiles(), "nothing was delivered")
	_, found := j.journal()
	assert.False(t, found, "and no journal was left behind by a refused reply")
	assert.True(t, j.inInbox("msg-cccccccccccc"))
}

func TestMCPReply_RefusesAnEmptyOrOversizedAnswerBeforeAnythingIsRead(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeQuery, "ask")
	assert.True(t, j.reply("msg-aaaaaaaaaaaa", "  \n").IsError)
	big := j.reply("msg-aaaaaaaaaaaa", strings.Repeat("x", j.app.cfg.MaxMessageBytes+1))
	require.True(t, big.IsError)
	assert.Contains(t, big.Text(), "the most one message may carry")
	assert.True(t, j.inInbox("msg-aaaaaaaaaaaa"))
}

// The permission, on the answering side: with the bridge closed or expired an
// architect cannot answer that val, and a reply that was refused never became a
// journal (a pending journal retries forever and would block every later reply).
func TestMCPReply_AClosedOrExpiredBridgeCannotBeAnswered(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeQuery, "ask")

	require.NoError(t, j.e.manager().CloseDesktopGrant(j.valSID))
	res := j.reply("msg-aaaaaaaaaaaa", "an answer")
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "held back", "the ask itself is no longer visible")
	assert.Empty(t, j.valInboxFiles())
	_, found := j.journal()
	assert.False(t, found)

	_, err := j.e.manager().OpenDesktopGrant(j.valSID, time.Hour)
	require.NoError(t, err)
	j.e.clock.advance(2 * time.Hour)
	res = j.reply("msg-aaaaaaaaaaaa", "an answer")
	require.True(t, res.IsError, "expired is closed as well")
	assert.Empty(t, j.valInboxFiles())
	assert.True(t, j.inInbox("msg-aaaaaaaaaaaa"))
}

// The C2 matrix. A journal survives a crash between its creation and its delivery;
// then the bridge closes. The journal is judged on the val it is ADDRESSED to, not on
// the ask the new call names: blocked, intact, and the new request not processed.
func TestMCPReply_APendingJournalWhoseValClosedIsBlockedAndKeptIntact(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeQuery, "ask A")
	j.put("msg-bbbbbbbbbbbb", j.valSID, message.TypeQuery, "ask B")

	// The crash: the journal for A is written, nothing delivered.
	selected := false
	txn, err := beginReply(j.e.manager(), j.arcSID,
		j.app.replySelector(j.app.lookup(projectKey("/repo/demo", surfaceCowork)), newAuthorizer(j.e.manager(), "/repo/demo", j.e.clock.now()), "msg-aaaaaaaaaaaa", "the answer to A", &selected), nil)
	require.NoError(t, err)
	require.Equal(t, session.ReplyTxnPending, txn.State)
	onDisk, err := os.ReadFile(filepath.Join(j.e.dataDir, "sessions", j.arcSID, "reply-txn.json"))
	require.NoError(t, err)

	// The val closes the bridge. A new reply, for the OTHER ask, arrives.
	require.NoError(t, j.e.manager().CloseDesktopGrant(j.valSID))
	res := j.reply("msg-bbbbbbbbbbbb", "the answer to B")
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "not open")
	assert.Contains(t, res.Text(), "stays on record untouched")
	assert.NotContains(t, res.Raw, "VAL-demo", "the notice names nobody")

	after, err := os.ReadFile(filepath.Join(j.e.dataDir, "sessions", j.arcSID, "reply-txn.json"))
	require.NoError(t, err)
	assert.Equal(t, string(onDisk), string(after), "the journal is byte for byte what it was")
	assert.Empty(t, j.valInboxFiles(), "nothing was delivered")
	assert.True(t, j.inInbox("msg-bbbbbbbbbbbb"), "and the new request was not processed")

	// Reopened, the next call finishes A first, says so, and does not send B.
	_, err = j.e.manager().OpenDesktopGrant(j.valSID, time.Hour)
	require.NoError(t, err)
	res = j.reply("msg-bbbbbbbbbbbb", "the answer to B")
	require.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "earlier reply to ask msg-aaaaaaaaaaaa")
	assert.Contains(t, res.Text(), "Your reply to ask msg-bbbbbbbbbbbb was NOT sent")
	assert.NotContains(t, res.Raw, "the answer to A", "what the journal held is never shown")
	assert.True(t, j.archived("msg-aaaaaaaaaaaa"))
	assert.True(t, j.inInbox("msg-bbbbbbbbbbbb"))
	require.Len(t, j.valInboxFiles(), 1)

	res = j.reply("msg-bbbbbbbbbbbb", "the answer to B")
	require.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "Reply delivered")
	assert.Len(t, j.valInboxFiles(), 2)
}

// The other half of C2: a journal already SENT only has its archiving left. That is
// not a delivery, so it is finished even though the bridge is closed, and nothing of
// it is shown: not the text, not the name of the val.
func TestMCPReply_ASentJournalIsArchivedEvenWithTheBridgeClosedAndShowsNothing(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeQuery, "ask A")
	j.put("msg-bbbbbbbbbbbb", j.valSID, message.TypeQuery, "ask B")

	// The crash in the gap: the response reached the val, the record of it was written
	// as sent, the archiving did not happen.
	cfg := j.e.cfg
	mgr := j.e.manager()
	sent := &session.ReplyTxn{
		ResponseID: session.DeterministicResponseID(j.arcSID, "msg-aaaaaaaaaaaa"), To: j.valSID, Anchor: "msg-aaaaaaaaaaaa",
		CloseIDs: []string{"msg-aaaaaaaaaaaa"}, State: session.ReplyTxnPending, Timestamp: j.e.clock.now(), Content: "SECRET-ANSWER-TEXT",
	}
	_, err := deliverResponse(cfg, mgr, j.arcSID, sent)
	require.NoError(t, err)
	sent.State = session.ReplyTxnSent
	require.NoError(t, mgr.WriteReplyTxn(j.arcSID, sent))
	require.NoError(t, mgr.CloseDesktopGrant(j.valSID))
	delivered := j.valInboxFiles()

	res := j.reply("msg-bbbbbbbbbbbb", "the answer to B")
	require.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "only its archiving was left")
	assert.Contains(t, res.Text(), "Your reply to ask msg-bbbbbbbbbbbb was NOT sent")
	for _, hidden := range []string{"SECRET-ANSWER-TEXT", "VAL-demo", "valdemo01"} {
		assert.NotContains(t, res.Raw, hidden)
	}
	assert.True(t, j.archived("msg-aaaaaaaaaaaa"), "the ask the delivered answer closed was archived")
	_, found := j.journal()
	assert.False(t, found)
	assert.Equal(t, delivered, j.valInboxFiles(), "and nothing was delivered a second time")
	assert.True(t, j.inInbox("msg-bbbbbbbbbbbb"))
}

// A retry of the SAME reply finds its own journal: it is finished as first written.
func TestMCPReply_ARetryOfTheSameReplyFinishesTheJournalAsFirstWritten(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeQuery, "ask A")
	selected := false
	_, err := beginReply(j.e.manager(), j.arcSID,
		j.app.replySelector(j.app.lookup(projectKey("/repo/demo", surfaceCowork)), newAuthorizer(j.e.manager(), "/repo/demo", j.e.clock.now()), "msg-aaaaaaaaaaaa", "FIRST WORDING", &selected), nil)
	require.NoError(t, err)

	res := j.reply("msg-aaaaaaaaaaaa", "second wording")
	require.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "went as it was first written")
	files := j.valInboxFiles()
	require.Len(t, files, 1)
	data, err := os.ReadFile(filepath.Join(j.e.dataDir, "sessions", j.valSID, "inbox", files[0]))
	require.NoError(t, err)
	assert.Contains(t, string(data), "FIRST WORDING")
	assert.NotContains(t, string(data), "second wording")
}

// A client that retries a call it never heard back from sends the same reply twice at
// once: one answer is delivered, the others end cleanly.
func TestMCPReply_TheSameReplyTwiceAtOnceDeliversOneAnswer(t *testing.T) {
	j := newJoinedDemo(t)
	for round := 0; round < 10; round++ {
		id := fmt.Sprintf("msg-%012x", round+1)
		j.put(id, j.valSID, message.TypeQuery, "ask")
		const racers = 5
		var wg sync.WaitGroup
		results := make([]toolResult, racers)
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = j.reply(id, "the answer")
			}()
		}
		wg.Wait()
		delivered := 0
		for i, r := range results {
			if !r.IsError {
				delivered++
				continue
			}
			assert.Contains(t, r.Text(), "already answered", "round %d racer %d must end cleanly, got: %s", round, i, r.Text())
		}
		assert.Equal(t, 1, delivered, "round %d: exactly one call did the answering", round)
		assert.True(t, j.archived(id))
		_, found := j.journal()
		assert.False(t, found)
	}
	assert.Len(t, j.valInboxFiles(), 10, "one response per ask, never two")
}

// The routing policy is part of the gate too: the architect's role is fixed when it
// joins, but the manifest can be changed from the CLI, and a session that may not send
// does not send through here either. The notice says routing, not the bridge.
func TestMCPReply_TheRoutingPolicyIsPartOfTheGate(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeQuery, "ask")
	require.NoError(t, j.e.manager().SetRole(j.arcSID, session.RoleObserver))

	res := j.reply("msg-aaaaaaaaaaaa", "an answer")
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "routing policy")
	assert.NotContains(t, res.Text(), "bridge of the val")
	assert.Empty(t, j.valInboxFiles())
	_, found := j.journal()
	assert.False(t, found)
}
