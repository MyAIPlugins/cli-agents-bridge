package session

// F-160, CRI diff-gate round 2, blocker R2-1: a process that opened the lock file
// before it was unlinked, and locked it after the same session id existed again, held
// "the" lock of that session on an inode nobody else was contending for, next to the
// holder of the new file. The waiter is a real process, stopped between the open and
// the lock (the same helper as lock_sequence_test.go); this process retires the
// session the way cleanup does (retireByHand) and then makes the id exist again the
// way an importer would. The real importer, migrate, is tested against the real
// binary in tests/integration/migrate_exclusive_test.go.
//
// These tests only use what was already here before the fix, so they compile and fail
// on the code they are about.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcquire_AWaiterOnAReplacedFileDoesNotShareTheLockWithItsNewHolder(t *testing.T) {
	dataDir, sid := exclSession(t)
	mgr := NewManager(dataDir, time.Second)
	manifest, err := mgr.LoadManifest(sid)
	require.NoError(t, err)
	lockPath := SessionLockBase(dataDir, sid)
	oldInfo, err := os.Stat(lockPath)
	require.NoError(t, err)

	child := startSeqChild(t, seqModeRetired, dataDir, sid, "")
	child.expectLine(t, "OPENED") // it holds a descriptor on the file as it is now

	retireByHand(t, mgr, sid)
	require.NoDirExists(t, mgr.sessionDir(sid), "premise: the retirement is complete")
	require.NoError(t, os.Mkdir(mgr.sessionDir(sid), 0o700), "the same id exists again")
	require.NoError(t, mgr.SaveManifest(manifest))
	holder, err := mgr.AcquireSessionLock(sid, 0)
	require.NoError(t, err, "premise: the new session can be locked")
	defer func() { require.NoError(t, holder()) }()
	newInfo, err := os.Stat(lockPath)
	require.NoError(t, err)
	require.False(t, os.SameFile(oldInfo, newInfo), "premise: the lock the child opened and the lock held now are two files")

	child.say(t, "GO")

	code := child.wait(t)
	assert.Equal(t, seqExitHeld, code,
		"the waiter must end up contending for the file that carries the name, and find it held (12 means it took the old inode next to the new holder): %s", child.stderr.String())
}

// The other barrier, on its own: the lock file is still the one the name points to
// (cleanup has removed the manifest and released the lock, and has not unlinked the
// file yet), so only validate can stop a waiter that locks it now.
func TestAcquire_AManifestlessSessionWithItsLockFileStopsAtValidate(t *testing.T) {
	dataDir, sid := exclSession(t)
	mgr := NewManager(dataDir, time.Second)
	dir := mgr.sessionDir(sid)

	child := startSeqChild(t, seqModeRetired, dataDir, sid, "")
	child.expectLine(t, "OPENED")

	require.NoError(t, os.Remove(filepath.Join(dir, "manifest.json")))
	for _, name := range dirNames(t, dir) {
		if name != LockFileName {
			require.NoError(t, os.RemoveAll(filepath.Join(dir, name)))
		}
	}
	require.Equal(t, []string{LockFileName}, dirNames(t, dir), "premise: the middle of a retirement, only the lock file is left")
	child.say(t, "GO")

	code := child.wait(t)
	assert.Equal(t, seqExitGone, code, "validate must refuse a session whose manifest is gone: %s", child.stderr.String())
	assert.Equal(t, []string{LockFileName}, dirNames(t, dir), "and nothing was created next to the lock file")
}
