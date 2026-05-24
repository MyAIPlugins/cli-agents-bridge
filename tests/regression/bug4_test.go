package regression

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/cleanup"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// TestBUG4_CleanupMySession_DoesNotTouchOtherProject reproduces BUG-4
// (Patil cleanup.sh:55-67 globally wiped ALL stale sessions across ALL
// projects via $BRIDGE_DIR/sessions/*/manifest.json glob — evidenced by
// chatterence-bi-template cleanup destroying 2 ac-agents sessions on
// 2026-05-24, PLAN §2 evidence trail).
//
// cli-agents-bridge fix: cleanup.Run with ScopeMySession touches ONLY the
// caller's OwnSessionID. Global scope is explicit via --scope=global with
// confirmation prompt in the cmd wrapper.
//
// Test scenario:
//   - Register two sessions A and B (representing two projects sharing the
//     same DataDir, as Alan's daily setup does).
//   - Run cleanup with scope=my-session targeting only A.
//   - Assert A removed, B intact (the BUG-4 cross-project safety).
func TestBUG4_CleanupMySession_DoesNotTouchOtherProject(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	mgr := session.NewManager(dataDir, time.Second)

	projA := t.TempDir()
	projB := t.TempDir()

	mfA, relA, err := mgr.Register(context.Background(), session.RegisterOpts{
		ProjectPath: projA,
		Role:        session.RoleVal,
		AgentName:   "VAL-projA",
	})
	require.NoError(t, err)
	require.NoError(t, relA())

	mfB, relB, err := mgr.Register(context.Background(), session.RegisterOpts{
		ProjectPath: projB,
		Role:        session.RoleEsc,
		AgentName:   "ESC-projB",
		ForceNew:    true,
	})
	require.NoError(t, err)
	require.NoError(t, relB())

	res, err := cleanup.Run(context.Background(), cleanup.Options{
		DataDir:       dataDir,
		Scope:         cleanup.ScopeMySession,
		OwnSessionID:  mfA.SessionID,
		StaleSeconds:  300,
		RetentionDays: 7,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{mfA.SessionID}, res.SessionsRemoved,
		"only own session must be in the removed list")

	_, err = os.Stat(filepath.Join(dataDir, "sessions", mfA.SessionID))
	assert.True(t, os.IsNotExist(err), "own session must be removed")

	_, err = os.Stat(filepath.Join(dataDir, "sessions", mfB.SessionID))
	assert.NoError(t, err,
		"BUG-4 regression: other project's session must be UNTOUCHED — Patil bug recurred if this fails")
}
