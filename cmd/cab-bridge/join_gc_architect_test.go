package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

func plantAbandoned(t *testing.T, dataDir, id, role, name, scope string) {
	t.Helper()
	plantSessionFull(t, dataDir, id, role, name, scope, filepath.Join(scope, ".planted", id), "idle")
	mgr := newSessionManager(nextTestConfig(t, dataDir))
	mf, err := mgr.LoadManifest(id)
	require.NoError(t, err)
	mf.PID = deadPID
	mf.LastHeartbeat = time.Now().UTC().Add(-72 * time.Hour)
	require.NoError(t, mgr.SaveManifest(mf))
}

// D11 end to end: the sweep every `join` runs does not collect the architect of the
// scope, however long its connector has been gone, and it still collects the session
// next to it that is in exactly the same condition.
func TestJoin_TheSweepOnJoinLeavesAStaleArchitectAndCollectsTheOtherSessions(t *testing.T) {
	dataDir := t.TempDir()
	proj := t.TempDir()
	dataDir = useBase(t, dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0") // learn the scope the join derives, with the sweep off
	args := []string{"--role=esc", "--agent-name=ESC-arriving", "--project-path=" + proj}
	captureStdout(t, func() { require.NoError(t, runJoin(args)) })
	entries, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	mf, err := newSessionManager(nextTestConfig(t, dataDir)).LoadManifest(entries[0].Name())
	require.NoError(t, err)
	scope := session.EffectiveScope(mf)
	require.NotEmpty(t, scope)

	plantAbandoned(t, dataDir, "arcgone01", session.RoleArchitect, "ARC-gone-cowork", scope)
	plantAbandoned(t, dataDir, "escgone01", session.RoleEsc, "ESC-gone", scope)

	t.Setenv("CAB_AUTO_GC_HOURS", "24")
	captureStdout(t, func() { require.NoError(t, runJoin(args)) })

	assert.True(t, sessionExists(dataDir, "arcgone01"), "the architect of the scope survives the sweep")
	assert.False(t, sessionExists(dataDir, "escgone01"), "and the executor in the same state is still collected")
}
