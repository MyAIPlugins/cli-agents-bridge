package cleanup

// F-160 C5: retirement under a kernel lock. The order is the contract (see the comment
// on archiveAndRemoveSession): the manifest goes first, under the lock, so a process
// that opened the lock file just before it disappears finds the session gone and writes
// nothing; the lock file goes after the release, and the directory last, with a plain
// rmdir that reports a leftover instead of deleting it.

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

const archiveDate = "2026-11-10" // failuresBase, as archiveAndRemoveSession formats it

func writeFileIn(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func sessionPath(dataDir, id string, elem ...string) string {
	return filepath.Join(append([]string{dataDir, "sessions", id}, elem...)...)
}

func requirePOSIXNonRoot(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX directory permissions, a non-root user, and a directory that can be removed while a lock is open")
	}
}

func TestArchiveAndRemoveSession_FullRetirementRemovesEverythingIncludingTheLock(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantStale(t, dataDir, "ffffffff")
	writeFileIn(t, sessionPath(dataDir, "ffffffff", "inbox", "msg-aaaaaaaaaaaa.json"), "unread")
	writeFileIn(t, sessionPath(dataDir, "ffffffff", "processed", "msg-bbbbbbbbbbbb.json"), "handled")
	writeFileIn(t, sessionPath(dataDir, "ffffffff", "outbox", "msg-cccccccccccc.json"), "sent")
	writeFileIn(t, sessionPath(dataDir, "ffffffff", "listener.json"), "{}")

	err := archiveAndRemoveSession(dataDir, "ffffffff", func() time.Time { return failuresBase }, nil)

	require.NoError(t, err)
	assert.NoDirExists(t, sessionPath(dataDir, "ffffffff"), "the directory goes, lock file and all")
	archived := filepath.Join(dataDir, "archive", archiveDate, "ffffffff")
	for _, rel := range []string{"inbox/msg-aaaaaaaaaaaa.json", "processed/msg-bbbbbbbbbbbb.json", "outbox/msg-cccccccccccc.json"} {
		assert.FileExists(t, filepath.Join(archived, filepath.FromSlash(rel)), "messages are archived before the removal")
	}
	var names []string
	require.NoError(t, filepath.WalkDir(filepath.Join(dataDir, "archive"), func(p string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		names = append(names, d.Name())
		return nil
	}))
	assert.NotContains(t, names, session.LockFileName, "the lock file is not state worth keeping: it is never archived")
}

// The seam runs after the release and before the lock file and the directory are
// removed. At that point the session must already be gone for everyone else (no
// manifest), the lock must be free, and the lock file must be all that is left.
//
// What this does NOT show is that the manifest was removed WHILE the lock was held:
// retireSession runs inside the held region by construction, and the process that
// finds the session gone after waiting is the retired-waiter case in
// internal/session (TestAcquire_ARetiredInodeStopsAtValidate).
func TestArchiveAndRemoveSession_ByTheReleaseTheSessionIsGoneAndOnlyTheLockFileIsLeft(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantStale(t, dataDir, "aaaaaaab")
	writeFileIn(t, sessionPath(dataDir, "aaaaaaab", "inbox", "msg-aaaaaaaaaaaa.json"), "unread")
	mgr := session.NewManager(dataDir, 0)

	calls := 0
	seam := func() {
		calls++
		assert.NoFileExists(t, sessionPath(dataDir, "aaaaaaab", "manifest.json"), "the manifest was removed under the lock")
		entries, err := os.ReadDir(sessionPath(dataDir, "aaaaaaab"))
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, session.LockFileName, entries[0].Name(), "only the lock file is left")

		_, lerr := mgr.AcquireSessionLock("aaaaaaab", 0)
		assert.ErrorIs(t, lerr, session.ErrSessionGone, "a late arrival is refused: the session is gone, not busy")

		again, terr := tryLockWhenFree(sessionPath(dataDir, "aaaaaaab", session.LockFileName))
		require.NoError(t, terr, "and the lock itself was released")
		require.NoError(t, again())
	}

	require.NoError(t, archiveAndRemoveSession(dataDir, "aaaaaaab", func() time.Time { return failuresBase }, seam))
	assert.Equal(t, 1, calls, "the seam ran once")
	assert.NoDirExists(t, sessionPath(dataDir, "aaaaaaab"))
}

