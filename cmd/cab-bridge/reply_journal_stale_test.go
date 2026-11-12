package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// F-176: the journal of `reply` is written and deleted by a driver that does not know
// whether the file on disk is still the one it started from. These tests build the order
// of events that matters with the real functions and no seam, one process after the other:
//
//	A and B both resume the journal J1 (beginReply gives each of them its own copy);
//	A finishes J1 and removes it;
//	C starts a reply for ANOTHER ask: the file is J2 now, and C delivers its response and
//	stops before it records SENT (the crash gap the journal exists for);
//	B, still holding J1 in memory, drives it.
//
// No retry is involved, so nothing here is Windows-specific.

const (
	staleAskJ1 = "msg-aaaaaaaaaaaa"
	staleAskJ2 = "msg-bbbbbbbbbbbb"
	j2Payload  = "J2 original frozen payload"
)

// staleScene is the state after the first three steps: B holds J1 and has not run yet.
type staleScene struct {
	mgr   *session.Manager
	cfg   config.Config
	data  string
	b     *session.ReplyTxn // the stale driver's copy of J1
	j2    *session.ReplyTxn
	j2Raw []byte // the bytes of reply-txn.json while J2 is the journal
}

// newStaleScene runs the first three steps. j1State is the state J1 is in when A and B
// resume it: a pending J1 still has to deliver and archive; a sent one whose asks are all
// archived has nothing left but its own removal. withJ2 false stops after A removed J1: the
// file is just gone, and nobody has created another.
func newStaleScene(t *testing.T, j1State string, withJ2 bool) *staleScene {
	t.Helper()
	mgr, cfg, data := newReplyPair(t)

	j1 := newTxn(replySelf, []string{staleAskJ1}, "J1 payload")
	switch j1State {
	case session.ReplyTxnPending:
		deliverAndNotify(t, mgr, data, staleAskJ1, "first ask", j1.Timestamp)
	case session.ReplyTxnSent:
		j1.State = session.ReplyTxnSent
		j1.ArchivedIndex = len(j1.CloseIDs)
	default:
		t.Fatalf("unknown state %q", j1State)
	}
	require.NoError(t, mgr.WriteReplyTxn(replySelf, j1))

	unused := false
	a, err := beginReply(mgr, replySelf, staticSelector(&unused, nil), nil)
	require.NoError(t, err)
	b, err := beginReply(mgr, replySelf, staticSelector(&unused, nil), nil)
	require.NoError(t, err)
	require.False(t, unused, "premise: A and B both resumed the journal, neither built one")
	require.NotSame(t, a, b, "premise: each driver holds its own copy")
	require.Equal(t, j1State, b.State, "premise: B starts from the state J1 was in")

	// A finishes J1 and removes it.
	require.NoError(t, driveReplyTxn(mgr, cfg, replySelf, a, func(string, ...any) {}))
	_, found, rerr := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, rerr)
	require.False(t, found, "premise: A removed J1, no journal is on disk")
	if !withJ2 {
		return &staleScene{mgr: mgr, cfg: cfg, data: data, b: b}
	}

	// C builds J2 for another ask, delivers it, and stops at the crash gap.
	deliverAndNotify(t, mgr, data, staleAskJ2, "second ask", j1.Timestamp)
	selected := false
	j2, err := beginReply(mgr, replySelf, staticSelector(&selected, newTxn(replySelf, []string{staleAskJ2}, j2Payload)), nil)
	require.NoError(t, err)
	require.True(t, selected, "premise: C created a journal of its own")
	require.NotEqual(t, b.ResponseID, j2.ResponseID, "premise: J2 is another transaction")
	delivered, derr := deliverResponse(cfg, mgr, replySelf, j2)
	require.NoError(t, derr)
	require.True(t, delivered, "premise: C's response is in the recipient's inbox")
	onDisk, found, rerr := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, rerr)
	require.True(t, found)
	require.Equal(t, session.ReplyTxnPending, onDisk.State, "premise: C stopped before recording SENT")
	require.Equal(t, j2.ResponseID, onDisk.ResponseID)
	raw, rawErr := os.ReadFile(replyJournalPath(data))
	require.NoError(t, rawErr)

	return &staleScene{mgr: mgr, cfg: cfg, data: data, b: b, j2: j2, j2Raw: raw}
}

