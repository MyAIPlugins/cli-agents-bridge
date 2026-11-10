package session

// Criteria 5 and 9 of the BAR: a busy lock is waited for, with a numeric limit and
// an error that states facts; and an error of the call itself is neither contention
// nor a way through.

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcquire_AShortHoldByARealProcessIsWaitedFor(t *testing.T) {
	dataDir, sid := exclSession(t)
	mgr := NewManager(dataDir, time.Second)
	startExclHolder(t, dataDir, sid, 300)

	started := time.Now()
	release, err := mgr.AcquireSessionLock(sid, 2*time.Second)
	elapsed := time.Since(started)

	require.NoError(t, err, "a hold shorter than the wait must be waited for, not refused")
	require.NoError(t, release())
	assert.GreaterOrEqual(t, elapsed, 250*time.Millisecond, "it really waited: the holder let go after 300 ms")
	assert.Less(t, elapsed, 2*time.Second)
}

func TestAcquire_AHoldLongerThanTheWaitGivesErrLockHeldWithTheFacts(t *testing.T) {
	dataDir, sid := exclSession(t)
	mgr := NewManager(dataDir, time.Second)
	holder := startExclHolder(t, dataDir, sid, 0)

	started := time.Now()
	release, err := mgr.AcquireSessionLock(sid, 200*time.Millisecond)
	elapsed := time.Since(started)

	require.Error(t, err)
	assert.Nil(t, release)
	assert.ErrorIs(t, err, ErrLockHeld)
	assert.GreaterOrEqual(t, elapsed, 190*time.Millisecond)
	assert.Less(t, elapsed, time.Second)
	assert.Contains(t, err.Error(), strconv.Quote(mgr.lockBase(sid)))
	assert.Contains(t, err.Error(), "still held after waiting 200ms")
	assert.NotContains(t, err.Error(), "--", "no flag, no remedy: only the facts")

	holder.release(t)
}

func TestAcquire_ZeroWaitIsOneAttempt(t *testing.T) {
	dataDir, sid := exclSession(t)
	mgr := NewManager(dataDir, time.Second)
	holder := startExclHolder(t, dataDir, sid, 0)

	started := time.Now()
	_, err := mgr.AcquireSessionLock(sid, 0)
	elapsed := time.Since(started)

	assert.ErrorIs(t, err, ErrLockHeld)
	assert.Less(t, elapsed, 100*time.Millisecond, "wait <= 0 does not sleep")

	started = time.Now()
	_, err = mgr.AcquireSessionLock(sid, -time.Second)
	assert.ErrorIs(t, err, ErrLockHeld, "a negative wait is also one attempt")
	assert.Less(t, time.Since(started), 100*time.Millisecond)

	holder.release(t)
}

func TestAcquire_TheNumberOfAttemptsIsBoundedByWaitOverPoll(t *testing.T) {
	t.Parallel()
	lockPath := filepath.Join(t.TempDir(), "session.lock")
	holdElsewhere(t, lockPath)

	const wait = 200 * time.Millisecond
	attempts := 0
	_, err := acquire(lockPath, acquireOpts{wait: wait, afterOpen: func() { attempts++ }})

	require.ErrorIs(t, err, ErrLockHeld)
	assert.LessOrEqual(t, attempts, int(wait/lockPoll)+1, "the loop has a numeric limit")
	assert.Greater(t, attempts, 1, "and it really retried")
}

// Criterion 9. These are errors of the CALL: the lock was never contended, so they
// must not be reported as ErrLockHeld (a caller would retry or skip), must not be
// retried, and must not be a way through.
func TestAcquire_AnErrorOfTheCallIsImmediateAndNotContention(t *testing.T) {
	t.Parallel()

	t.Run("the parent directory is missing", func(t *testing.T) {
		t.Parallel()
		lockPath := filepath.Join(t.TempDir(), "missing", "lock")
		assertImmediateFailure(t, lockPath)
	})

	t.Run("a directory where the lock should be", func(t *testing.T) {
		t.Parallel()
		lockPath := filepath.Join(t.TempDir(), "lock")
		require.NoError(t, os.Mkdir(lockPath, 0o700))
		assertImmediateFailure(t, lockPath)
	})

	t.Run("a link where the lock should be, target intact", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		target := filepath.Join(dir, "target")
		require.NoError(t, os.WriteFile(target, []byte("precious"), 0o600))
		lockPath := filepath.Join(dir, "lock")
		if err := os.Symlink(target, lockPath); err != nil {
			t.Skipf("cannot create a symlink here: %v", err)
		}
		assertImmediateFailure(t, lockPath)
		got, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, "precious", string(got), "the target of the link must be untouched")
	})

	t.Run("a dangling link where the lock should be, nothing created", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		target := filepath.Join(dir, "nowhere")
		lockPath := filepath.Join(dir, "lock")
		if err := os.Symlink(target, lockPath); err != nil {
			t.Skipf("cannot create a symlink here: %v", err)
		}
		assertImmediateFailure(t, lockPath)
		assert.NoFileExists(t, target, "the lock must not be created through the link")
	})
}

func assertImmediateFailure(t *testing.T, lockPath string) {
	t.Helper()
	started := time.Now()
	attempts := 0
	// A long wait: if the failure were retried as contention, this would take two
	// seconds and count many attempts.
	release, err := acquire(lockPath, acquireOpts{wait: 2 * time.Second, precheck: func() error { attempts++; return nil }})
	elapsed := time.Since(started)

	require.Error(t, err)
	assert.Nil(t, release)
	assert.False(t, errors.Is(err, ErrLockHeld), "an error of the call is not contention: %v", err)
	assert.Equal(t, 1, attempts, "and it is not retried")
	assert.Less(t, elapsed, 500*time.Millisecond)
	assert.False(t, heldInThisProcess(lockPath), "nothing is left held")
}
