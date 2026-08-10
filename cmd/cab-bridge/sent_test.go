package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// --- v0.8 §2.4: honest states -----------------------------------------------

// TestSent_StatesAreNamedForWhatTheyProve covers the five states of §2.4 in one
// mailbox. The names matter as much as the values: `archived` proves a reply
// moved a file, NOT that the work is finished, and none of them attests
// completion — which is why the old single word "gone" was wrong, collapsing
// five distinct causes into one.
func TestSent_StatesAreNamedForWhatTheyProve(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)

	const me, peer = "sndme001", "sndpeer1"
	plantOverviewSession(t, dataDir, me, session.RoleEsc, "ESC-s", "/repo/s", "", "working")
	plantOverviewSession(t, dataDir, peer, session.RoleVal, "VAL-s", "/repo/s", "", session.StateOrchestrating)

	now := time.Now().UTC()
	plantInboxAt(t, dataDir, peer, "msg-aaaaaaaaaaaa", me, message.TypeQuery, "never shown", now)
	plantInboxAt(t, dataDir, peer, "msg-bbbbbbbbbbbb", me, message.TypeQuery, "delivered to a next", now)
	_, err := mgr.CommitWakeCursor(peer, []string{"msg-bbbbbbbbbbbb"}, now, nil, nil)
	require.NoError(t, err)

	// Archived: a file in the recipient's processed/ with the timestamped name.
	processed := filepath.Join(dataDir, "sessions", peer, "processed")
	require.NoError(t, os.MkdirAll(processed, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(processed, "20260808T120000.000000000Z-msg-cccccccccccc.json"), []byte("{}"), 0o600))

	rows := []sentSummary{
		{MsgID: "msg-aaaaaaaaaaaa", To: peer},
		{MsgID: "msg-bbbbbbbbbbbb", To: peer},
		{MsgID: "msg-cccccccccccc", To: peer},
		{MsgID: "msg-dddddddddddd", To: peer},       // session there, message gone
		{MsgID: "msg-eeeeeeeeeeee", To: "ghost001"}, // session gone
	}
	require.NoError(t, annotateSentStates(cfg, mgr, rows))

	assert.Equal(t, sentStateUnread, rows[0].State, "in their inbox, never handed to a next")
	assert.Equal(t, sentStateNotified, rows[1].State, "handed to a next of theirs")
	assert.Equal(t, sentStateArchived, rows[2].State, "their reply closed it")
	assert.Equal(t, sentStateExpired, rows[3].State, "session alive, message pruned by retention")
	assert.Equal(t, sentStateUnknown, rows[4].State, "the session itself is gone")
}

// TestSent_ScansEachRecipientOnce is the §2.4 cost requirement: the naive
// per-message lookup is O(sent x mailbox), and archived files must be searched
// by name rather than decoded.
func TestSent_ScansEachRecipientOnce(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)

	const me, peer = "sndme002", "sndpeer2"
	plantOverviewSession(t, dataDir, me, session.RoleEsc, "ESC-s2", "/repo/s2", "", "working")
	plantOverviewSession(t, dataDir, peer, session.RoleVal, "VAL-s2", "/repo/s2", "", session.StateOrchestrating)

	now := time.Now().UTC()
	rows := make([]sentSummary, 0, 20)
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("msg-%012d", i)
		plantInboxAt(t, dataDir, peer, id, me, message.TypeQuery, "body", now)
		rows = append(rows, sentSummary{MsgID: id, To: peer})
	}

	// One index for the recipient answers all twenty rows.
	index, err := buildMailboxIndex(cfg, mgr, peer)
	require.NoError(t, err)
	require.Len(t, index, 20, "one scan indexes the whole mailbox")

	require.NoError(t, annotateSentStates(cfg, mgr, rows))
	for _, r := range rows {
		assert.Equal(t, sentStateUnread, r.State)
	}
}

func TestArchivedID(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"20260808T120000.000000000Z-msg-aaaaaaaaaaaa.json": "msg-aaaaaaaaaaaa",
		"msg-bbbbbbbbbbbb.json":                            "msg-bbbbbbbbbbbb",
		"not-a-message.json":                               "",
		"":                                                 "",
	}
	for name, want := range cases {
		assert.Equal(t, want, archivedID(name), name)
	}
}

