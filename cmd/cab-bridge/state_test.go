package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// plantStateSession writes a manifest with an explicit State + heartbeat so the
// staleness/display paths can be driven deterministically.
func plantStateSession(t *testing.T, dataDir, id, state string, heartbeat time.Time) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "sessions", id), 0o700))
	mgr := session.NewManager(dataDir, time.Second)
	require.NoError(t, mgr.SaveManifest(&session.Manifest{
		SessionID:     id,
		SchemaVersion: session.SchemaVersionV2,
		ProjectName:   "p-" + id,
		ProjectPath:   filepath.Join(dataDir, "p-"+id),
		AgentName:     "a-" + id,
		Role:          session.RoleVal,
		PID:           1,
		StartedAt:     heartbeat,
		LastHeartbeat: heartbeat,
		Status:        session.StatusActive,
		Capabilities:  []string{"query"},
		State:         state,
	}))
}

func TestRunState_SetsValidState(t *testing.T) {
	// Not parallel: t.Setenv.
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	mgr := session.NewManager(dataDir, time.Second)
	mf, rel, err := mgr.Register(context.Background(), session.RegisterOpts{ProjectPath: t.TempDir(), Role: session.RoleEsc})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel() })

	var runErr error
	out := captureStdout(t, func() {
		runErr = runState([]string{"--session-id=" + mf.SessionID, "working"})
	})
	require.NoError(t, runErr)
	assert.Contains(t, out, "working")

	loaded, err := mgr.LoadManifest(mf.SessionID)
	require.NoError(t, err)
	assert.Equal(t, session.StateWorking, loaded.State)
}

func TestRunState_InvalidValue_Errors(t *testing.T) {
	t.Parallel()
	err := runState([]string{"--session-id=abc123", "bogus"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid value")
}

func TestRunState_RequiresExactlyOneValue(t *testing.T) {
	t.Parallel()
	err := runState([]string{"--session-id=abc123"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one state value")
}

// TestCollectPeers_OrchestratingNotStale_LegacyStale is the F-23a observability
// core: an orchestrating session with an ancient heartbeat is NOT stale (exempt),
// while a legacy (empty-state) session with the same heartbeat IS stale.
func TestCollectPeers_OrchestratingNotStale_LegacyStale(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	old := time.Now().UTC().Add(-1 * time.Hour)
	plantStateSession(t, dataDir, "orchsess", session.StateOrchestrating, old)
	plantStateSession(t, dataDir, "legacyss", "", old)

	peers, _, err := collectPeers(session.NewManager(dataDir, time.Second), dataDir, 300, true, "", "")
	require.NoError(t, err)
	byID := map[string]peerSummary{}
	for _, p := range peers {
		byID[p.SessionID] = p
	}
	require.Contains(t, byID, "orchsess")
	require.Contains(t, byID, "legacyss")
	assert.False(t, byID["orchsess"].Stale, "orchestrating is heartbeat-exempt -> not stale")
	assert.Equal(t, session.StateOrchestrating, byID["orchsess"].State, "peers must surface the state")
	assert.True(t, byID["legacyss"].Stale, "legacy empty-state old heartbeat -> stale (unchanged behaviour)")
}

func TestRunStatus_ShowsStateAndOrchestratingNotStale(t *testing.T) {
	// Not parallel: t.Setenv.
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	plantStateSession(t, dataDir, "orchstat", session.StateOrchestrating, time.Now().UTC().Add(-1*time.Hour))

	var runErr error
	out := captureStdout(t, func() { runErr = runStatus([]string{"--session-id=orchstat"}) })
	require.NoError(t, runErr)

	var rep map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	assert.Equal(t, "orchestrating", rep["state"])
	assert.Equal(t, false, rep["stale"], "orchestrating must show not-stale in status")
}

func TestRunWhoami_ShowsState(t *testing.T) {
	// Not parallel: t.Setenv.
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	plantStateSession(t, dataDir, "whoamist", session.StateWorking, time.Now().UTC())

	var runErr error
	out := captureStdout(t, func() { runErr = runWhoami([]string{"--session-id=whoamist"}) })
	require.NoError(t, runErr)
	assert.Contains(t, out, "working", "whoami must show the state")
}
