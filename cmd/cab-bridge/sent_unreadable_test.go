package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// F-172 on `sent`: for the sender, a message that is in the recipient's inbox and that
// the system will not let be read, and one that was read and does not decode, are the
// same thing, "unreadable": it is there and it will not arrive (the BAR leaves `sent`
// as it is). A file that is GONE is neither: it ends where it always ended.
func TestSent_AnUnreadableOrCorruptFileIsUnreadableAndAGoneOneIsArchivedOrExpired(t *testing.T) {
	dataDir := t.TempDir()
	cfg := fixtureConfig(t, dataDir)
	mgr := newSessionManager(cfg)
	const peer = "sndpeer1"
	plantOverviewSession(t, dataDir, peer, session.RoleVal, "VAL-s", "/repo/s", "", session.StateOrchestrating)

	now := time.Now().UTC()
	plantInboxAt(t, dataDir, peer, "msg-aaaaaaaaaaaa", "sndme001", message.TypeQuery, "fine", now)
	plantDenied(t, dataDir, peer, "msg-bbbbbbbbbbbb")
	inbox := filepath.Join(dataDir, "sessions", peer, "inbox")
	require.NoError(t, os.WriteFile(filepath.Join(inbox, "msg-cccccccccccc.json"), []byte("{nope"), 0o600))

	// Gone from the inbox: one has its copy in processed/, one is nowhere.
	processed := filepath.Join(dataDir, "sessions", peer, "processed")
	require.NoError(t, os.MkdirAll(processed, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(processed, "20260808T120000.000000000Z-msg-dddddddddddd.json"), []byte("{}"), 0o600))

	rows := []sentSummary{
		{MsgID: "msg-aaaaaaaaaaaa", To: peer},
		{MsgID: "msg-bbbbbbbbbbbb", To: peer},
		{MsgID: "msg-cccccccccccc", To: peer},
		{MsgID: "msg-dddddddddddd", To: peer},
		{MsgID: "msg-eeeeeeeeeeee", To: peer},
	}
	require.NoError(t, annotateSentStates(cfg, mgr, rows))

	assert.Equal(t, sentStateUnread, rows[0].State, "the control: a readable message")
	assert.Equal(t, sentStateUnreadable, rows[1].State, "the system will not let it be read: it is there and will not arrive")
	assert.Equal(t, sentStateUnreadable, rows[2].State, "read and not decodable: the same word for the sender")
	assert.Equal(t, sentStateArchived, rows[3].State, "gone from the inbox with a copy in processed/")
	assert.Equal(t, sentStateExpired, rows[4].State, "gone and nowhere else: what retention would have done")
}
