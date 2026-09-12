// Package regression houses end-to-end repro tests for the 9 upstream bugs
// confirmed in PLAN §2. Each test file is named after the bug it covers and
// asserts the cli-agents-bridge behavior after the fix.
package regression

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// heartbeatTick is the accelerated interval this test runs the goroutine at —
// 30ms against a 30s production default.
//
// heartbeatPatience is DERIVED from it rather than chosen. The number this test
// used to carry was 100ms, picked once for one machine, and picking another
// literal would only move the arbitrariness somewhere else: a hundred ticks of
// slack means the same thing whatever the tick becomes.
const (
	heartbeatTick     = 30 * time.Millisecond
	heartbeatPatience = 100 * heartbeatTick
	heartbeatPoll     = heartbeatTick / 3
)

// TestBUG1_HeartbeatPersistsDuringListen reproduces BUG-1 (Patil
// bridge-listen.sh:30-68 never invokes heartbeat.sh during the polling loop,
// so lastHeartbeat freezes at register time and list-peers reports a stale
// peer after STALE_SECONDS=300).
//
// cli-agents-bridge fix: Manager.StartHeartbeat() launches a goroutine that
// updates lastHeartbeat at every tick (config.HeartbeatTickMs).
//
// IT ASSERTS PROGRESS, NOT FRESHNESS, and that is a correction to this very
// test rather than a style preference.
//
// The previous version sampled lastHeartbeat five times and required each to be
// less than 100ms old against the wall clock — three and a bit ticks of
// tolerance, under -race, in parallel with the rest of the suite, on a
// two-core shared runner. Five CI runs out of six went red in one morning,
// including two on commits that touched only ROADMAP.md: a documentation change
// cannot slow a goroutine down, so what the assertion measured was the runner's
// scheduling, not the property in its own name.
//
// THE TWO KINDS OF TIME LIMIT, because this file still contains one and the
// difference is the whole point: the old threshold DEFINED the property — "fresh
// within 100ms" — so a slow machine violated it while everything was healthy.
// heartbeatPatience below DEFINES NOTHING. It only bounds how long we wait
// before calling something dead that is not moving. A slow runner takes longer
// and still passes; a goroutine that has stopped never advances and fails every
// time, on any hardware. A timeout that is part of the oracle is a flake waiting
// to happen; a timeout that is only patience is not.
//
// THREE ADVANCES, not one. One would only prove the goroutine STARTED, and
// BUG-1 is not a failure to start — it is freezing AFTER registration. Three
// prove it keeps going, which also covers "starts and then dies" without a
// second test.
//
// STRICTLY LATER, not merely different: a timestamp that went backwards would be
// a defect, and an inequality would have accepted it.
//
// THE ASSUMPTION, stated rather than left to be discovered: this cannot tell
// "the goroutine updates the manifest" from "somebody else updates it". Nothing
// else touches this session inside the test, so it does not bite here — but that
// is the ground the oracle stands on, and an implicit assumption is the thing
// somebody violates in a year without knowing they did.
func TestBUG1_HeartbeatPersistsDuringListen(t *testing.T) {
	t.Parallel()

	mgr := session.NewManager(t.TempDir(), heartbeatTick)
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

	// The starting point is the value written at REGISTER time — which is
	// exactly the value BUG-1 leaves frozen there forever.
	registered, err := mgr.LoadManifest(mf.SessionID)
	require.NoError(t, err)

	const advances = 3
	prev := registered.LastHeartbeat
	for i := 1; i <= advances; i++ {
		prev = awaitHeartbeatAdvance(t, mgr, mf.SessionID, prev, i)
	}
}

// awaitHeartbeatAdvance blocks until lastHeartbeat is strictly later than prev,
// and fails by name when it never is.
//
// Two ticks 30ms apart are always distinguishable because the manifest stores a
// time.Time and the default JSON marshalling keeps nanoseconds (manifest.go) —
// checked before this shape was written, because a progress test on a coarser
// timestamp would hang until the deadline on a perfectly healthy system, which
// would have been one flake traded for another inside the lot that exists to
// remove flakes.
func awaitHeartbeatAdvance(t *testing.T, mgr *session.Manager, sessionID string, prev time.Time, n int) time.Time {
	t.Helper()

	deadline := time.Now().Add(heartbeatPatience)
	for {
		current, err := mgr.LoadManifest(sessionID)
		require.NoError(t, err)
		if current.LastHeartbeat.After(prev) {
			return current.LastHeartbeat
		}
		if time.Now().After(deadline) {
			t.Fatalf("advance %d/%d: lastHeartbeat never moved past %v within %v (%d ticks of slack) — "+
				"BUG-1 regression: the heartbeat goroutine is not updating the manifest",
				n, 3, prev, heartbeatPatience, heartbeatPatience/heartbeatTick)
		}
		time.Sleep(heartbeatPoll)
	}
}
