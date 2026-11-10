package cleanup

// F-160, CRI diff-gate M4: the section of cleanup that removes a session stays under
// the session lock until the release. A test that holds the lock itself proves that
// cleanup REFUSES a busy session; it does not prove that cleanup, once it has the
// lock, keeps it while it works. A release moved too early, before the manifest is
// removed, leaves every other test of this package green, because none of them has
// anybody else in line at that moment.
//
// Here a real process is in line. It is started before the retirement and waits; the
// retirement runs its real section (reply-transaction check, archive, removal of the
// manifest and of every other entry) and, at the end of it, still under the lock, the
// seam lets the process try. It must find the lock held.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

const (
	contendEnv        = "CAB_CLEANUP_TEST_LOCK_CONTENDER"
	contendEnvLock    = contendEnv + "_LOCK"
	contendEnvMarkers = contendEnv + "_MARKERS"
)

// TestHelperProcess_LockContender is the child process, not a test. It says "loaded",
// waits for "go", tries once to take the lock file it was given, and leaves the
// verdict: busy, acquired, or the error it got.
func TestHelperProcess_LockContender(t *testing.T) {
	if os.Getenv(contendEnv) != "1" {
		return
	}
	markers := os.Getenv(contendEnvMarkers)
	if err := os.WriteFile(filepath.Join(markers, "loaded"), nil, 0o600); err != nil {
		os.Exit(2)
	}
	if !waitForFile(filepath.Join(markers, "go"), stallBound) {
		os.Exit(3)
	}
	verdict := "acquired"
	release, err := session.AcquireLock(os.Getenv(contendEnvLock), 0, nil)
	switch {
	case errors.Is(err, session.ErrLockHeld):
		verdict = "busy"
	case err != nil:
		verdict = "error: " + err.Error()
	default:
		_ = release()
	}
	if err := os.WriteFile(filepath.Join(markers, "result"), []byte(verdict), 0o600); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestRetirement_TheSectionOfCleanupHoldsTheLockUntilTheRelease(t *testing.T) {
	const id = "aaaaaa0e"
	dataDir := t.TempDir()
	plantStale(t, dataDir, id)
	lockFile := session.SessionLockBase(dataDir, id)
	writeFileIn(t, sessionPath(dataDir, id, "inbox", "msg-aaaaaaaaaaaa.json"), "unread")

	child := startHelperChild(t, "TestHelperProcess_LockContender", func(markers string) []string {
		return []string{contendEnv + "=1", contendEnvLock + "=" + lockFile, contendEnvMarkers + "=" + markers}
	})
	require.True(t, child.waitLoaded(), "premise: the contender is in line; stderr: %s", child.stderr.String())
	require.True(t, child.running(), "premise: and waiting")

	ranUnderLock := false
	err := retireWithSeams(dataDir, id, fixedNow, retireSeams{underLock: func() {
		ranUnderLock = true
		// The section got to its end: the premises are asserted, not assumed.
		assert.NoFileExists(t, sessionPath(dataDir, id, "manifest.json"), "premise: the manifest is already removed")
		assert.NoDirExists(t, sessionPath(dataDir, id, "inbox"), "premise: so is everything else except the lock file")
		assert.FileExists(t, lockFile, "premise: the lock file is still there")
		child.letGo(t)
		child.waitExit(t)
		assert.Equal(t, "busy", child.result(t), "the lock is held to the end of the section: a process in line cannot get it")
	}})

	require.NoError(t, err)
	require.True(t, ranUnderLock, "premise: the seam ran, so the section ran")
	assert.NoDirExists(t, sessionPath(dataDir, id), "and the retirement finished")
}
