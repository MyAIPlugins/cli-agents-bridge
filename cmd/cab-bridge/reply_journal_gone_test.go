package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// F-176, the other outcomes of a driver that finds the journal is not its own, on the scenes of
// reply_journal_stale_test.go. These use the sentinel errors of the fix, so they live apart from
// the two reproductions, which compile on the tree before the fix.

// The same two steps when the journal is not another transaction's but simply gone (A removed
// it and nobody created another). Writing progress must not recreate it, and the end of a
// reply that is already complete is a success with a note.
func TestReplyJournal_AStaleDriverDoesNotRecreateAJournalThatIsGone(t *testing.T) {
	s := newStaleScene(t, session.ReplyTxnPending, false)
	_, err := s.driveB()
	require.Error(t, err, "B was overtaken and must say so")
	assert.True(t, errors.Is(err, session.ErrReplyTxnGone), "got: %v", err)
	assertTruthfulJournalError(t, err)
	assert.NoFileExists(t, replyJournalPath(s.data), "the journal was not recreated")
}

func TestReplyJournal_ACompleteReplyWhoseJournalIsAlreadyGoneIsASuccessWithANote(t *testing.T) {
	s := newStaleScene(t, session.ReplyTxnSent, false)
	notes, err := s.driveB()
	require.NoError(t, err)
	assert.Contains(t, strings.Join(notes, "\n"), "already gone at the final check")
	assert.NoFileExists(t, replyJournalPath(s.data), "nothing was created")
}

// The sentinels survive the wrapping of the driver, so a front-end can phrase them in its own
// words (the MCP does, and names nobody).
func TestReplyJournal_AnOvertakenDriverReturnsTheSentinelOfWhatItFound(t *testing.T) {
	notYours := newStaleScene(t, session.ReplyTxnPending, true)
	_, err := notYours.driveB()
	require.Error(t, err)
	assert.True(t, errors.Is(err, session.ErrReplyTxnNotYours), "got: %v", err)
	assert.False(t, errors.Is(err, session.ErrReplyTxnGone))
	assert.True(t, strings.HasPrefix(err.Error(), "reply: "), "it is the driver's error: %v", err)
}

// A driver that finds the journal is not its own STOPS there. The scenes above are masked on
// purpose by an accident of the data: the other driver had already archived everything, so a
// driver that went on after the check would archive nothing and meet the next check, which
// stops it anyway. Here the asks are still in the inbox, so the only thing that keeps them
// there is the stop itself. The journal is made gone, or another reply's, by hand: what is
// being asked is what the driver does with a copy it can no longer trust, not how it got there.
func overtakenBy(t *testing.T, how string, data string, mgr *session.Manager) (check func(), wantErr error) {
	t.Helper()
	path := replyJournalPath(data)
	switch how {
	case "gone":
		require.NoError(t, os.Remove(path))
		return func() { assert.NoFileExists(t, path, "the journal was not recreated") }, session.ErrReplyTxnGone
	case "another reply's":
		j2 := newTxn(replySelf, []string{"msg-cccccccccccc"}, "J2 payload")
		require.NoError(t, mgr.WriteReplyTxn(replySelf, j2))
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		return func() {
			got, rerr := os.ReadFile(path)
			require.NoError(t, rerr)
			assert.Equal(t, string(raw), string(got), "the other reply's journal is byte for byte what it was")
		}, session.ErrReplyTxnNotYours
	}
	t.Fatalf("unknown way %q", how)
	return nil, nil
}

// At the first write (the one that records SENT): the response was delivered, then the check
// failed, and nothing is archived.
func TestReplyJournal_AnOvertakenDriverStopsAtTheFirstWriteAndArchivesNothing(t *testing.T) {
	for _, how := range []string{"gone", "another reply's"} {
		t.Run(how, func(t *testing.T) {
			mgr, cfg, data := newReplyPair(t)
			j1 := newTxn(replySelf, []string{"msg-aaaaaaaaaaaa"}, "J1 payload")
			deliverAndNotify(t, mgr, data, "msg-aaaaaaaaaaaa", "first ask", j1.Timestamp)
			require.NoError(t, mgr.WriteReplyTxn(replySelf, j1))
			mine := *j1
			require.Equal(t, session.ReplyTxnPending, mine.State, "premise: the driver starts pending, so its first write records SENT")

			check, wantErr := overtakenBy(t, how, data, mgr)
			err := driveReplyTxn(mgr, cfg, replySelf, &mine, func(string, ...any) {})

			require.Error(t, err)
			assert.True(t, errors.Is(err, wantErr), "got: %v", err)
			assert.FileExists(t, filepath.Join(data, "sessions", replySelf, "inbox", "msg-aaaaaaaaaaaa.json"), "the ask was not archived by a driver that was told to stop")
			assert.Equal(t, 0, askArchivedCount(t, data, "msg-aaaaaaaaaaaa"))
			check()
		})
	}
}

// At the write that records the archiving: the first ask was archived (that step ran before
// the check), the check failed, and the second is not touched.
func TestReplyJournal_AnOvertakenDriverStopsAtTheArchiveWriteAndDoesNotGoOn(t *testing.T) {
	for _, how := range []string{"gone", "another reply's"} {
		t.Run(how, func(t *testing.T) {
			mgr, cfg, data := newReplyPair(t)
			j1 := newTxn(replySelf, []string{"msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb"}, "J1 payload")
			j1.State = session.ReplyTxnSent
			deliverAndNotify(t, mgr, data, "msg-aaaaaaaaaaaa", "first ask", j1.Timestamp)
			deliverAndNotify(t, mgr, data, "msg-bbbbbbbbbbbb", "second ask", j1.Timestamp)
			require.NoError(t, mgr.WriteReplyTxn(replySelf, j1))
			mine := *j1

			check, wantErr := overtakenBy(t, how, data, mgr)
			err := driveReplyTxn(mgr, cfg, replySelf, &mine, func(string, ...any) {})

			require.Error(t, err)
			assert.True(t, errors.Is(err, wantErr), "got: %v", err)
			assert.Equal(t, 1, askArchivedCount(t, data, "msg-aaaaaaaaaaaa"), "the step that ran before the check is done")
			assert.FileExists(t, filepath.Join(data, "sessions", replySelf, "inbox", "msg-bbbbbbbbbbbb.json"), "and the driver did not go on to the second ask")
			check()
		})
	}
}
