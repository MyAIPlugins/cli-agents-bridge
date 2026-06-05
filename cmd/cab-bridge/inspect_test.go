package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// TestRunInspect_SessionIDFlag_Rejected is the A-5 check: inspect takes the id
// as a POSITIONAL argument, so --session-id returns an actionable error pointing
// at the positional form — not the cryptic stdlib "flag provided but not
// defined: -session-id". The check fires before any FS access.
func TestRunInspect_SessionIDFlag_Rejected(t *testing.T) {
	t.Parallel()
	err := runInspect([]string{"--session-id=abc123"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inspect: --session-id is not supported here")
	assert.Contains(t, err.Error(), "positional", "the message must teach the positional form")
}

// TestRunInspect_PositionalStillWorks guards the A-5 invariant: adding the
// reject path must not change the happy path — `inspect <id>` still prints the
// manifest.
func TestRunInspect_PositionalStillWorks(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	plantOverviewSession(t, dataDir, "insp0001", session.RoleEsc, "ESC-x", "/repo/x", "", "working")

	var runErr error
	out := captureStdout(t, func() {
		runErr = runInspect([]string{"--json=false", "insp0001"})
	})
	require.NoError(t, runErr)
	assert.Contains(t, out, "insp0001", "the positional inspect still prints the manifest")
}

// TestRunInspect_ShowsListenerRecord is the B-2 observability: once a listener
// has claimed, inspect's human output carries the listener generation/pid.
func TestRunInspect_ShowsListenerRecord(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	plantOverviewSession(t, dataDir, "inspls01", session.RoleEsc, "ESC-x", "/repo/x", "", "working")
	mgr := session.NewManager(dataDir, time.Second)
	_, err := mgr.ClaimListener("inspls01")
	require.NoError(t, err)

	var runErr error
	out := captureStdout(t, func() {
		runErr = runInspect([]string{"--json=false", "inspls01"})
	})
	require.NoError(t, runErr)
	assert.Contains(t, out, "Listener: generation 1", "inspect shows the listener ownership record")
}
