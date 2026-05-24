// Package regression houses end-to-end repro tests for the 9 upstream bugs
// confirmed in PLAN §2. Each test file is named after the bug it covers and
// asserts the cli-agents-bridge behavior after the fix.
package regression

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// TestBUG1_HeartbeatPersistsDuringListen reproduces BUG-1 (Patil
// bridge-listen.sh:30-68 never invokes heartbeat.sh during the polling loop,
// so lastHeartbeat freezes at register time and list-peers reports a stale
// peer after STALE_SECONDS=300).
//
// cli-agents-bridge fix: Manager.StartHeartbeat() launches a goroutine that
// updates lastHeartbeat at every tick (config.HeartbeatTickMs).
//
// Test acceleration: HeartbeatInterval is set to 30ms (vs 30s production
// default) and we sample lastHeartbeat across 300ms (~10 ticks). PLAN §10
// metric M1 specifies <90s in production; the test asserts <100ms freshness
// at the compressed scale to validate the structural property, not the
// absolute timing.
func TestBUG1_HeartbeatPersistsDuringListen(t *testing.T) {
	t.Parallel()

	mgr := session.NewManager(t.TempDir(), 30*time.Millisecond)
	projDir := t.TempDir()

	mf, release, err := mgr.Register(context.Background(), session.RegisterOpts{
		ProjectPath: projDir,
		Role:        session.RoleEsc,
		AgentName:   "ESC-bug1",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = release() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := mgr.StartHeartbeat(ctx, mf.SessionID)
	t.Cleanup(func() { cancel(); <-done })

	// Sample lastHeartbeat 5 times across 300ms. After the first tick
	// each sample must be at most 100ms old. The first sample (taken
	// immediately) is exempt because the goroutine may not have ticked yet.
	time.Sleep(50 * time.Millisecond) // let first tick land

	const samples = 5
	const maxAge = 100 * time.Millisecond
	for i := 0; i < samples; i++ {
		updated, err := mgr.LoadManifest(mf.SessionID)
		require.NoError(t, err)
		age := time.Since(updated.LastHeartbeat)
		assert.Less(t, age, maxAge,
			"sample %d: lastHeartbeat must be <%v old (got %v) — BUG-1 regression: heartbeat goroutine not updating manifest",
			i, maxAge, age)
		time.Sleep(60 * time.Millisecond)
	}
}
