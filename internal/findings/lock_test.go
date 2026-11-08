package findings

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Bounds of the waits in this file. They are limits of the TEST, not promises of
// the product: a loaded CI runner needs room, and a wait without a bound is a
// test that hangs instead of failing.
const (
	helperEnv          = "CAB_FINDINGS_TEST_HELPER"
	helperHoldLock     = "hold-lock"
	helperPollInterval = 20 * time.Millisecond
	helperPollAttempts = 500 // 10 s
)

func TestTryLockFile_FirstHolderGetsItSecondIsBusyAndReleaseFreesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "findings.lock")

	release, err := TryLockFile(path)
	require.NoError(t, err)

	_, err = TryLockFile(path)
	require.ErrorIs(t, err, ErrBusy, "a second open file in the same process contends too")

	require.NoError(t, release())

	again, err := TryLockFile(path)
	require.NoError(t, err, "the lock is free after the release")
	require.NoError(t, again())
}

func TestTryLockFile_ReleaseTwiceIsHarmless(t *testing.T) {
	release, err := TryLockFile(filepath.Join(t.TempDir(), "findings.lock"))
	require.NoError(t, err)
	require.NoError(t, release())
	assert.NoError(t, release())
}

func TestTryLockFile_CreatesTheFileOnceAndNeverWritesToIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "findings.lock")
	require.NoFileExists(t, path)

	release, err := TryLockFile(path)
	require.NoError(t, err)
	require.NoError(t, release())

	info, err := os.Stat(path)
	require.NoError(t, err, "the file stays after the release: it is never removed")
	assert.True(t, info.Mode().IsRegular())
	assert.Zero(t, info.Size(), "nothing is ever written to the lock file")
}

func TestTryLockFile_AMissingDirectoryIsAnErrorNotACreation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nope")
	_, err := TryLockFile(filepath.Join(dir, "findings.lock"))
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrBusy)
	assert.NoDirExists(t, dir)
}

func TestTryLockFile_RefusesADirectoryInPlaceOfTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "findings.lock")
	require.NoError(t, os.Mkdir(path, 0o700))
	_, err := TryLockFile(path)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrBusy)
}

// TestTryLockFile_SecondHolderIsBusyAndDeathFreesIt is the property the whole
// design rests on: a lock held by another PROCESS excludes this one, and the
// holder dying without cleanup (Kill: SIGKILL / TerminateProcess) frees it, with
// nothing to recover. It runs on Unix and on Windows; on Windows it is the test
// that shows ERROR_LOCK_VIOLATION is the contention code (lock_windows.go).
func TestTryLockFile_SecondHolderIsBusyAndDeathFreesIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "findings.lock")
	marker := filepath.Join(dir, "child-holds-the-lock")

	child := startLockHolder(t, path, marker)
	waitForFile(t, marker)

	_, err := TryLockFile(path)
	require.ErrorIs(t, err, ErrBusy, "the child holds the lock")

	require.NoError(t, child.Process.Kill())
	_ = child.Wait() // a killed process reports an error; that is the expected end

	var release func() error
	for attempt := 0; attempt < helperPollAttempts; attempt++ {
		release, err = TryLockFile(path)
		if err == nil {
			break
		}
		require.ErrorIs(t, err, ErrBusy, "while the kernel is still tearing the child down the answer is busy, never another error")
		time.Sleep(helperPollInterval)
	}
	require.NoError(t, err, "the lock must be free after the holder died (waited up to %d x %s)", helperPollAttempts, helperPollInterval)
	require.NoError(t, release())
}

// TestHelperProcess_HoldLock is the child of the test above, not a test: it runs
// only when re-executed with the helper env set. It takes the lock, leaves a
// marker so the parent knows it got there, and waits to be killed. The wait has
// a bound, so a parent that dies leaves no immortal child.
func TestHelperProcess_HoldLock(t *testing.T) {
	if os.Getenv(helperEnv) != helperHoldLock {
		t.Skip("helper process: runs only when re-executed by another test")
	}
	release, err := TryLockFile(os.Getenv(helperEnv + "_LOCK"))
	if err != nil {
		os.Exit(3)
	}
	if err := os.WriteFile(os.Getenv(helperEnv+"_MARKER"), []byte("held"), 0o600); err != nil {
		os.Exit(4)
	}
	time.Sleep(time.Minute)
	_ = release()
	os.Exit(0)
}

func startLockHolder(t *testing.T, lockPath, marker string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess_HoldLock$")
	cmd.Env = append(os.Environ(),
		helperEnv+"="+helperHoldLock, helperEnv+"_LOCK="+lockPath, helperEnv+"_MARKER="+marker)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return cmd
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	for attempt := 0; attempt < helperPollAttempts; attempt++ {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			require.NoError(t, err)
		}
		time.Sleep(helperPollInterval)
	}
	require.Failf(t, "marker never appeared", "%s after %d x %s", path, helperPollAttempts, helperPollInterval)
}