// driveB is the step under test: B drives its copy of J1. It returns what B said through its
// note and the error, so a test can read the words as the CLI prints them.
func (s *staleScene) driveB() (notes []string, err error) {
	note := func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }
	err = driveReplyTxn(s.mgr, s.cfg, replySelf, s.b, note)
	return notes, err
}

// assertJ2Untouched is the criterion: what B did, it did not do to a journal that is not
// its own. It reports what is on disk instead, so a red run says which half it was.
func (s *staleScene) assertJ2Untouched(t *testing.T) {
	t.Helper()
	got, err := os.ReadFile(replyJournalPath(s.data))
	if os.IsNotExist(err) {
		t.Errorf("J2 is gone: B removed a journal that was not its own")
		return
	}
	require.NoError(t, err)
	if string(got) != string(s.j2Raw) {
		cur, _, _ := s.mgr.ReadReplyTxn(replySelf)
		t.Errorf("J2 was overwritten by B: on disk now responseId=%s state=%s (J2 is %s)", cur.ResponseID, cur.State, s.j2.ResponseID)
	}
}

// assertAskStillAnswerable is the consequence, the way the user meets it: C died after
// delivering, the session replies again. With J2 on disk the next reply resumes it; with
// J2 lost it builds the same response id from other bytes, and the delivery refuses it.
func (s *staleScene) assertAskStillAnswerable(t *testing.T) {
	t.Helper()
	next, err := beginReply(s.mgr, replySelf, cliReplySelector(s.mgr, s.cfg, replySelf, []string{"different text after restart"}, nil), nil)
	require.NoError(t, err)
	assert.NoError(t, driveReplyTxn(s.mgr, s.cfg, replySelf, next, func(string, ...any) {}),
		"the next reply of the session cannot finish")
	delivered := readDelivered(t, s.data, s.j2.ResponseID)
	assert.Equal(t, message.TypeResponse, delivered.Type)
	assert.Equal(t, j2Payload, delivered.Content, "the response that was delivered keeps C's payload")
	_, statErr := os.Stat(filepath.Join(s.data, "sessions", replySelf, "inbox", staleAskJ2+".json"))
	assert.True(t, os.IsNotExist(statErr), "the ask C answered is still in the inbox: it stays open")
}

// TestReplyJournal_AStaleDriverDoesNotRemoveAnotherTransactionsJournal is the DELETE half:
// B has nothing left to do but remove its journal, and it removes the name, which now
// belongs to J2. B's reply IS done (it was delivered and archived by A), so B succeeds, says
// that the journal on disk is another reply's, and leaves it.
func TestReplyJournal_AStaleDriverDoesNotRemoveAnotherTransactionsJournal(t *testing.T) {
	s := newStaleScene(t, session.ReplyTxnSent, true)
	notes, err := s.driveB()
	require.NoError(t, err, "B's own reply is complete: a journal that is not its own is not a failure of it")
	assert.Contains(t, strings.Join(notes, "\n"), "belongs to another reply and was left as it was")
	s.assertJ2Untouched(t)
	s.assertAskStillAnswerable(t)
}

// TestReplyJournal_AStaleDriverDoesNotRewriteAnotherTransactionsJournal is the REWRITE
// half: B starts from a pending J1, so its first write records SENT with J1's content,
// under the lock and over J2. It must stop there, with an error that says what the check did.
func TestReplyJournal_AStaleDriverDoesNotRewriteAnotherTransactionsJournal(t *testing.T) {
	s := newStaleScene(t, session.ReplyTxnPending, true)
	_, err := s.driveB()
	require.Error(t, err, "B was overtaken and must say so")
	assertTruthfulJournalError(t, err)
	s.assertJ2Untouched(t)
	s.assertAskStillAnswerable(t)
}

// assertTruthfulJournalError holds the words of the error to what is true: the missing write is
// the check's, the command as a whole may already have done things, and nobody is blamed.
func assertTruthfulJournalError(t *testing.T, err error) {
	t.Helper()
	text := err.Error()
	assert.Contains(t, text, "this check", "the missing write is attributed to the check")
	assert.Contains(t, text, "may already have been delivered", "effects that came before are not denied")
	assert.NotContains(t, text, "nothing else")
	assert.NotContains(t, strings.ToLower(text), "removed by")
}
