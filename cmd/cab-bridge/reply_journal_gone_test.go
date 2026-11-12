package main

import (
	"errors"
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
