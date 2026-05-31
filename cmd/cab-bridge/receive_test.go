package main

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// Mode validation runs before any filesystem access, so these need no setup.
func TestRunReceive_AnyAndMsgID_MutuallyExclusive(t *testing.T) {
	err := runReceive([]string{"--any", "--msg-id=msg-aaaaaaaaaaaa"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestRunReceive_NeitherAnyNorMsgID(t *testing.T) {
	err := runReceive([]string{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--msg-id")
}

// TestRunReceive_Any_DrainsBatchAndRecordsLastConsumed: --any emits the non-ack
// message, leaves the ack, and records LastConsumed (F-12) for the drained msg.
func TestRunReceive_Any_DrainsBatchAndRecordsLastConsumed(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	sid := "rcvany01"
	plantOverviewSession(t, dataDir, sid, session.RoleVal, "VAL-x", "/repo/x", "", "")
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "esc99999", "ESC-y", message.TypeResponse, "report")
	plantMsg(t, dataDir, sid, "inbox", "msg-bbbbbbbbbbbb", "esc99999", "ESC-y", message.TypeAck, "ack")

	var runErr error
	out := captureStdout(t, func() {
		runErr = runReceive([]string{"--any", "--session-id=" + sid, "--max-deadline=2"})
	})
	require.NoError(t, runErr)
	assert.Contains(t, out, "msg-aaaaaaaaaaaa", "non-ack message drained and emitted")
	assert.False(t, strings.Contains(out, "msg-bbbbbbbbbbbb"), "ack must not be drained/emitted")

	mgr := session.NewManager(dataDir, time.Second)
	mf, err := mgr.LoadManifest(sid)
	require.NoError(t, err)
	assert.Equal(t, "msg-aaaaaaaaaaaa", mf.LastConsumedMsgID, "F-12: last drained non-ack recorded")
}

// TestRunReceive_Any_TimeoutExitsZeroWithPayload: an --any window that expires on
// an inbox of only acks exits 0 (NOT 124) with a {"status":"timeout"} payload.
func TestRunReceive_Any_TimeoutExitsZeroWithPayload(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	sid := "rcvany02"
	plantOverviewSession(t, dataDir, sid, session.RoleVal, "VAL-x", "/repo/x", "", "")
	plantMsg(t, dataDir, sid, "inbox", "msg-cccccccccccc", "esc99999", "ESC-y", message.TypeAck, "ack only")

	var runErr error
	out := captureStdout(t, func() {
		runErr = runReceive([]string{"--any", "--session-id=" + sid, "--max-deadline=1"})
	})
	require.NoError(t, runErr, "--any timeout must exit 0 (err nil), unlike --msg-id which is 124")
	assert.Contains(t, out, "\"status\": \"timeout\"")
}
