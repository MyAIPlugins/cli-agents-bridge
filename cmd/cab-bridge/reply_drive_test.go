package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

type noteLog struct{ lines []string }

func (n *noteLog) note(format string, args ...any) {
	n.lines = append(n.lines, fmt.Sprintf(format, args...))
}

// driveReplyTxn does the delivering and the archiving and says nothing of its own:
// a front-end that must not show what a journal holds drives it and writes its words.
func TestDriveReplyTxn_DeliversAndArchivesWithoutSayingAnythingItself(t *testing.T) {
	mgr, cfg, dataDir := newReplyPair(t)
	plantInboxAt(t, dataDir, replySelf, "msg-aaaaaaaaaaaa", replyPeer, message.TypeQuery, "the brief", time.Now().UTC())
	txn := newTxn(replySelf, []string{"msg-aaaaaaaaaaaa"}, "the answer")
	require.NoError(t, mgr.WriteReplyTxn(replySelf, txn))

	var log noteLog
	origOut, origErr := os.Stdout, os.Stderr
	defer func() { os.Stdout, os.Stderr = origOut, origErr }()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout, os.Stderr = w, w

	derr := driveReplyTxn(mgr, cfg, replySelf, txn, log.note)

	require.NoError(t, w.Close())
	os.Stdout, os.Stderr = origOut, origErr
	leaked := new(bytes.Buffer)
	_, _ = leaked.ReadFrom(r)
	require.NoError(t, derr)

	assert.Empty(t, log.lines, "a delivery that was not a recovery has nothing to note")
	assert.Empty(t, leaked.String(), "and nothing was printed on the process's own streams")
	assert.Equal(t, "the answer", readDelivered(t, dataDir, txn.ResponseID).Content)
	assert.NoFileExists(t, filepath.Join(dataDir, "sessions", replySelf, "inbox", "msg-aaaaaaaaaaaa.json"), "the ask was archived")
	_, found, rerr := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, rerr)
	assert.False(t, found, "and the journal is gone")
}

// A journal found already SENT is a recovery, and it is said only through note, in
// the words the CLI has always used: finishReplyTxn routes them to stderr unchanged.
func TestDriveReplyTxn_ARecoveryIsSaidThroughNoteAndTheCLIKeepsItsStderr(t *testing.T) {
	mgr, cfg, dataDir := newReplyPair(t)
	plantInboxAt(t, dataDir, replySelf, "msg-aaaaaaaaaaaa", replyPeer, message.TypeQuery, "the brief", time.Now().UTC())
	txn := newTxn(replySelf, []string{"msg-aaaaaaaaaaaa"}, "already delivered text")
	delivered, err := deliverResponse(cfg, mgr, replySelf, txn)
	require.NoError(t, err)
	require.True(t, delivered)
	txn.State = session.ReplyTxnSent
	require.NoError(t, mgr.WriteReplyTxn(replySelf, txn))
	before := peerInbox(t, dataDir)

	var log noteLog
	require.NoError(t, driveReplyTxn(mgr, cfg, replySelf, txn, log.note))
	require.Len(t, log.lines, 1)
	assert.Contains(t, log.lines[0], "resuming an interrupted reply")
	assert.Contains(t, log.lines[0], txn.ResponseID)
	assert.NotContains(t, log.lines[0], "already delivered text", "a note carries ids, never content")
	assert.Equal(t, before, peerInbox(t, dataDir), "and nothing was delivered a second time")

	// The CLI path on an identical situation prints that very line on stderr.
	plantInboxAt(t, dataDir, replySelf, "msg-bbbbbbbbbbbb", replyPeer, message.TypeQuery, "another brief", time.Now().UTC())
	txn2 := newTxn(replySelf, []string{"msg-bbbbbbbbbbbb"}, "second answer")
	_, err = deliverResponse(cfg, mgr, replySelf, txn2)
	require.NoError(t, err)
	txn2.State = session.ReplyTxnSent
	require.NoError(t, mgr.WriteReplyTxn(replySelf, txn2))
	var stdout, stderr bytes.Buffer
	require.NoError(t, finishReplyTxn(mgr, cfg, replySelf, txn2, &stdout, &stderr))
	assert.True(t, strings.HasPrefix(stderr.String(), "reply: resuming an interrupted reply — response "+txn2.ResponseID), stderr.String())
	assert.Contains(t, stdout.String(), "second answer", "and the echo is still the CLI's")
}

// A pending journal whose response is already in the recipient's inbox (the crash was
// between the delivery and the record of it) finishes without delivering again, and
// says that through note too.
func TestDriveReplyTxn_APendingJournalWhoseResponseIsThereSaysSo(t *testing.T) {
	mgr, cfg, dataDir := newReplyPair(t)
	plantInboxAt(t, dataDir, replySelf, "msg-aaaaaaaaaaaa", replyPeer, message.TypeQuery, "the brief", time.Now().UTC())
	txn := newTxn(replySelf, []string{"msg-aaaaaaaaaaaa"}, "delivered before the crash")
	_, err := deliverResponse(cfg, mgr, replySelf, txn)
	require.NoError(t, err)
	require.NoError(t, mgr.WriteReplyTxn(replySelf, txn)) // still pending
	before := peerInbox(t, dataDir)

	var log noteLog
	require.NoError(t, driveReplyTxn(mgr, cfg, replySelf, txn, log.note))
	require.Len(t, log.lines, 1)
	assert.Contains(t, log.lines[0], "was already delivered")
	assert.Equal(t, before, peerInbox(t, dataDir))
}