// A leftover that appears after the release is reported, never deleted blind: the
// last step is a plain rmdir.
func TestArchiveAndRemoveSession_ALeftoverAfterTheReleaseIsAnErrorAndIsKept(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantStale(t, dataDir, "aaaaaaac")
	late := sessionPath(dataDir, "aaaaaaac", "late.json")

	err := archiveAndRemoveSession(dataDir, "aaaaaaac", func() time.Time { return failuresBase }, func() {
		writeFileIn(t, late, "arrived after the sweep")
	})

	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrSessionBusy), "got %v", err)
	assert.Contains(t, err.Error(), "directory could not be removed")
	assert.Contains(t, err.Error(), "holds no session any more and no cleanup pass will collect it", "the error says nobody will come back to the folder")
	assert.Contains(t, err.Error(), "removed by hand")
	assert.NotContains(t, err.Error(), "cab-bridge", "and it names no command")
	content, rerr := os.ReadFile(late)
	require.NoError(t, rerr, "the leftover is still there")
	assert.Equal(t, "arrived after the sweep", string(content))
	assert.NoFileExists(t, sessionPath(dataDir, "aaaaaaac", session.LockFileName), "the lock file went, as planned")
}

// The sentence is true: a folder with no manifest is not listed by any sweep, so a
// second pass over the same data dir leaves the leftover alone and says nothing about
// it. This is the fact the error states, held by a test instead of a comment.
func TestArchiveAndRemoveSession_ALeftoverFolderIsNotCollectedByALaterPass(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantStale(t, dataDir, "aaaaaa0c")
	late := sessionPath(dataDir, "aaaaaa0c", "late.json")
	require.Error(t, archiveAndRemoveSession(dataDir, "aaaaaa0c", func() time.Time { return failuresBase }, func() {
		writeFileIn(t, late, "arrived after the sweep")
	}))

	orphans, err := GCOrphans(dataDir, "", 1, func() time.Time { return failuresBase })

	require.NoError(t, err, "a later pass does not even notice the folder")
	assert.Empty(t, orphans)
	assert.FileExists(t, late, "and it does not collect it")
}

// The lock file that cannot be removed leaves the same folder behind, and says the same.
func TestArchiveAndRemoveSession_ALockFileThatCannotBeRemovedLeavesTheSameNote(t *testing.T) {
	requirePOSIXNonRoot(t)
	t.Parallel()
	dataDir := t.TempDir()
	plantStale(t, dataDir, "aaaaaa0d")
	dir := sessionPath(dataDir, "aaaaaa0d")

	err := archiveAndRemoveSession(dataDir, "aaaaaa0d", func() time.Time { return failuresBase }, func() {
		require.NoError(t, os.Chmod(dir, 0o500)) // the lock file can no longer be unlinked
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "lock file could not be removed")
	assert.Contains(t, err.Error(), "no cleanup pass will collect it")
	assert.FileExists(t, sessionPath(dataDir, "aaaaaa0d", session.LockFileName), "the lock file is still there")
}

// Something under the session that cannot be removed: the sweep fails with the
// reason (not busy), the session stays retired for everyone else, and what is left
// is exactly what was there. N processes could be waiting; here N goroutines each
// with its own manager, which contend on the same files.
func TestArchiveAndRemoveSession_AnObstacleLeavesARetiredSessionNobodyCanLock(t *testing.T) {
	requirePOSIXNonRoot(t)
	t.Parallel()
	dataDir := t.TempDir()
	plantStale(t, dataDir, "aaaaaaad")
	obstacle := sessionPath(dataDir, "aaaaaaad", "obstacle")
	writeFileIn(t, filepath.Join(obstacle, "stuck.json"), "cannot be unlinked")
	require.NoError(t, os.Chmod(obstacle, 0o500))
	t.Cleanup(func() {
		if err := os.Chmod(obstacle, 0o700); err != nil {
			t.Logf("restore permissions of %s: %v", obstacle, err)
		}
	})

	err := archiveAndRemoveSession(dataDir, "aaaaaaad", func() time.Time { return failuresBase }, nil)

	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrSessionBusy), "an obstacle is a failure with a reason, not contention: %v", err)
	assert.Contains(t, err.Error(), "obstacle")
	assert.NoFileExists(t, sessionPath(dataDir, "aaaaaaad", "manifest.json"), "the session was retired first")

	before := listTree(t, sessionPath(dataDir, "aaaaaaad"))
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = session.NewManager(dataDir, 0).AcquireSessionLock("aaaaaaad", 0)
		}()
	}
	wg.Wait()
	for i, e := range errs {
		assert.ErrorIs(t, e, session.ErrSessionGone, "waiter %d", i)
	}
	assert.Equal(t, before, listTree(t, sessionPath(dataDir, "aaaaaaad")), "and none of them wrote anything")
}

func listTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(root, p)
		require.NoError(t, rerr)
		out = append(out, rel)
		return nil
	}))
	return out
}

