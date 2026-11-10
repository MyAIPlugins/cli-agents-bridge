package cleanup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-160 C2: cleanup used to report ANY lock error as "busy" and then swallow it
// (`continue // best-effort`), so a sweep that could not touch a session looked
// exactly like a sweep with nothing to do. Busy is now a counted skip, and every
// other error is a failure with its reason.

var failuresBase = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func plantStale(t *testing.T, dataDir, id string) {
	t.Helper()
	plantSession(t, dataDir, id, deadPID, failuresBase.Add(-48*time.Hour))
}

// holdLockAsBusy makes the session look held by a live foreign process: the
// lock carries the pid of the test's parent. (F-160 C5 replaces the lock with a
// kernel lock; this helper is the only place that knows how to fake a holder.)
func holdLockAsBusy(t *testing.T, dataDir, id string) {
	t.Helper()
	lock := filepath.Join(dataDir, "sessions", id, "lock")
	require.NoError(t, os.WriteFile(lock, []byte(fmt.Sprintf("%d\n", os.Getppid())), 0o600))
}

// lockOutDir makes creating files inside the session dir fail with a permission
// error, which is a lock error that is NOT contention.
func lockOutDir(t *testing.T, dataDir, id string) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX directory permissions and a non-root user")
	}
	dir := filepath.Join(dataDir, "sessions", id)
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Logf("restore permissions of %s: %v", dir, err)
		}
	})
}

func sweepOpts(dataDir string) Options {
	return Options{
		DataDir:       dataDir,
		Scope:         ScopeGlobal,
		AllScopes:     true,
		StaleSeconds:  300,
		RetentionDays: 7,
		Now:           func() time.Time { return failuresBase },
	}
}

func TestRun_Global_ABusySessionIsSkippedCountedAndLeftIntact(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantStale(t, dataDir, "aaaaaaaa")
	plantStale(t, dataDir, "bbbbbbbb")
	holdLockAsBusy(t, dataDir, "bbbbbbbb")

	res, err := Run(context.Background(), sweepOpts(dataDir))
	require.NoError(t, err)

	assert.Equal(t, []string{"aaaaaaaa"}, res.SessionsRemoved)
	assert.Equal(t, 1, res.SkippedBusy)
	assert.Empty(t, res.SessionsFailed, "busy is a skip, not a failure")
	_, statErr := os.Stat(filepath.Join(dataDir, "sessions", "bbbbbbbb", "manifest.json"))
	assert.NoError(t, statErr, "the busy session must be left whole")
}

func TestRun_Global_ALockErrorThatIsNotContentionIsAFailureNotBusy(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantStale(t, dataDir, "aaaaaaaa")
	plantStale(t, dataDir, "bbbbbbbb")
	lockOutDir(t, dataDir, "bbbbbbbb")

	res, err := Run(context.Background(), sweepOpts(dataDir))
	require.NoError(t, err, "a per-session failure does not abort the sweep")

	assert.Equal(t, []string{"aaaaaaaa"}, res.SessionsRemoved, "the good session is still removed")
	assert.Zero(t, res.SkippedBusy, "a permission error is not contention")
	require.Len(t, res.SessionsFailed, 1)
	assert.Equal(t, "bbbbbbbb", res.SessionsFailed[0].ID)
	assert.Contains(t, res.SessionsFailed[0].Error, "acquire lock")
}

func TestArchiveAndRemoveSession_ALockErrorIsNotErrSessionBusy(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantStale(t, dataDir, "bbbbbbbb")
	lockOutDir(t, dataDir, "bbbbbbbb")

	err := archiveAndRemoveSession(dataDir, "bbbbbbbb", func() time.Time { return failuresBase })

	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrSessionBusy), "got %v", err)
}

func TestArchiveAndRemoveSession_ContentionIsErrSessionBusy(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantStale(t, dataDir, "bbbbbbbb")
	holdLockAsBusy(t, dataDir, "bbbbbbbb")

	err := archiveAndRemoveSession(dataDir, "bbbbbbbb", func() time.Time { return failuresBase })

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSessionBusy), "got %v", err)
}

func TestGCOrphans_ReturnsWhatItRemovedTogetherWithTheFailures(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantStale(t, dataDir, "cccccccc") // removable
	plantStale(t, dataDir, "dddddddd") // fails: not contention
	plantStale(t, dataDir, "eeeeeeee") // busy: skipped without an error
	lockOutDir(t, dataDir, "dddddddd")
	holdLockAsBusy(t, dataDir, "eeeeeeee")

	removed, err := GCOrphans(dataDir, "", 24, func() time.Time { return failuresBase })

	require.Len(t, removed, 1)
	assert.Equal(t, "cccccccc", removed[0].SessionID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dddddddd")
	assert.NotContains(t, err.Error(), "eeeeeeee", "a busy session is not a failure")
	_, statErr := os.Stat(filepath.Join(dataDir, "sessions", "eeeeeeee", "manifest.json"))
	assert.NoError(t, statErr, "the busy session must be left whole")
}

func TestResult_NewFieldsAreAbsentFromAQuietRun(t *testing.T) {
	t.Parallel()
	res := &Result{SessionsRemoved: []string{}, ArchivesPurged: []string{}}

	out, err := json.Marshal(res)
	require.NoError(t, err)

	assert.NotContains(t, string(out), "skippedBusy", "additive and omitempty: a script that parses today's JSON must not see a new key")
	assert.NotContains(t, string(out), "sessionsFailed")
}
