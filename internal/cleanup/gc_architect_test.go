package cleanup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// plantRole is plantScoped with the role of the session set.
func plantRole(t *testing.T, dataDir, id, role string, pid int, heartbeat time.Time, scope string) {
	t.Helper()
	plantScoped(t, dataDir, id, pid, heartbeat, scope)
	mgr := session.NewManager(dataDir, time.Second)
	mf, err := mgr.LoadManifest(id)
	require.NoError(t, err)
	mf.Role = role
	require.NoError(t, mgr.SaveManifest(mf))
}

// D11. The architect is Claude Desktop's session over MCP and its process dies with
// every quit of the app, so "dead PID, quiet for a day" is where it rests, not where it
// was abandoned. The sweep that runs on every join leaves it alone; the session next to
// it in the same condition, any other role, is still collected.
func TestGCOrphans_NeverCollectsAnArchitectAndStillCollectsTheSessionNextToIt(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	const scope = "/repo/desktop"
	old := base.Add(-72 * time.Hour)
	plantRole(t, dataDir, "arcstale1", session.RoleArchitect, deadPID, old, scope)
	plantRole(t, dataDir, "escstale1", session.RoleEsc, deadPID, old, scope)
	plantRole(t, dataDir, "valstale1", session.RoleVal, deadPID, old, scope)

	removed, err := GCOrphans(dataDir, scope, 24, func() time.Time { return base })
	require.NoError(t, err)

	var gone []string
	for _, o := range removed {
		gone = append(gone, o.SessionID)
	}
	assert.ElementsMatch(t, []string{"escstale1", "valstale1"}, gone, "every other role in that state is swept, as before")
	assertPresent(t, dataDir, "arcstale1")
	assertGone(t, dataDir, "escstale1")
}

// The explicit cleanup is the other half of the decision: only the sweep nobody asked
// for leaves the architect alone.
func TestRun_Global_StillCollectsAStaleArchitect(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	const scope = "/repo/desktop"
	plantRole(t, dataDir, "arcstale1", session.RoleArchitect, deadPID, base.Add(-72*time.Hour), scope)

	res, err := Run(context.Background(), Options{
		DataDir: dataDir, Scope: ScopeGlobal, CallerScope: scope, StaleSeconds: 300, RetentionDays: 7,
		Now: func() time.Time { return base },
	})
	require.NoError(t, err)
	assert.Contains(t, res.SessionsRemoved, "arcstale1")
	assertGone(t, dataDir, "arcstale1")
}
