package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcquireLock_Fresh(t *testing.T) {
	t.Parallel()

	lockPath := filepath.Join(t.TempDir(), "session.lock")
	release, err := AcquireLock(lockPath, false)
	require.NoError(t, err)
	require.NotNil(t, release)
	t.Cleanup(func() { _ = release() })

	info, err := os.Stat(lockPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// Content is our PID
	got, err := readPIDFromLock(lockPath)
	require.NoError(t, err)
	assert.Equal(t, os.Getpid(), got)
}

func TestAcquireLock_HeldByLiveProcess_ReturnsErrLockHeld(t *testing.T) {
	t.Parallel()

	lockPath := filepath.Join(t.TempDir(), "session.lock")

	// Plant a lock owned by a process we know is alive: our own PID
	// (but not via AcquireLock so the test path is not "re-entrant").
	// Simulate "other live process" by writing a different live PID:
	// init (PID 1) is alive on every Unix system.
	require.NoError(t, os.WriteFile(lockPath, []byte("1\n"), 0o600))

	release, err := AcquireLock(lockPath, false)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLockHeld)
	assert.Nil(t, release)

	// Lock file must still exist — AcquireLock must NOT remove a live lock
	_, statErr := os.Stat(lockPath)
	assert.NoError(t, statErr, "AcquireLock must not remove a live lock")
}

func TestAcquireLock_StaleRecovery(t *testing.T) {
	t.Parallel()

	lockPath := filepath.Join(t.TempDir(), "session.lock")

	// PID 999999 is virtually guaranteed to not exist (max PID typically
	// 32768 on Linux, 99999 on macOS). If kernel.pid_max is higher than
	// this on some host, the test is a false negative (would fail because
	// the random PID is taken) — acceptable risk for test reliability.
	require.NoError(t, os.WriteFile(lockPath, []byte("999999\n"), 0o600))

	release, err := AcquireLock(lockPath, false)
	require.NoError(t, err, "stale lock with dead PID must be recovered")
	require.NotNil(t, release)
	t.Cleanup(func() { _ = release() })

	got, err := readPIDFromLock(lockPath)
	require.NoError(t, err)
	assert.Equal(t, os.Getpid(), got, "lock must now contain our PID")
}

func TestAcquireLock_MalformedLock_RecoveredAsStale(t *testing.T) {
	t.Parallel()

	lockPath := filepath.Join(t.TempDir(), "session.lock")
	require.NoError(t, os.WriteFile(lockPath, []byte("not-a-pid\n"), 0o600))

	release, err := AcquireLock(lockPath, false)
	require.NoError(t, err, "malformed lock must be treated as stale")
	require.NotNil(t, release)
	t.Cleanup(func() { _ = release() })
}

func TestAcquireLock_ForceNew_OverridesLive(t *testing.T) {
	t.Parallel()

	lockPath := filepath.Join(t.TempDir(), "session.lock")
	// Live lock (PID 1)
	require.NoError(t, os.WriteFile(lockPath, []byte("1\n"), 0o600))

	release, err := AcquireLock(lockPath, true) // forceNew=true
	require.NoError(t, err, "forceNew must override live lock")
	require.NotNil(t, release)
	t.Cleanup(func() { _ = release() })

	got, err := readPIDFromLock(lockPath)
	require.NoError(t, err)
	assert.Equal(t, os.Getpid(), got)
}

func TestAcquireLock_Reentrant_SamePID(t *testing.T) {
	t.Parallel()

	lockPath := filepath.Join(t.TempDir(), "session.lock")

	rel1, err := AcquireLock(lockPath, false)
	require.NoError(t, err)
	defer rel1()

	// Second acquire from same process: re-entrant, must succeed with no-op release
	rel2, err := AcquireLock(lockPath, false)
	require.NoError(t, err, "re-entrant acquire from same PID must succeed")
	require.NotNil(t, rel2)
	require.NoError(t, rel2(), "re-entrant release must be no-op")

	// Original lock still in place after re-entrant release
	_, statErr := os.Stat(lockPath)
	assert.NoError(t, statErr, "re-entrant release must not remove the original lock")
}

func TestAcquireLock_Release_RemovesFile(t *testing.T) {
	t.Parallel()

	lockPath := filepath.Join(t.TempDir(), "session.lock")
	release, err := AcquireLock(lockPath, false)
	require.NoError(t, err)

	require.NoError(t, release())

	_, statErr := os.Stat(lockPath)
	assert.True(t, errors.Is(statErr, os.ErrNotExist), "release must remove lock file")
}

func TestIsProcessAlive(t *testing.T) {
	t.Parallel()

	// Self is always alive
	assert.True(t, isProcessAlive(os.Getpid()))

	// PID 1 (init) is always alive on Unix
	assert.True(t, isProcessAlive(1))

	// PID 0 / negative are invalid
	assert.False(t, isProcessAlive(0))
	assert.False(t, isProcessAlive(-1))

	// Very high PID is unlikely to exist (see test note in TestAcquireLock_StaleRecovery)
	assert.False(t, isProcessAlive(999999))
}
