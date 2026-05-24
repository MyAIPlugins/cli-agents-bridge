package regression

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// TestBUG9_TouchRefreshesHeartbeatPreConnect reproduces BUG-9 (Patil
// connect-peer.sh did not refresh the sender's heartbeat before
// establishing the link, so a long-idle peer could appear stale to the
// remote at the exact moment of connect, breaking the handshake UX).
//
// cli-agents-bridge fix: session.Manager.Touch refreshes lastHeartbeat
// atomically. The cmd-side connect path (Sprint 4 candidate when a
// dedicated `connect-peer` subcommand lands) will call mgr.Touch(sid)
// before issuing the ping. For Sprint 3 we assert the library primitive
// behaves correctly: after Touch, lastHeartbeat is within milliseconds
// of "now", not the original Register timestamp.
func TestBUG9_TouchRefreshesHeartbeatPreConnect(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	mgr := session.NewManager(dataDir, time.Second)

	// Stage: register a session and backdate its heartbeat to simulate
	// long idle (5 min ago, beyond default StaleSeconds=300).
	pastNow := time.Now().UTC().Add(-5 * time.Minute)
	mgr.Now = func() time.Time { return pastNow }

	proj := filepath.Join(t.TempDir(), "p")
	require.NoError(t, os.MkdirAll(proj, 0o755))
	mf, release, err := mgr.Register(context.Background(), session.RegisterOpts{
		ProjectPath: proj,
		Role:        session.RoleEsc,
		AgentName:   "ESC-bug9",
		ForceNew:    true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = release() })

	before, err := mgr.LoadManifest(mf.SessionID)
	require.NoError(t, err)
	assert.True(t, time.Since(before.LastHeartbeat) > time.Minute,
		"pre-Touch heartbeat must be visibly old (engineered backdate)")

	// Reset clock to real-now and call Touch (simulates connect-peer path)
	mgr.Now = func() time.Time { return time.Now().UTC() }
	require.NoError(t, mgr.Touch(mf.SessionID))

	after, err := mgr.LoadManifest(mf.SessionID)
	require.NoError(t, err)
	assert.True(t, time.Since(after.LastHeartbeat) < 5*time.Second,
		"BUG-9 regression: Touch must refresh heartbeat to ~now; got age=%v",
		time.Since(after.LastHeartbeat))
}
