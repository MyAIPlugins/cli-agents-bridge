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
