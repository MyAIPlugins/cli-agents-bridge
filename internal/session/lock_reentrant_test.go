package session

// Criterion 4 of the BAR: the lock is re-entrant inside one process, and only
// there. next.go takes the session lock and, inside it, CommitWakeCursor takes it
// again; the inner release must not free the outer hold. Every claim about "another
// party" is checked with another PROCESS (probeLock): asked in the same process, the
// registry would answer for us.

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcquireLock_Reentrant_SamePID(t *testing.T) {
	t.Parallel()
	lockPath := filepath.Join(t.TempDir(), "session.lock")

	outer, err := AcquireLock(lockPath, 0, nil)
	require.NoError(t, err)
	inner, err := AcquireLock(lockPath, 0, nil)
	require.NoError(t, err, "re-entrant acquire from the same process must succeed")

	require.NoError(t, inner())
	assert.False(t, probeLock(t, lockPath), "after the INNER release another process still finds the lock busy")

	require.NoError(t, outer())
	assert.True(t, probeBecomesFree(t, lockPath), "after the outer release it becomes free")
}

func TestAcquireLock_ReleasingTwiceDoesNotLowerTheCount(t *testing.T) {
	t.Parallel()
	lockPath := filepath.Join(t.TempDir(), "session.lock")

	outer, err := AcquireLock(lockPath, 0, nil)
	require.NoError(t, err)
	inner, err := AcquireLock(lockPath, 0, nil)
	require.NoError(t, err)

	require.NoError(t, inner())
	require.NoError(t, inner(), "a repeated release is harmless")
	require.NoError(t, inner())
	assert.False(t, probeLock(t, lockPath), "a release repeated three times is still ONE release: the outer hold stands")

	require.NoError(t, outer())
	assert.True(t, probeBecomesFree(t, lockPath))
}

// Re-entrancy is per process, not per goroutine, and that is the declared
// behaviour: a second goroutine asking for a lock the first one holds gets it. The
// exclusion is between processes, which is what the kernel gives.
func TestAcquireLock_ASecondGoroutineEntersWhileTheFirstHolds(t *testing.T) {
	t.Parallel()
	lockPath := filepath.Join(t.TempDir(), "session.lock")

	first, err := AcquireLock(lockPath, 0, nil)
	require.NoError(t, err)

	entered := make(chan error, 1)
	go func() {
		second, serr := AcquireLock(lockPath, 0, nil)
		if serr == nil {
			serr = second()
		}
		entered <- serr
	}()
	select {
	case serr := <-entered:
		assert.NoError(t, serr)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the second goroutine did not get the lock")
	}
	require.NoError(t, first())
}

// The holds of two goroutines are released in whatever order they finish, not in the
// order they were taken. The last release of the two is the one that frees the lock,
// whichever goroutine it belongs to.
func TestAcquireLock_ReleasesInAnyOrderBetweenTwoGoroutines(t *testing.T) {
	t.Parallel()

	for _, firstReleasesFirst := range []bool{true, false} {
		lockPath := filepath.Join(t.TempDir(), "session.lock")

		var wg sync.WaitGroup
		releases := make([]func() error, 2)
		for i := range releases {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rel, err := AcquireLock(lockPath, time.Second, nil)
				assert.NoError(t, err)
				releases[i] = rel
			}()
		}
		wg.Wait()
		require.NotNil(t, releases[0])
		require.NotNil(t, releases[1])

		order := []int{0, 1}
		if !firstReleasesFirst {
			order = []int{1, 0}
		}
		require.NoError(t, releases[order[0]]())
		assert.False(t, probeLock(t, lockPath), "one hold is still standing (releasing goroutine %d first)", order[0])
		require.NoError(t, releases[order[1]]())
		assert.True(t, probeBecomesFree(t, lockPath), "the last release frees the lock (goroutine %d last)", order[1])
	}
}

// L8. Two spellings of one directory are two keys of the registry, so they are not
// re-entrant with each other: they contend in the kernel and the second one gives
// up with ErrLockHeld after its wait. It is a limit, and this is the test that says
// it is the limit and not a surprise.
func TestAcquireLock_TwoSpellingsOfTheSamePathContend(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	require.NoError(t, os.Mkdir(realDir, 0o700))
	linkDir := filepath.Join(root, "alias")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	held, err := AcquireLock(filepath.Join(realDir, "lock"), 0, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = held() })

	_, err = AcquireLock(filepath.Join(linkDir, "lock"), 50*time.Millisecond, nil)
	assert.ErrorIs(t, err, ErrLockHeld)
}

// The nested case that exists in production (next.go): WithSessionLock, and inside
// it CommitWakeCursor, which takes the same lock.
func TestWithSessionLock_NestedCommitWakeCursorWorks(t *testing.T) {
	t.Parallel()
	mgr, sid := registeredSession(t)

	err := mgr.WithSessionLock(sid, func() error {
		evicted, cerr := mgr.CommitWakeCursor(sid, []string{"msg-aaaaaaaaaaaa"}, time.Now().UTC(), nil, nil)
		assert.False(t, evicted)
		assert.False(t, probeLock(t, mgr.lockBase(sid)), "the inner commit's release did not free the outer lock")
		return cerr
	})
	require.NoError(t, err)

	assert.True(t, probeBecomesFree(t, mgr.lockBase(sid)), "and it becomes free once the outer section is over")
	cursor, _, err := mgr.ReadWakeCursor(sid)
	require.NoError(t, err)
	assert.True(t, cursor.IsNotified("msg-aaaaaaaaaaaa"))
}
