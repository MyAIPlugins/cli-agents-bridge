package session

// F-160, CRI diff-gate round 2: the identity of the locked file. A lock taken on a
// file that the name does not point to any more is not a lock (checkNamed), and
// acquire starts again from the name as it is now, a bounded number of times.
// The process-level case is in lock_replaced_test.go; here the seam afterOpen does the
// replacing in-process, so each branch of the check has its own small test.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/fslock"
)

// replaceTheFile is what afterOpen does in these tests: the named file is unlinked and
// another one takes its name, as a retirement followed by a re-creation would.
func replaceTheFile(t *testing.T, lockPath string) {
	t.Helper()
	require.NoError(t, os.Remove(lockPath))
	fresh, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	require.NoError(t, fresh.Close())
}

func TestAcquire_AFileReplacedBetweenOpenAndLockIsLockedAgainByName(t *testing.T) {
	t.Parallel()
	lockPath := filepath.Join(t.TempDir(), "session.lock")
	opened, validated := 0, 0

	release, err := acquire(lockPath, acquireOpts{
		afterOpen: func() {
			opened++
			if opened == 1 {
				replaceTheFile(t, lockPath)
			}
		},
		validate: func() error { validated++; return nil },
	})

	require.NoError(t, err)
	t.Cleanup(func() { _ = release() })
	assert.Equal(t, 2, opened, "the name was opened again after the first file was found replaced")
	assert.Equal(t, 1, validated, "validate ran once, on the lock that counts")
	_, serr := fslock.TryLock(lockPath)
	assert.ErrorIs(t, serr, fslock.ErrBusy, "the lock held is on the file that carries the name: a second open of the name is refused")
}

func TestAcquire_AFileReplacedEveryTimeIsAnErrorAfterABound(t *testing.T) {
	t.Parallel()
	lockPath := filepath.Join(t.TempDir(), "session.lock")
	opened, validated := 0, 0

	release, err := acquire(lockPath, acquireOpts{
		wait: time.Second,
		afterOpen: func() {
			opened++
			replaceTheFile(t, lockPath)
		},
		validate: func() error { validated++; return nil },
	})

	require.Error(t, err)
	assert.Nil(t, release)
	assert.ErrorIs(t, err, errLockFileReplaced)
	assert.NotErrorIs(t, err, ErrLockHeld, "a replaced file is not contention")
	assert.Contains(t, err.Error(), "replaced")
	assert.Equal(t, maxReplaced, opened, "bounded: it tried maxReplaced times and stopped")
	assert.Zero(t, validated, "no round that found the file replaced got as far as validate")
	assert.False(t, heldInThisProcess(lockPath), "and no hold is left in the registry")
}

// The retirement itself, through the session API: the waiter's file is unlinked and
// the session is gone, so the second round stops at the precheck and creates nothing.
func TestAcquire_AFileReplacedByARetirementEndsAsASessionThatIsGone(t *testing.T) {
	t.Parallel()
	mgr, sid := registeredSession(t)

	release, err := mgr.acquireSessionLock(sid, 0, func() { retireByHand(t, mgr, sid) })

	require.ErrorIs(t, err, ErrSessionGone)
	assert.Nil(t, release)
	assert.NoDirExists(t, mgr.sessionDir(sid), "nothing was created where the session was")
}
