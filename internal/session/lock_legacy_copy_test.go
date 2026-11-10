//go:build !windows

package session

// CONTROLS ON A COPY OF THE OLD ALGORITHM. Not the real function, and not evidence
// that the real function misbehaves: the real one is shown to misbehave by running
// lock_exclusion_test.go and lock_exclusion_holder_test.go on the tree before the
// fix (and, for window 2, by the proof in .handover/piani/f160-prova-forma3/).
//
// What this file is for is the other direction: a detector that counts zero proves
// nothing until it is shown to count when the lock really is broken. The algorithm
// below is the one F-160 replaced, and the same detector, run on it, must report
// overlaps. Windows 1 and 3 of the BAR are also forced here, deterministically.
//
// PROVENANCE: AcquireLock, tryCreate and readPIDFromLock of internal/session/lock.go
// at c0649ad (git blob f179a130846cbbb658c89d5f85ee187742064132, sha256 of the file
// b799cf300eb136d0cf52e42215ecbe894d6295513873cb58664a5dfd01a71115), copied as they
// were, renamed with a "legacy" prefix, plus TWO hook calls marked HOOK that do
// nothing while the hooks are nil. It is !windows because its search for a stale
// holder is kill(pid, 0).

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
)

var (
	legacyHookAfterCreate  func() // HOOK: runs after the O_EXCL create, before the PID is written
	legacyHookBeforeRemove func() // HOOK: runs after the dead PID was read, before the file is removed
)

func legacyAcquireLock(lockPath string, forceNew bool) (release func() error, err error) {
	if forceNew {
		_ = os.Remove(lockPath)
	}

	if rel, err := legacyTryCreate(lockPath); err == nil {
		return rel, nil
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}

	existingPID, perr := legacyReadPIDFromLock(lockPath)
	if perr != nil {
		_ = os.Remove(lockPath)
		return legacyTryCreate(lockPath)
	}

	if existingPID == os.Getpid() {
		return func() error { return nil }, nil
	}

	if IsProcessAlive(existingPID) {
		return nil, fmt.Errorf("%w: lockPath=%q holder pid=%d", ErrLockHeld, lockPath, existingPID)
	}

	if legacyHookBeforeRemove != nil { // HOOK
		legacyHookBeforeRemove()
	}
	_ = os.Remove(lockPath)
	return legacyTryCreate(lockPath)
}

func legacyTryCreate(lockPath string) (func() error, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if legacyHookAfterCreate != nil { // HOOK
		hook := legacyHookAfterCreate
		legacyHookAfterCreate = nil
		hook()
	}

	pid := os.Getpid()
	if _, werr := fmt.Fprintf(f, "%d\n", pid); werr != nil {
		_ = os.Remove(lockPath)
		return nil, fmt.Errorf("write PID to lock %q: %w", lockPath, werr)
	}

	return func() error {
		if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("release lock %q: %w", lockPath, err)
		}
		return nil
	}, nil
}

func legacyReadPIDFromLock(lockPath string) (int, error) {
	data, err := security.ReadOwnedFile(lockPath)
	if err != nil {
		return 0, err
	}
	s := strings.TrimRight(string(data), "\n")
	return strconv.Atoi(s)
}

// --- the controls -----------------------------------------------------------

// Window 1: the file is created and the PID not yet written. A second process reads
// an empty file, takes it for stale, removes it and creates its own: two holders.
// Not parallel: it uses the package-level hook.
func TestLegacyCopy_StillShowsWindow1(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "legacy.lock")
	t.Cleanup(func() { legacyHookAfterCreate = nil })

	secondHolds := false
	legacyHookAfterCreate = func() {
		// The first process is between create and write: this is the second one.
		_, err := legacyAcquireLock(lockPath, false)
		secondHolds = err == nil
	}
	_, err := legacyAcquireLock(lockPath, false)

	require.NoError(t, err, "the first process also believes it holds the lock")
	assert.True(t, secondHolds, "the second took the lock while the first was still creating it: the old algorithm admits two holders")
}

// Window 3: two processes see the same dead PID. Both decide to remove the file; the
// second removal deletes the lock the first one has just created.
func TestLegacyCopy_StillShowsWindow3(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "legacy.lock")
	t.Cleanup(func() { legacyHookBeforeRemove = nil })

	dead := exec.Command(os.Args[0], "-test.run=^$")
	require.NoError(t, dead.Run())
	require.False(t, IsProcessAlive(dead.Process.Pid), "premise: the planted PID belongs to a process that is gone")
	require.NoError(t, os.WriteFile(lockPath, []byte(strconv.Itoa(dead.Process.Pid)+"\n"), 0o600))

	firstHolds := false
	var firstFile os.FileInfo
	legacyHookBeforeRemove = func() {
		legacyHookBeforeRemove = nil
		// The other process runs now, between our read of the dead PID and our remove.
		if _, err := legacyAcquireLock(lockPath, false); err != nil {
			return
		}
		firstHolds = true
		// Keep the lock the other process created open, so its inode cannot be
		// reused by the one we are about to create and fool the comparison below.
		kept, err := os.Open(lockPath)
		if err != nil {
			return
		}
		t.Cleanup(func() { _ = kept.Close() })
		firstFile, _ = kept.Stat()
	}
	_, err := legacyAcquireLock(lockPath, false)

	require.NoError(t, err, "we also believe we hold the lock")
	require.True(t, firstHolds, "premise: the other process took the lock too")
	require.NotNil(t, firstFile, "premise: the other process's lock file was observed")
	finalFile, statErr := os.Stat(lockPath)
	require.NoError(t, statErr)
	assert.False(t, os.SameFile(firstFile, finalFile), "we removed the lock the other process had just created and replaced it: two holders")
}

// The same detector as the real test, pointed at the old algorithm, must see
// overlaps. Smaller volume: the old algorithm overlaps thousands of times in 12,000.
func TestLegacyCopy_ExclusionBenchSeesOverlaps(t *testing.T) {
	res := runExclusion(t, exclRun{procs: 8, total: 2000, sectionUS: 100, pauseUS: 50, helper: "TestHelperProcess_LegacyLock"})
	assert.Positive(t, res.overlaps, "the detector must report overlaps on an algorithm that has them (children: %v)", res.failures)
}

// TestHelperProcess_LegacyLock is the child of the bench above, not a test.
func TestHelperProcess_LegacyLock(t *testing.T) {
	if os.Getenv(exclHelperEnv) != exclModeLoop {
		t.Skip("helper process: runs only when re-executed by another test")
	}
	lockPath := filepath.Join(os.Getenv(exclEnvDir), "legacy.lock")
	os.Exit(exclLoopFromEnv().run(func(fn func() error) error {
		release, err := legacyAcquireLock(lockPath, false)
		if err != nil {
			return err
		}
		defer func() { _ = release() }()
		return fn()
	}))
}
