//go:build !windows

package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/fslock"
)

// CONTROL for the retirement order, not a property of the product. A kernel lock is a
// lock on an inode: unlink the file while somebody holds it and the next process
// creates a new inode at the same name and locks that one, with the first holder
// still holding. That is why cleanup removes the lock file only AFTER releasing it,
// why the manifest goes first so a waiter that opened the old inode finds the session
// gone, and why the sequence tests exist. If this test ever stops passing, the
// platform no longer behaves the way those arguments assume and they must be redone.
func TestLock_RemovingTheFileOfAHeldLockLetsASecondHolderIn(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "session.lock")
	holdElsewhere(t, path) // a holder on its own descriptor, outside the registry

	_, err := AcquireLock(path, 0, nil)
	require.ErrorIs(t, err, ErrLockHeld, "premise: while the file is in place the second process is refused")

	require.NoError(t, os.Remove(path))
	release, err := AcquireLock(path, 0, nil)

	require.NoError(t, err, "with the file unlinked a second process locks a NEW inode at the same name: two holders")
	require.NoError(t, release())
	_, terr := fslock.TryLock(path)
	require.NoError(t, terr, "and the first holder's lock never was on this file")
}
