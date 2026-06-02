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

// TestResolveMaxBlocking covers the F-26 window precedence: --until-deadline
// flag > CAB_MAX_BLOCKING_SECONDS env (cfgSeconds) > 540s default, plus the
// invalid/non-positive flag errors.
func TestResolveMaxBlocking(t *testing.T) {
	t.Parallel()

	t.Run("flag parses and sets the window", func(t *testing.T) {
		d, err := resolveMaxBlocking("10s", 0)
		require.NoError(t, err)
		assert.Equal(t, 10*time.Second, d)
	})

	t.Run("flag wins over env (precedence flag>env)", func(t *testing.T) {
		d, err := resolveMaxBlocking("2s", 100)
		require.NoError(t, err)
		assert.Equal(t, 2*time.Second, d, "--until-deadline must win over CAB_MAX_BLOCKING_SECONDS")
	})

	t.Run("no flag uses env seconds", func(t *testing.T) {
		d, err := resolveMaxBlocking("", 120)
		require.NoError(t, err)
		assert.Equal(t, 120*time.Second, d)
	})

	t.Run("no flag, no env falls back to 540s", func(t *testing.T) {
		d, err := resolveMaxBlocking("", 0)
		require.NoError(t, err)
		assert.Equal(t, 9*time.Minute, d)
	})

	t.Run("invalid duration is a clear error", func(t *testing.T) {
		_, err := resolveMaxBlocking("2hh", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--until-deadline", "error must name the flag")
		assert.Contains(t, err.Error(), "2hh", "error must echo the bad value")
	})

	t.Run("non-positive duration is rejected", func(t *testing.T) {
		_, err := resolveMaxBlocking("0s", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "positive")

		_, err = resolveMaxBlocking("-5m", 0)
		require.Error(t, err)
	})
}

// F-48: listen --wait-one --emit=content delivers only the body of the batch.
func TestRunListen_WaitOne_EmitContent_BodyOnly(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	sid := "lsnemit1"
	plantOverviewSession(t, dataDir, sid, session.RoleEsc, "ESC-y", "/repo/x", "", "")
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val12345", "VAL-x", message.TypeResponse, "wake body text")

	var runErr error
	out := captureStdout(t, func() {
		runErr = runListen([]string{"--wait-one", "--session-id=" + sid, "--emit=content", "--until-deadline=5s", "--no-auto-ack"})
	})
	require.NoError(t, runErr)
	assert.Contains(t, out, "wake body text")
	assert.NotContains(t, out, "schemaVersion", "content mode emits only the body")
}

// F-48: a --wait-one window that expires in content mode suppresses the JSON
// timeout payload — empty stdout, exit 0.
func TestRunListen_WaitOne_EmitContentTimeout_EmptyStdout(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	sid := "lsnemit2"
	plantOverviewSession(t, dataDir, sid, session.RoleEsc, "ESC-y", "/repo/x", "", "")

	var runErr error
	out := captureStdout(t, func() {
		runErr = runListen([]string{"--wait-one", "--session-id=" + sid, "--emit=content", "--until-deadline=1s", "--no-auto-ack"})
	})
	require.NoError(t, runErr, "wait-one timeout exits 0")
	assert.Empty(t, strings.TrimSpace(out), "content mode suppresses the timeout payload")
}

func TestRunListen_EmitInvalid(t *testing.T) {
	err := runListen([]string{"--emit=xml", "--session-id=lsnemit3"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--emit")
}

// F-81: listen publishes its window into the manifest at startup, so overview
// can report "listening (PID, expires in ...)". Verified after a --wait-one run
// that exits as soon as it drains the pre-planted inbox.
func TestRunListen_WaitOne_WritesListenUntil(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	sid := "lsnlu001"
	plantOverviewSession(t, dataDir, sid, session.RoleEsc, "ESC-y", "/repo/x", "", "")
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val12345", "VAL-x", message.TypeResponse, "wake")

	before := time.Now().UTC()
	var runErr error
	_ = captureStdout(t, func() {
		runErr = runListen([]string{"--wait-one", "--session-id=" + sid, "--until-deadline=5s", "--no-auto-ack"})
	})
	require.NoError(t, runErr)

	mgr := session.NewManager(dataDir, time.Second)
	mf, err := mgr.LoadManifest(sid)
	require.NoError(t, err)
	require.NotNil(t, mf.ListenUntil, "listen must publish its window at startup")
	assert.True(t, mf.ListenUntil.After(before), "ListenUntil is in the future relative to before the call")
	assert.True(t, mf.ListenUntil.Before(before.Add(10*time.Second)), "ListenUntil ~ now + 5s window, not unbounded")
}