// TestSent_ReQueuedIsItsOwnState: an ask put back in line by a reply (F-109) or
// by a join must NOT travel backwards to `unread`.
//
// `unread` would have been literally true — that is where the file sits — and
// misleading in the one way that matters: a sender watching the column sees a
// message they were told was delivered become undelivered, with nothing saying
// why. The cursor already knows the difference, so the honest state was free.
func TestSent_ReQueuedIsItsOwnState(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)

	const me, peer = "sndme002", "sndpeer2"
	plantOverviewSession(t, dataDir, me, session.RoleEsc, "ESC-r", "/repo/r", "", "working")
	plantOverviewSession(t, dataDir, peer, session.RoleVal, "VAL-r", "/repo/r", "", session.StateOrchestrating)

	now := time.Now().UTC()
	plantInboxAt(t, dataDir, peer, "msg-aaaaaaaaaaaa", me, message.TypeQuery, "never shown", now)
	plantInboxAt(t, dataDir, peer, "msg-bbbbbbbbbbbb", me, message.TypeQuery, "shown, then left open", now)
	_, err := mgr.CommitWakeCursor(peer, []string{"msg-bbbbbbbbbbbb"}, now, nil, nil)
	require.NoError(t, err)

	index, err := buildMailboxIndex(cfg, mgr, peer)
	require.NoError(t, err)
	require.Equal(t, sentStateNotified, index["msg-bbbbbbbbbbbb"])

	// Their reply left it open, so it went back in line.
	require.NoError(t, mgr.ForgetNotified(peer, []string{"msg-bbbbbbbbbbbb"}))

	index, err = buildMailboxIndex(cfg, mgr, peer)
	require.NoError(t, err)
	assert.Equal(t, sentStateRequeued, index["msg-bbbbbbbbbbbb"], "delivered once, on its way again — not undelivered")
	assert.Equal(t, sentStateUnread, index["msg-aaaaaaaaaaaa"], "a genuinely unseen one keeps its own state")
}

// TestSent_ArchivedDoesNotClaimAReply pins what `archived` is allowed to mean.
//
// A `tell` archived by `inbox --tidy` reaches processed/ exactly like a query
// closed by a reply, and the index cannot tell them apart. So the word must not
// promise the cause: it used to be documented as "closed by their reply", which
// is false for EVERY tell (that verb expects no reply, so none can exist) and,
// worse, for an ask the other side tidied without answering — the sender would
// read "they replied" about an answer that never came.
func TestSent_ArchivedDoesNotClaimAReply(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)

	const me, peer = "sndme003", "sndpeer3"
	plantOverviewSession(t, dataDir, me, session.RoleVal, "VAL-t", "/repo/t", "", session.StateOrchestrating)
	plantOverviewSession(t, dataDir, peer, session.RoleEsc, "ESC-t", "/repo/t", "", "working")

	// A tell, delivered and then tidied away — never replied to, and unreplyable.
	now := time.Now().UTC()
	plantInboxAt(t, dataDir, peer, "msg-aaaaaaaaaaaa", me, message.TypeNotify, "a brief", now)
	_, err := mgr.CommitWakeCursor(peer, []string{"msg-aaaaaaaaaaaa"}, now, nil, nil)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "sessions", peer, "processed"), 0o700))
	require.NoError(t, os.Rename(
		filepath.Join(dataDir, "sessions", peer, "inbox", "msg-aaaaaaaaaaaa.json"),
		filepath.Join(dataDir, "sessions", peer, "processed", "20260810-msg-aaaaaaaaaaaa.json")))

	index, err := buildMailboxIndex(cfg, mgr, peer)
	require.NoError(t, err)
	require.Equal(t, sentStateArchived, index["msg-aaaaaaaaaaaa"], "out of their inbox is what the state knows")

	// And the gloss must not attribute it to a reply that cannot exist.
	out := captureStdout(t, func() {
		t.Setenv("CAB_DATA_DIR", dataDir)
		require.NoError(t, runSent([]string{"--session-id=" + me}))
	})
	assert.NotContains(t, out, "closed by their reply", "a tell is archived without any reply")
	assert.Contains(t, out, "out of their inbox")
}
