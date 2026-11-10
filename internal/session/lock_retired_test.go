package session

// A session that cleanup retired must be refused by EVERY method that takes its lock,
// before that method writes anything, and the notify-watch lock (which lives inside
// the session directory) must obey the same rule.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The table of B3: one row per method that takes the session lock. Each is run
// against a session directory that exists but has no manifest (what a late arrival
// finds while cleanup is removing the rest), and must answer ErrSessionGone and leave
// the directory exactly as it found it: no lock file created, nothing written.
func TestRetiredSession_EveryLockingMethodStopsBeforeItsFirstWrite(t *testing.T) {
	t.Parallel()

	ids := []string{"msg-aaaaaaaaaaaa"}
	calls := map[string]func(m *Manager, sid string) error{
		"AcquireSessionLock": func(m *Manager, sid string) error { _, err := m.AcquireSessionLock(sid, 0); return err },
		"WithSessionLock": func(m *Manager, sid string) error {
			return m.WithSessionLock(sid, func() error { return errors.New("fn must not run on a retired session") })
		},
		"ClaimListener":   func(m *Manager, sid string) error { _, err := m.ClaimListener(sid); return err },
		"ReclaimListener": func(m *Manager, sid string) error { _, err := m.ReclaimListener(sid); return err },
		"StartWait":       func(m *Manager, sid string) error { _, err := m.StartWait(sid); return err },
		"CommitWakeCursor": func(m *Manager, sid string) error {
			_, err := m.CommitWakeCursor(sid, ids, time.Now().UTC(), nil, nil)
			return err
		},
		"ForgetNotified": func(m *Manager, sid string) error { return m.ForgetNotified(sid, ids) },
		"RenameAgent":    func(m *Manager, sid string) error { return m.RenameAgent(sid, "ESC-renamed") },
		"SetRole":        func(m *Manager, sid string) error { return m.SetRole(sid, RoleEsc) },
		"AcquireWatcherLock": func(m *Manager, sid string) error {
			_, err := m.AcquireWatcherLock(sid, "w")
			return err
		},
		"touchHeartbeatOwned": func(m *Manager, sid string) error {
			_, err := m.touchHeartbeatOwned(sid, func() bool { return true })
			return err
		},
		// The writers that used to take only manifestMu and could bring a retired
		// session's manifest back (F-160, CRI diff-gate blocker). Every one of them
		// now stops at the lock, and a retired session is an error, not a skipped beat.
		"SetState":        func(m *Manager, sid string) error { return m.SetState(sid, StateIdle) },
		"SetLastConsumed": func(m *Manager, sid string) error { return m.SetLastConsumed(sid, "msg-aaaaaaaaaaaa") },
		"Touch":           func(m *Manager, sid string) error { return m.Touch(sid) },
		"AdoptPID":        func(m *Manager, sid string) error { return m.AdoptPID(sid) },
		"adoptPIDLocked":  func(m *Manager, sid string) error { return m.adoptPIDLocked(sid) },
		"adoptAndBackfill": func(m *Manager, sid string) error {
			_, err := m.adoptAndBackfill(sid, "scope")
			return err
		},
		"touchHeartbeat": func(m *Manager, sid string) error { return m.touchHeartbeat(sid) },
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mgr, sid := registeredSession(t)
			retireByHand(t, mgr, sid)
			require.NoError(t, os.MkdirAll(filepath.Join(mgr.sessionDir(sid), "inbox"), 0o700))
			before := dirNames(t, mgr.sessionDir(sid))

			err := call(mgr, sid)

			require.ErrorIs(t, err, ErrSessionGone, "a retired session is refused")
			assert.Equal(t, before, dirNames(t, mgr.sessionDir(sid)), "and nothing was created or written in its directory")
		})
	}
}

