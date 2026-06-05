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
	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// TestTwoListeners_SecondClaimEvictsFirst is B-2 test 4 end-to-end across the
// session (ClaimListener) and transport (DrainInboxOnceOwned) layers, without a
// subprocess: a second claim (a new instance) supersedes the first, so a message
// is consumed by exactly ONE listener — the new owner — while the evicted first
// consumes nothing and leaves it in inbox.
func TestTwoListeners_SecondClaimEvictsFirst(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	const sid = "twolsn01"
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "sessions", sid), 0o700))
	mgr := session.NewManager(dataDir, time.Second)

	o1, err := mgr.ClaimListener(sid)
	require.NoError(t, err)
	ownerOK1 := func() bool { return mgr.IsListenerCurrent(sid, o1.Token) }

	// A new instance claims → evicts the first.
	o2, err := mgr.ClaimListener(sid)
	require.NoError(t, err)
	ownerOK2 := func() bool { return mgr.IsListenerCurrent(sid, o2.Token) }

	inbox := filepath.Join(dataDir, "sessions", sid, "inbox")
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valsess0", message.TypeQuery, "brief", time.Now().UTC())

	// The evicted first listener consumes nothing — the message stays in inbox.
	msgs1, err := transportfs.DrainInboxOnceOwned(inbox, 65536, ownerOK1)
	require.NoError(t, err)
	assert.Empty(t, msgs1, "the evicted first listener must consume nothing")

	// The current (second) listener consumes it.
	msgs2, err := transportfs.DrainInboxOnceOwned(inbox, 65536, ownerOK2)
	require.NoError(t, err)
	require.Len(t, msgs2, 1, "the current listener consumes the message exactly once")
	assert.Equal(t, "msg-aaaaaaaaaaaa", msgs2[0].ID)
}
