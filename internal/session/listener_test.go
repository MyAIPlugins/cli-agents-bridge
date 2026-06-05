package session

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustLoadHB(t *testing.T, mgr *Manager, sid string) time.Time {
	t.Helper()
	mf, err := mgr.LoadManifest(sid)
	require.NoError(t, err)
	return mf.LastHeartbeat
}

// newListenerMgr returns a Manager over a temp data dir with the session dir
// pre-created (AcquireLock needs sessions/<id>/ to exist to write the lock file).
func newListenerMgr(t *testing.T, sid string) *Manager {
	t.Helper()
	mgr := NewManager(t.TempDir(), time.Second)
	require.NoError(t, os.MkdirAll(mgr.sessionDir(sid), 0o700))
	return mgr
}

// TestClaimListener_FreshAndBump: the first claim mints generation 1 + a token;
// a second claim bumps the generation, mints a NEW token, and the old token is
// no longer current (a second listener spodesta the first).
func TestClaimListener_FreshAndBump(t *testing.T) {
	t.Parallel()
	const sid = "lsnown01"
	mgr := newListenerMgr(t, sid)

	o1, err := mgr.ClaimListener(sid)
	require.NoError(t, err)
	assert.Equal(t, 1, o1.Generation)
	assert.NotEmpty(t, o1.Token)
	assert.Equal(t, os.Getpid(), o1.PID)
	assert.False(t, o1.ClaimedAt.IsZero())
	assert.True(t, mgr.IsListenerCurrent(sid, o1.Token), "the fresh token is current")

	o2, err := mgr.ClaimListener(sid)
	require.NoError(t, err)
	assert.Equal(t, 2, o2.Generation, "generation is monotone")
	assert.NotEqual(t, o1.Token, o2.Token, "a new claim mints a new token")
	assert.False(t, mgr.IsListenerCurrent(sid, o1.Token), "the old token is no longer current")
	assert.True(t, mgr.IsListenerCurrent(sid, o2.Token))
}

// TestReclaimListener_InvalidatesCurrent: a reclaim bumps the generation, writes
// a revocation token (PID=0), and invalidates the previous holder's token.
func TestReclaimListener_InvalidatesCurrent(t *testing.T) {
	t.Parallel()
	const sid = "lsnown02"
	mgr := newListenerMgr(t, sid)

	o1, err := mgr.ClaimListener(sid)
	require.NoError(t, err)

	info, err := mgr.ReclaimListener(sid)
	require.NoError(t, err)
	assert.Equal(t, o1.Generation, info.PrevGeneration)
	assert.Equal(t, o1.Generation+1, info.NewGeneration)
	assert.Equal(t, os.Getpid(), info.PrevPID)
	assert.Equal(t, o1.Token, info.PrevToken)

	assert.False(t, mgr.IsListenerCurrent(sid, o1.Token), "reclaim invalidates the previous token")

	o, ok, err := mgr.ReadListener(sid)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 0, o.PID, "reclaim leaves PID=0 (reclaim-pending, no listener has re-claimed)")
	assert.Equal(t, o1.Generation+1, o.Generation)
}

// TestReadListener_AbsentFailsClosed: with no listener.json, ReadListener
// reports absent and IsListenerCurrent fails closed (false), never erroring.
func TestReadListener_AbsentFailsClosed(t *testing.T) {
	t.Parallel()
	const sid = "lsnown03"
	mgr := newListenerMgr(t, sid)

	_, ok, err := mgr.ReadListener(sid)
	require.NoError(t, err)
	assert.False(t, ok, "no listener.json yet")
	assert.False(t, mgr.IsListenerCurrent(sid, "anytoken"), "absent → not current (fail closed)")
}

// TestReclaimThenClaim_NewOwnerCurrent: after a reclaim, a fresh ClaimListener
// (the new listen) becomes current; the revocation token is superseded.
func TestReclaimThenClaim_NewOwnerCurrent(t *testing.T) {
	t.Parallel()
	const sid = "lsnown04"
	mgr := newListenerMgr(t, sid)

	o1, err := mgr.ClaimListener(sid)
	require.NoError(t, err)
	_, err = mgr.ReclaimListener(sid)
	require.NoError(t, err)
	o2, err := mgr.ClaimListener(sid)
	require.NoError(t, err)

	assert.Equal(t, 3, o2.Generation, "claim(1) -> reclaim(2) -> claim(3)")
	assert.True(t, mgr.IsListenerCurrent(sid, o2.Token), "the new listener is current")
	assert.False(t, mgr.IsListenerCurrent(sid, o1.Token), "the original token stays invalid")
	assert.Equal(t, os.Getpid(), o2.PID)
}

// TestStartHeartbeatOwned_StopsOnEviction is B-2 test 8: an evicted listener's
// heartbeat goroutine STOPS on the token mismatch and writes no further
// LastHeartbeat — so it cannot clobber the heartbeat of the session the new
// owner now holds (the cross-process lost-update the in-process lock can't stop).
func TestStartHeartbeatOwned_StopsOnEviction(t *testing.T) {
	t.Parallel()
	const sid = "hbown001"
	mgr := NewManager(t.TempDir(), 5*time.Millisecond) // fast heartbeat
	require.NoError(t, os.MkdirAll(mgr.sessionDir(sid), 0o700))
	plantManifestDetails(t, mgr, sid, "/repo/x", "/repo/x", "ESC-x", RoleEsc)

	o, err := mgr.ClaimListener(sid)
	require.NoError(t, err)
	ownerOK := func() bool { return mgr.IsListenerCurrent(sid, o.Token) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := mgr.StartHeartbeatOwned(ctx, sid, ownerOK)

	// Reclaim → ownerOK goes false → the goroutine must stop on its OWN (no cancel).
	_, err = mgr.ReclaimListener(sid)
	require.NoError(t, err)

	select {
	case <-done:
		// stopped on the token mismatch — correct
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat goroutine must stop on eviction (token mismatch)")
	}

	hbAtStop := mustLoadHB(t, mgr, sid)
	time.Sleep(30 * time.Millisecond) // several heartbeat intervals
	assert.True(t, mustLoadHB(t, mgr, sid).Equal(hbAtStop), "no heartbeat write after eviction")
}