// A session whose heartbeat meets a retired session reports it as a failure for the
// heartbeat's own counter (D7): it is not "contended", so the beat is not silently
// skipped. The beat for a session that is merely busy IS skipped.
func TestTouchHeartbeatOwned_SkipsTheBeatWhenBusyButReportsAFailingCall(t *testing.T) {
	dataDir, sid := exclSession(t)
	mgr := NewManager(dataDir, time.Second)

	holder := startExclHolder(t, dataDir, sid, 0)
	evicted, err := mgr.touchHeartbeatOwned(sid, func() bool { return true })
	assert.NoError(t, err, "a busy lock skips the beat quietly")
	assert.False(t, evicted)
	holder.release(t)

	before, err := mgr.LoadManifest(sid)
	require.NoError(t, err)
	evicted, err = mgr.touchHeartbeatOwned(sid, func() bool { return true })
	require.NoError(t, err)
	assert.False(t, evicted)
	after, err := mgr.LoadManifest(sid)
	require.NoError(t, err)
	assert.False(t, after.LastHeartbeat.Before(before.LastHeartbeat), "and a free lock beats")

	retireByHand(t, mgr, sid)
	_, err = mgr.touchHeartbeatOwned(sid, func() bool { return true })
	assert.ErrorIs(t, err, ErrSessionGone, "a failing call is reported, not skipped")
}

// R2-2. W1 is a watcher that already holds <session>/notify-watch/w.lock. W2 is
// stopped after its initial LoadManifest, the session is retired in the meantime,
// and W2 must be refused with ErrSessionGone and create NOTHING: a new watcher of the
// same name on a retired session would be a lock on a file nobody else can contend
// for.
func TestWatcherLock_RefusedAfterRetirement(t *testing.T) {
	dataDir, sid := exclSession(t)
	mgr := NewManager(dataDir, time.Second)
	w1 := startSeqChild(t, seqModeWatcher, dataDir, sid, "w")
	w1.expectLine(t, "HELD")

	release, err := mgr.acquireWatcherLock(sid, "w", func() { retireByHand(t, mgr, sid) })

	require.ErrorIs(t, err, ErrSessionGone)
	assert.Nil(t, release)
	assert.NoDirExists(t, mgr.sessionDir(sid), "nothing was created in the place of the retired session")

	_, err = mgr.AcquireWatcherLock(sid, "w")
	assert.ErrorIs(t, err, ErrSessionGone, "a third attempt gets the same answer")
	assert.NoDirExists(t, mgr.sessionDir(sid))

	w1.say(t, "RELEASE")
	assert.Zero(t, w1.wait(t), "W1 itself ends normally: its lock was an unlinked inode and nobody was disturbed")
}

func TestWatcherLock_SecondWatcherWithTheSameNameIsRefused(t *testing.T) {
	dataDir, sid := exclSession(t)
	mgr := NewManager(dataDir, time.Second)
	w1 := startSeqChild(t, seqModeWatcher, dataDir, sid, "w")
	w1.expectLine(t, "HELD")

	release, err := mgr.AcquireWatcherLock(sid, "w")

	require.ErrorIs(t, err, ErrLockHeld, "one watcher per (session, name): the second one stops at once")
	assert.Nil(t, release)
	assert.Contains(t, err.Error(), `"w"`)
	assert.NotContains(t, err.Error(), "--", "and the message names no remedy")

	w1.say(t, "RELEASE")
	require.Zero(t, w1.wait(t))

	again, err := mgr.AcquireWatcherLock(sid, "w")
	require.NoError(t, err, "once the first watcher is gone the name is free")
	require.NoError(t, again())
}

func TestWatcherLock_DifferentNamesBothSucceed(t *testing.T) {
	dataDir, sid := exclSession(t)
	mgr := NewManager(dataDir, time.Second)
	w1 := startSeqChild(t, seqModeWatcher, dataDir, sid, "w")
	w1.expectLine(t, "HELD")

	other, err := mgr.AcquireWatcherLock(sid, "v")
	require.NoError(t, err)
	require.NoError(t, other())

	w1.say(t, "RELEASE")
	require.Zero(t, w1.wait(t))
}

// The session directory of a session that is gone is never recreated by a watcher.
// (The Mkdir of the watcher directory is non-recursive and its ENOENT is a belt: it
// cannot happen while the session lock is held. What is shown here is the visible
// half, that the answer is ErrSessionGone and nothing appears.)
func TestWatcherLock_NeverRecreatesTheSessionDirectory(t *testing.T) {
	t.Parallel()
	mgr, sid := registeredSession(t)
	retireByHand(t, mgr, sid)

	_, err := mgr.AcquireWatcherLock(sid, "w")

	require.ErrorIs(t, err, ErrSessionGone)
	assert.NoDirExists(t, mgr.sessionDir(sid))
	assert.NoDirExists(t, filepath.Join(mgr.sessionDir(sid), NotifyWatchDir))
}
