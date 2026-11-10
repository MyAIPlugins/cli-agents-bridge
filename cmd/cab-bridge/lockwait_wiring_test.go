package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/fslock"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// F-160: the configured wait reaches the Manager every command builds. A key that
// is read and never used would pass every config test and change nothing.
func TestNewSessionManager_TakesTheLockWaitFromTheConfig(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		ms   int
		want time.Duration
	}{
		{ms: 5000, want: 5 * time.Second},
		{ms: 200, want: 200 * time.Millisecond},
		{ms: 0, want: 0},
		{ms: -1, want: -1 * time.Millisecond},
	} {
		cfg := fixtureConfig(t, t.TempDir())
		cfg.SessionLockWaitMs = tc.ms
		assert.Equal(t, tc.want, newSessionManager(cfg).LockWait, "session_lock_wait_ms=%d", tc.ms)
	}
	assert.Equal(t, 5*time.Second, newSessionManager(fixtureConfig(t, t.TempDir())).LockWait, "and the default config waits 5 s")
}

// The same thing end to end: a Manager built from a config with a short wait gives up
// on a held lock after about that long (ClaimListener is one of the methods that take
// the session lock and wait for it), and with 0 gives up at once. The holder uses its
// own descriptor, so it is a real contender and not a re-entrant hold.
func TestNewSessionManager_TheConfiguredWaitIsTheWaitOfAMethodThatTakesTheLock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		ms       int
		atLeast  time.Duration
		atMost   time.Duration
		wantText string
	}{
		{ms: 250, atLeast: 250 * time.Millisecond, atMost: 3 * time.Second, wantText: "still held after waiting 250ms"},
		{ms: 0, atLeast: 0, atMost: time.Second, wantText: "still held after waiting 0s"},
	} {
		dataDir := t.TempDir()
		plantLiveSession(t, dataDir, "lwwire01")
		release, err := fslock.TryLock(filepath.Join(dataDir, "sessions", "lwwire01", session.LockFileName))
		require.NoError(t, err)
		cfg := fixtureConfig(t, dataDir)
		cfg.SessionLockWaitMs = tc.ms

		start := time.Now()
		_, cerr := newSessionManager(cfg).ClaimListener("lwwire01") // takes the session lock with the Manager's wait
		elapsed := time.Since(start)

		require.ErrorIs(t, cerr, session.ErrLockHeld, "session_lock_wait_ms=%d", tc.ms)
		assert.Contains(t, cerr.Error(), tc.wantText)
		assert.GreaterOrEqual(t, elapsed, tc.atLeast, "session_lock_wait_ms=%d", tc.ms)
		assert.Less(t, elapsed, tc.atMost, "session_lock_wait_ms=%d", tc.ms)
		require.NoError(t, release())
		_, statErr := os.Stat(filepath.Join(dataDir, "sessions", "lwwire01", "manifest.json"))
		require.NoError(t, statErr, "the session was never touched")
	}
}
