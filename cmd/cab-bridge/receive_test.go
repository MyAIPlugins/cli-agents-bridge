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

// F-48: --emit=content emits only the message body, no JSON envelope.
func TestRunReceive_Any_EmitContent_BodyOnly(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	sid := "rcvemit1"
	plantOverviewSession(t, dataDir, sid, session.RoleVal, "VAL-x", "/repo/x", "", "")
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "esc99999", "ESC-y", message.TypeResponse, "the report body")

	var runErr error
	out := captureStdout(t, func() {
		runErr = runReceive([]string{"--any", "--session-id=" + sid, "--max-deadline=2", "--emit=content"})
	})
	require.NoError(t, runErr)
	assert.Contains(t, out, "the report body")
	assert.NotContains(t, out, "schemaVersion", "content mode emits only the body, no JSON envelope")
}

// F-48: in content mode the timeout envelope is suppressed (it would be JSON in a
// content stream); empty stdout + exit 0 is the timeout signal there.
func TestRunReceive_Any_EmitContentTimeout_EmptyStdout(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	sid := "rcvemit2"
	plantOverviewSession(t, dataDir, sid, session.RoleVal, "VAL-x", "/repo/x", "", "")
	plantMsg(t, dataDir, sid, "inbox", "msg-cccccccccccc", "esc99999", "ESC-y", message.TypeAck, "ack only")

	var runErr error
	out := captureStdout(t, func() {
		runErr = runReceive([]string{"--any", "--session-id=" + sid, "--max-deadline=1", "--emit=content"})
	})
	require.NoError(t, runErr, "timeout still exits 0 in content mode")
	assert.Empty(t, strings.TrimSpace(out), "content mode suppresses the JSON timeout payload")
}

func TestRunReceive_EmitInvalid(t *testing.T) {
	err := runReceive([]string{"--any", "--emit=xml"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--emit")
}

// F-49: --any --unseen ignores a pre-existing pending and times out exit 0; the
// pre-existing message is left in the inbox (a plantMsg's fixed 2026-05-31
// timestamp is older than now, so it is not "new").
func TestRunReceive_UnseenIgnoresPreExisting(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	sid := "rcvuns01"
	plantOverviewSession(t, dataDir, sid, session.RoleVal, "VAL-x", "/repo/x", "", "")
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "esc99999", "ESC-y", message.TypeResponse, "old pending")

	var runErr error
	out := captureStdout(t, func() {
		runErr = runReceive([]string{"--any", "--unseen", "--session-id=" + sid, "--max-deadline=1"})
	})
	require.NoError(t, runErr, "--unseen ignores the pre-existing pending → timeout exit 0")
	assert.Contains(t, out, "\"status\": \"timeout\"")
	assert.Equal(t, 1, countInboxJSON(t, dataDir, sid), "the pre-existing pending stays in inbox (not consumed)")
}

func TestRunReceive_UnseenRequiresAny(t *testing.T) {
	err := runReceive([]string{"--unseen", "--msg-id=msg-aaaaaaaaaaaa"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--unseen requires --any")
}