// A reply in flight refuses the retirement BEFORE anything is touched: the manifest
// is still there, nothing is archived.
func TestArchiveAndRemoveSession_AReplyInFlightIsBusyAndNothingIsTouched(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantStale(t, dataDir, "aaaaaaae")
	writeFileIn(t, sessionPath(dataDir, "aaaaaaae", "inbox", "msg-aaaaaaaaaaaa.json"), "unread")
	writeFileIn(t, sessionPath(dataDir, "aaaaaaae", "reply-txn.json"), "{}")

	err := archiveAndRemoveSession(dataDir, "aaaaaaae", func() time.Time { return failuresBase }, nil)

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSessionBusy), "got %v", err)
	assert.FileExists(t, sessionPath(dataDir, "aaaaaaae", "manifest.json"), "a session mid-transaction is not abandoned: it stays alive")
	assert.FileExists(t, sessionPath(dataDir, "aaaaaaae", "inbox", "msg-aaaaaaaaaaaa.json"))
	assert.NoDirExists(t, filepath.Join(dataDir, "archive"), "and nothing was archived")
}

// A directory with no manifest is a session already retired: cleanup says so with
// ErrSessionGone (which the sweeps count as "nothing to do") and leaves it alone.
func TestArchiveAndRemoveSession_ARetiredDirectoryIsGoneNotBusyNotFailed(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	writeFileIn(t, sessionPath(dataDir, "aaaaaaaf", "inbox", "msg-aaaaaaaaaaaa.json"), "left behind")

	err := archiveAndRemoveSession(dataDir, "aaaaaaaf", func() time.Time { return failuresBase }, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, session.ErrSessionGone)
	assert.False(t, errors.Is(err, ErrSessionBusy))
	assert.FileExists(t, sessionPath(dataDir, "aaaaaaaf", "inbox", "msg-aaaaaaaaaaaa.json"), "and nothing was touched")
	assert.NoFileExists(t, sessionPath(dataDir, "aaaaaaaf", session.LockFileName), "not even a lock file was created in it")
}

// A notify-watch lock lives INSIDE the session directory, so retirement unlinks it
// while the watcher still holds it. The old watcher ends normally (its lock was an
// unlinked inode), and a new one with the same name is refused: it must not be able
// to create a lock in the place of a session that is gone.
func TestRun_ARetiredSessionRefusesANewWatcherOfTheSameName(t *testing.T) {
	requirePOSIXNonRoot(t)
	t.Parallel()
	dataDir := t.TempDir()
	plantStale(t, dataDir, "aaaaaaag")
	mgr := session.NewManager(dataDir, 0)
	releaseOld, err := mgr.AcquireWatcherLock("aaaaaaag", "w")
	require.NoError(t, err)
	waitSessionLockFree(t, dataDir, "aaaaaaag") // the session lock AcquireWatcherLock took is released; the retirement does not wait

	require.NoError(t, archiveAndRemoveSession(dataDir, "aaaaaaag", func() time.Time { return failuresBase }, nil))

	assert.NoError(t, releaseOld(), "the old watcher lets go of an unlinked inode without a complaint")
	_, err = mgr.AcquireWatcherLock("aaaaaaag", "w")
	assert.ErrorIs(t, err, session.ErrSessionGone)
	assert.NoDirExists(t, sessionPath(dataDir, "aaaaaaag"), "and nothing recreated the directory")
}

// The manifest is removed first and on its own, with a plain remove. If it cannot be
// removed the retirement stops there and touches nothing else: a session must never
// end up gutted but still looking alive (a manifest, and no inbox behind it).
//
// The fixture makes the manifest unremovable by a non-recursive remove, and the test
// passes for the right reason only if everything else is still there afterwards: a
// retirement that removed every entry the same way, manifest included, would take the
// "manifest" directory with it and report success.
func TestArchiveAndRemoveSession_IfTheManifestCannotBeRemovedNothingElseIsTouched(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantStale(t, dataDir, "aaaaaaah")
	manifest := sessionPath(dataDir, "aaaaaaah", "manifest.json")
	require.NoError(t, os.Remove(manifest))
	writeFileIn(t, filepath.Join(manifest, "inside.json"), "keeps the directory from being removed non-recursively")
	writeFileIn(t, sessionPath(dataDir, "aaaaaaah", "listener.json"), "{}")
	writeFileIn(t, sessionPath(dataDir, "aaaaaaah", "findings", "f.json"), "{}")

	err := archiveAndRemoveSession(dataDir, "aaaaaaah", func() time.Time { return failuresBase }, nil)

	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrSessionBusy), "got %v", err)
	assert.Contains(t, err.Error(), "remove manifest")
	assert.FileExists(t, sessionPath(dataDir, "aaaaaaah", "listener.json"), "nothing else was removed")
	assert.FileExists(t, sessionPath(dataDir, "aaaaaaah", "findings", "f.json"))
	assert.FileExists(t, filepath.Join(manifest, "inside.json"))
	assert.FileExists(t, sessionPath(dataDir, "aaaaaaah", session.LockFileName), "and the lock file is still there")
}
