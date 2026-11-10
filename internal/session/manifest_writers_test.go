package session

// F-160, CRI diff-gate blocker. Every read-modify-write of an existing session's
// manifest runs under the session lock (updateManifest), so that "the manifest is
// gone" is a lasting fact once cleanup has retired the session. These tests fix what
// a writer does with a lock somebody else holds: the commands wait for it, as every
// other caller of the lock does; the heartbeat never waits and skips the beat.
// What a writer does with a RETIRED session is in lock_retired_test.go, and the
// race with a real retirement in internal/cleanup.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const writerHoldMs = 400

func TestManifestWriters_WaitForAShortHoldAndWriteAfterIt(t *testing.T) {
	writers := map[string]struct {
		write func(m *Manager, sid string) error
		saw   func(before, after *Manifest) bool
	}{
		"SetState": {
			write: func(m *Manager, sid string) error { return m.SetState(sid, StateWorking) },
			saw:   func(_, after *Manifest) bool { return after.State == StateWorking },
		},
		"Touch": {
			write: func(m *Manager, sid string) error { return m.Touch(sid) },
			saw:   func(before, after *Manifest) bool { return after.LastHeartbeat.After(before.LastHeartbeat) },
		},
	}
	for name, w := range writers {
		t.Run(name, func(t *testing.T) {
			dataDir, sid := exclSession(t)
			mgr := NewManager(dataDir, time.Second)
			mgr.LockWait = 5 * time.Second
			before, err := mgr.LoadManifest(sid)
			require.NoError(t, err)

			startExclHolder(t, dataDir, sid, writerHoldMs) // lets go by itself; the helper reaps it
			start := time.Now()
			require.NoError(t, w.write(mgr, sid), "the writer waits for a hold that ends by itself")
			waited := time.Since(start)

			assert.GreaterOrEqual(t, waited, writerHoldMs*time.Millisecond/2, "and it really waited: it did not write over the holder")
			after, err := mgr.LoadManifest(sid)
			require.NoError(t, err)
			assert.True(t, w.saw(before, after), "the write landed after the hold")
		})
	}
}

func TestManifestWriters_AHoldLongerThanTheWaitIsAnErrorAndWritesNothing(t *testing.T) {
	dataDir, sid := exclSession(t)
	mgr := NewManager(dataDir, time.Second)
	mgr.LockWait = 100 * time.Millisecond
	before, err := mgr.LoadManifest(sid)
	require.NoError(t, err)

	holder := startExclHolder(t, dataDir, sid, 0)
	writers := map[string]func() error{
		"SetState":        func() error { return mgr.SetState(sid, StateWorking) },
		"SetLastConsumed": func() error { return mgr.SetLastConsumed(sid, "msg-aaaaaaaaaaaa") },
		"Touch":           func() error { return mgr.Touch(sid) },
		"AdoptPID":        func() error { return mgr.AdoptPID(sid) },
		"RenameAgent":     func() error { return mgr.RenameAgent(sid, "ESC-renamed") },
		"SetRole":         func() error { return mgr.SetRole(sid, RoleEsc) },
	}
	for name, write := range writers {
		require.ErrorIs(t, write(), ErrLockHeld, name)
	}
	holder.release(t)

	after, err := mgr.LoadManifest(sid)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a writer that was refused changed nothing")
}

// A beat of the heartbeat goroutine never waits: the lock is held by a claim, a
// reclaim or a commit, all short, and the next tick tries again. A beat skipped is
// not an error; a retired session (lock_retired_test.go) is.
func TestTouchHeartbeat_SkipsTheBeatWhileTheLockIsHeld(t *testing.T) {
	dataDir, sid := exclSession(t)
	mgr := NewManager(dataDir, time.Second)
	mgr.LockWait = 5 * time.Second // a beat must not use it
	before, err := mgr.LoadManifest(sid)
	require.NoError(t, err)

	holder := startExclHolder(t, dataDir, sid, 0)
	start := time.Now()
	require.NoError(t, mgr.touchHeartbeat(sid), "a busy lock skips the beat quietly")
	assert.Less(t, time.Since(start), time.Second, "and at once: a beat never waits")
	holder.release(t)

	skipped, err := mgr.LoadManifest(sid)
	require.NoError(t, err)
	assert.Equal(t, before.LastHeartbeat, skipped.LastHeartbeat, "the skipped beat wrote nothing")

	time.Sleep(5 * time.Millisecond) // the clock must move for a beat to show
	require.NoError(t, mgr.touchHeartbeat(sid))
	beat, err := mgr.LoadManifest(sid)
	require.NoError(t, err)
	assert.True(t, beat.LastHeartbeat.After(before.LastHeartbeat), "a free lock beats")
}
