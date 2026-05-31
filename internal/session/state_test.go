package session

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsValidState(t *testing.T) {
	t.Parallel()
	for _, s := range []string{StateIdle, StateWorking, StateDone, StateOrchestrating} {
		assert.True(t, IsValidState(s), "%q must be valid", s)
	}
	for _, s := range []string{"", "Working", "busy", "idle ", "blocked"} {
		assert.False(t, IsValidState(s), "%q must be invalid", s)
	}
}

func TestIsStale(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)

	assert.False(t, IsStale(&Manifest{LastHeartbeat: now.Add(-10 * time.Second)}, 300, now), "10s < 300s -> not stale")
	assert.True(t, IsStale(&Manifest{LastHeartbeat: now.Add(-10 * time.Minute)}, 300, now), "10min > 300s -> stale")

	// orchestrating is heartbeat-exempt: never stale, even with an ancient heartbeat.
	orch := &Manifest{State: StateOrchestrating, LastHeartbeat: now.Add(-24 * time.Hour)}
	assert.False(t, IsStale(orch, 300, now), "orchestrating is never stale regardless of heartbeat age")

	// a non-orchestrating state does NOT exempt.
	working := &Manifest{State: StateWorking, LastHeartbeat: now.Add(-10 * time.Minute)}
	assert.True(t, IsStale(working, 300, now), "working does not exempt staleness")

	// legacy empty state behaves as before (no exemption).
	legacy := &Manifest{State: "", LastHeartbeat: now.Add(-10 * time.Minute)}
	assert.True(t, IsStale(legacy, 300, now), "legacy empty state -> normal staleness")
}

func TestSetState_UpdatesManifestAndHeartbeat(t *testing.T) {
	t.Parallel()
	mgr := NewManager(t.TempDir(), time.Second)
	mf, rel, err := mgr.Register(context.Background(), RegisterOpts{ProjectPath: t.TempDir(), Role: RoleEsc})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel() })

	assert.Empty(t, mf.State, "fresh session has no state")
	before := mf.LastHeartbeat
	time.Sleep(2 * time.Millisecond)
	require.NoError(t, mgr.SetState(mf.SessionID, StateWorking))

	loaded, err := mgr.LoadManifest(mf.SessionID)
	require.NoError(t, err)
	assert.Equal(t, StateWorking, loaded.State)
	assert.True(t, loaded.LastHeartbeat.After(before), "SetState must refresh the heartbeat")
}

// TestSetState_ConcurrentWithHeartbeat_NoLostUpdate mirrors the F-12 manifestMu
// race: SetState and the heartbeat goroutine both RMW the same manifest; the
// mutex must serialize them so the last state written survives. Run under -race.
func TestSetState_ConcurrentWithHeartbeat_NoLostUpdate(t *testing.T) {
	t.Parallel()
	mgr := NewManager(t.TempDir(), 5*time.Millisecond) // aggressive heartbeat tick
	mf, rel, err := mgr.Register(context.Background(), RegisterOpts{ProjectPath: t.TempDir(), Role: RoleEsc})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel() })

	ctx, cancel := context.WithCancel(context.Background())
	done := mgr.StartHeartbeat(ctx, mf.SessionID)

	states := []string{StateIdle, StateWorking, StateDone, StateOrchestrating}
	const n = 40
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if e := mgr.SetState(mf.SessionID, states[i%len(states)]); e != nil {
				select {
				case errCh <- e:
				default:
				}
				return
			}
		}
	}()
	wg.Wait()
	cancel()
	<-done

	select {
	case e := <-errCh:
		require.NoError(t, e, "SetState must not error under concurrency")
	default:
	}

	loaded, err := mgr.LoadManifest(mf.SessionID)
	require.NoError(t, err)
	assert.Equal(t, states[(n-1)%len(states)], loaded.State, "last state must survive concurrent heartbeat writes")
	assert.False(t, loaded.LastHeartbeat.IsZero(), "heartbeat must also have run")
}
