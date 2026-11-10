//go:build !windows

package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Unix only: the lstat is made to fail by taking the search permission off the
// directory, which has no equivalent on Windows.
// A stat that fails for a reason other than "no such file" is a failure to report:
// "I could not look" is not "it is the same file", and not contention either.
func TestAcquire_ALstatThatFailsIsAFailureAndNotAnAnswer(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions: the lstat cannot be made to fail")
	}
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "session.lock")

	release, err := acquire(lockPath, acquireOpts{
		afterOpen: func() {
			require.NoError(t, os.Chmod(dir, 0o000))
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		},
	})

	require.Error(t, err)
	assert.Nil(t, release)
	assert.NotErrorIs(t, err, errLockFileReplaced)
	assert.NotErrorIs(t, err, ErrLockHeld)
	assert.Contains(t, err.Error(), "lstat the lock file")
	require.NoError(t, os.Chmod(dir, 0o700))
	assertLockIsFree(t, lockPath)
	assert.False(t, heldInThisProcess(lockPath))
}
