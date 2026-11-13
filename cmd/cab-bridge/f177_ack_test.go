//go:build !windows

package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// F-177, the `ack` of Claude Desktop. It archives from the inbox of the architect session the MCP
// server joined, and until now it did it without that session's lock. The server runs in this
// process, so whoever else holds the lock is a child (f177_helper_test.go).

// newJoinedDemoWaiting is newJoinedDemo with the wait for the session lock set by the config, the
// way a real server gets it, before the app is built: the manager is not touched afterwards, so
// nothing races with the heartbeat.
func newJoinedDemoWaiting(t *testing.T, lockWaitMs int) *joinedDemo {
	t.Helper()
	e := newMCPEnv(t)
	e.cfg.SessionLockWaitMs = lockWaitMs
	e.val("valdemo01", "VAL-demo", "/repo/demo", 8*time.Hour)
	app, c := e.start(coworkClient)
	res := c.call("join", map[string]any{"project": "demo"})
	require.False(t, res.IsError, res.Text())
	arcs := architects(t, e)
	require.Len(t, arcs, 1)
	j := &joinedDemo{e: e, app: app, c: c, valSID: "valdemo01", arcSID: arcs[0].SessionID,
		inbox: filepath.Join(f177SessionDir(e.dataDir, arcs[0].SessionID), "inbox"), project: "demo"}
	// No heartbeat of this process is in flight while another process holds the lock.
	j.stopHeartbeats()
	return j
}

// A4: the lock is another process's. ack says it did not run, in the words of a tool result an
// agent acts on, and the message stays in the inbox. After the release the same ack goes through.
func TestMCPAck_WhileAnotherProcessHoldsTheSessionLockItDoesNotRunAndSaysSo(t *testing.T) {
	j := newJoinedDemoWaiting(t, 150)
	j.put(f177Tell, j.valSID, message.TypeNotify, "a tell")
	h := holdLock(t, j.e.dataDir, j.arcSID)
	requireHeldByAnother(t, j.e.dataDir, j.arcSID)

	res := j.ack(f177Tell)

	assert.True(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "Not done")
	assert.Contains(t, res.Text(), "session lock was still held")
	assert.Contains(t, res.Text(), "Nothing was moved")
	assert.Contains(t, res.Text(), f177Tell+": not-attempted", "one line per id, as for every other refusal of ack")
	assert.True(t, j.inInbox(f177Tell))
	assert.False(t, j.archived(f177Tell))

	h.release()
	res = j.ack(f177Tell)
	require.False(t, res.IsError, res.Text())
	assert.True(t, j.archived(f177Tell), "the retry goes through once the lock is free")
	assert.False(t, j.inInbox(f177Tell))
}

// A6 (ack half): a lock that cannot be opened is not "busy": the text does not tell the agent that
// waiting will help, and nothing moves.
func TestMCPAck_ALockThatCannotBeOpenedIsNotPresentedAsABusyOne(t *testing.T) {
	j := newJoinedDemoWaiting(t, 150)
	j.put(f177Tell, j.valSID, message.TypeNotify, "a tell")
	breakLockFile(t, j.e.dataDir, j.arcSID)

	res := j.ack(f177Tell)

	assert.True(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "could not be taken")
	assert.Contains(t, res.Text(), "not a wait that will pass")
	assert.NotContains(t, res.Text(), "still held", "it is not the busy case, and must not read like it")
	assert.True(t, j.inInbox(f177Tell))
	assert.False(t, j.archived(f177Tell))
}
