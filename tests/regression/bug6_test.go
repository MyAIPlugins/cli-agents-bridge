package regression

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// TestBUG6_DoubleRegisterSameCwd_RefusedWithoutForceNew reproduces BUG-6
// (Patil register.sh:10-24 silently reused .claude/bridge-session when two
// Claude Code instances ran in the same cwd — producing two sessions with
// the same ID sharing inbox/outbox, indistinguishable as peers).
//
// cli-agents-bridge fix: Manager.Register checks for an existing live
// session at the same projectPath via LongestPrefixLookup + isProcessAlive.
// If found, returns ErrSessionExistsForProject with the existing PID in the
// error message and the hint "use --force-new to override".
//
// Test scenario from PLAN §7.2 row BUG-6:
//   - First Register on projDir succeeds.
//   - Second Register on the same projDir without ForceNew must fail with
//     ErrSessionExistsForProject mentioning the holder PID.
//   - Third Register with ForceNew=true must succeed and produce a fresh
//     session ID, demonstrating the override path.
func TestBUG6_DoubleRegisterSameCwd_RefusedWithoutForceNew(t *testing.T) {
	t.Parallel()

	mgr := session.NewManager(t.TempDir(), time.Second)
	projDir := t.TempDir()

	mf1, rel1, err := mgr.Register(context.Background(), session.RegisterOpts{
		ProjectPath: projDir,
		Role:        session.RoleVal,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel1() })

	_, _, err = mgr.Register(context.Background(), session.RegisterOpts{
		ProjectPath: projDir,
		Role:        session.RoleEsc,
		// ForceNew omitted (= false)
	})
	require.Error(t, err, "BUG-6 regression: second register on same projectPath must fail")
	assert.ErrorIs(t, err, session.ErrSessionExistsForProject)
	assert.Contains(t, err.Error(), mf1.SessionID,
		"error must mention the holder session ID for debuggability")
	assert.Contains(t, err.Error(), "use --force-new",
		"error must include the override hint")

	mf3, rel3, err := mgr.Register(context.Background(), session.RegisterOpts{
		ProjectPath: projDir,
		Role:        session.RoleEsc,
		ForceNew:    true,
	})
	require.NoError(t, err, "BUG-6: ForceNew must override the existing-session check")
	t.Cleanup(func() { _ = rel3() })

	assert.NotEqual(t, mf1.SessionID, mf3.SessionID,
		"ForceNew must produce a distinct session ID, not reuse the existing one")
}
