//go:build windows

package security

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// F-170, the half that needs the platform the defect lives on. The fixture is the
// one of F-154 (deleteInFlight, perms_windows_denied_test.go): a deleting handle
// that has set the disposition and stays open, which is the state the first way in
// (another delete of the same name in flight) leaves a name in, and which probe 3
// measured answering os.Remove with ACCESS_DENIED (lab row R1) and with "not found"
// once that handle closes. It is a real ACCESS_DENIED from the real API, released at
// an exact attempt through the injected wait, so no test here races a clock.
//
// Every test asserts its premise, and none skips: if os.Remove is not refused in the
// state the fixture builds, the test fails saying so.

// TestTransientRemoveDenial_IsAccessDeniedAndNothingElse is the Windows half of the
// predicate: the refusal of a pending delete is retried, and a sharing violation
// (somebody holds the file open without share-delete), a missing file and a
// directory that is not empty are not, because they will not change in 155 ms.
func TestTransientRemoveDenial_IsAccessDeniedAndNothingElse(t *testing.T) {
	wrap := func(e windows.Errno) error { return &os.PathError{Op: "remove", Path: "grant", Err: e} }

	assert.True(t, transientRemoveDenial(wrap(windows.ERROR_ACCESS_DENIED)))
	assert.False(t, transientRemoveDenial(wrap(windows.ERROR_SHARING_VIOLATION)))
	assert.False(t, transientRemoveDenial(wrap(windows.ERROR_FILE_NOT_FOUND)))
	assert.False(t, transientRemoveDenial(wrap(windows.ERROR_DIR_NOT_EMPTY)))
}

// TestRemoveFile_ADeleteInFlightRidesOutToTheFileBeingGone: the name has another
// delete in flight; the bare os.Remove is refused (the premise, asserted), and the
// same call through the loop waits twice, sees the delete complete and reports
// success. A single attempt, or the bare os.Remove, returns ACCESS_DENIED on the
// first answer and fails here.
func TestRemoveFile_ADeleteInFlightRidesOutToTheFileBeingGone(t *testing.T) {
	path, release := deleteInFlight(t, t.TempDir(), "desktop-grant.json")

	premise := os.Remove(path)
	require.Error(t, premise, "premise not reproduced: os.Remove succeeded on a name with a delete in flight")
	require.ErrorIs(t, premise, windows.ERROR_ACCESS_DENIED,
		"premise not reproduced: os.Remove answered something other than ACCESS_DENIED on a name with a delete in flight")

	var s sleeps
	err := removeFileSleeping(path, os.Remove, func(d time.Duration) {
		s.sleep(d)
		if len(s.waits) == 2 {
			release()
		}
	}, transientRemoveDenial)

	require.NoError(t, err)
	_, statErr := os.Lstat(path)
	assert.True(t, os.IsNotExist(statErr), "the name must be free once the other delete completed: %v", statErr)
	assert.Equal(t, []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}, s.waits)
}

// TestRemoveFile_ADeleteHeldForeverReportsAccessDeniedAfterTheBudget is the bound,
// with the real error: a delete that never completes is waited on five times and
// then reported as ACCESS_DENIED with the attempts in the message. RemoveFile does
// not pretend the file was removed.
func TestRemoveFile_ADeleteHeldForeverReportsAccessDeniedAfterTheBudget(t *testing.T) {
	path, _ := deleteInFlight(t, t.TempDir(), "desktop-grant.json")

	var s sleeps
	err := removeFileSleeping(path, os.Remove, s.sleep, transientRemoveDenial)

	require.Error(t, err)
	assert.ErrorIs(t, err, windows.ERROR_ACCESS_DENIED, "the real cause, not a generic one")
	assert.Contains(t, err.Error(), "6 attempts")
	assert.Equal(t, []time.Duration{
		5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond,
	}, s.waits)
}

// TestRemoveFile_AHeldDeleteReportsAccessDeniedThroughTheProductionWiring calls RemoveFile
// itself, not the seam: the tests above pass os.Remove, a fake sleep and the predicate by
// hand, so the cablaggio of production (os.Remove, time.Sleep, transientRemoveDenial) had
// no oracle on the platform of the defect, and a predicate that said "never" there would
// have left them green (CRI2, M1). The delete is held forever, so the call goes through
// the whole budget with the REAL clock; what is asserted is deterministic: the error is
// the real ACCESS_DENIED, the message says six attempts, and the call took at least the
// requested backoff (time.Sleep never returns early). A predicate that did not retry
// would return at once, with no "attempts" and no wait.
func TestRemoveFile_AHeldDeleteReportsAccessDeniedThroughTheProductionWiring(t *testing.T) {
	path, _ := deleteInFlight(t, t.TempDir(), "desktop-grant.json")

	start := time.Now()
	err := RemoveFile(path)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, windows.ERROR_ACCESS_DENIED, "the real cause, not a generic one")
	assert.Contains(t, err.Error(), "6 attempts")
	assert.GreaterOrEqual(t, elapsed, backoffTotal(deniedAttempts, deniedFirstDelay), "the waits were real")
}
