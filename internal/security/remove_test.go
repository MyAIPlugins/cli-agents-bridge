package security

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-170, the half that runs everywhere. removeFileSleeping is generic: the delete,
// the wait and the verdict on an error are all passed in, so what the loop does
// with each answer is proved here with a scripted delete and a fake sleep. No test
// waits for real time and none needs Windows. That the REAL answer of a pending
// delete is the one retried is remove_windows_test.go.

// errRefused stands for ACCESS_DENIED in these tests; isTransient (the F-154 tests)
// is the predicate that retries it.
var errRefused = &fs.PathError{Op: "remove", Path: "grant", Err: errTransient}

// scriptedRemove answers each call with the next result and fails the test if the
// loop asks for more than the script holds: a loop that runs past its budget must
// not be rescued by a script that happens to end in nil.
type scriptedRemove struct {
	t       *testing.T
	results []error
	calls   int
}

func (r *scriptedRemove) remove(string) error {
	r.t.Helper()
	if r.calls >= len(r.results) {
		r.t.Fatalf("remove called %d times, the script holds %d answers", r.calls+1, len(r.results))
	}
	err := r.results[r.calls]
	r.calls++
	return err
}

func TestRemoveFile_RemovesAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop-grant.json")
	require.NoError(t, os.WriteFile(path, []byte("GRANT"), 0o600))

	require.NoError(t, RemoveFile(path))

	_, err := os.Lstat(path)
	assert.True(t, os.IsNotExist(err), "the file must be gone: %v", err)
}

// TestRemoveFile_AFileAlreadyGoneIsRemoved: a delete of a name that is not there is
// the end state that was asked for, with the production function and no fake.
func TestRemoveFile_AFileAlreadyGoneIsRemoved(t *testing.T) {
	require.NoError(t, RemoveFile(filepath.Join(t.TempDir(), "never-existed.json")))
}

// TestRemoveFileSleeping_ANameAlreadyGoneIsSuccessWithoutWaiting is the same
// verdict through the seam, and adds what the real call cannot show: no wait.
func TestRemoveFileSleeping_ANameAlreadyGoneIsSuccessWithoutWaiting(t *testing.T) {
	var s sleeps
	r := &scriptedRemove{t: t, results: []error{&fs.PathError{Op: "remove", Path: "grant", Err: fs.ErrNotExist}}}

	require.NoError(t, removeFileSleeping("grant", r.remove, s.sleep, isTransient))
	assert.Equal(t, 1, r.calls)
	assert.Empty(t, s.waits)
}

// TestRemoveFileSleeping_RidesOutATransientRefusal: the defect and its cure. Two
// refusals and then the delete goes through; the waits are the first two of the
// F-154 backoff. A loop that tried once would return the first refusal and fail here.
func TestRemoveFileSleeping_RidesOutATransientRefusal(t *testing.T) {
	var s sleeps
	r := &scriptedRemove{t: t, results: []error{errRefused, errRefused, nil}}

	require.NoError(t, removeFileSleeping("grant", r.remove, s.sleep, isTransient))
	assert.Equal(t, 3, r.calls)
	assert.Equal(t, []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}, s.waits)
}

// TestRemoveFileSleeping_ARefusalThatTurnsIntoAnAbsenceIsSuccess: the pending delete
// of another party completes and the name is free. That is the answer the caller
// wanted (it is the H-a way in), reached through two refusals.
func TestRemoveFileSleeping_ARefusalThatTurnsIntoAnAbsenceIsSuccess(t *testing.T) {
	var s sleeps
	notExist := &fs.PathError{Op: "remove", Path: "grant", Err: fs.ErrNotExist}
	r := &scriptedRemove{t: t, results: []error{errRefused, errRefused, notExist}}

	require.NoError(t, removeFileSleeping("grant", r.remove, s.sleep, isTransient))
	assert.Equal(t, 3, r.calls)
	assert.Len(t, s.waits, 2)
}

// TestRemoveFileSleeping_AnEndlessRefusalReportsTheRealError is the bound: the budget
// is the one of F-154 (six attempts, the waits it requests), and what comes back is
// the refusal itself, reachable with errors.Is, with the attempts in the message.
// Nothing here pretends the file was removed.
func TestRemoveFileSleeping_AnEndlessRefusalReportsTheRealError(t *testing.T) {
	var s sleeps
	results := make([]error, deniedAttempts)
	for i := range results {
		results[i] = errRefused
	}
	r := &scriptedRemove{t: t, results: results}

	err := removeFileSleeping("grant", r.remove, s.sleep, isTransient)
	require.Error(t, err)
	assert.ErrorIs(t, err, errTransient, "the real cause stays reachable")
	assert.Contains(t, err.Error(), "6 attempts")
	assert.Equal(t, deniedAttempts, r.calls)

	var total time.Duration
	for _, w := range s.waits {
		total += w
	}
	assert.Len(t, s.waits, deniedAttempts-1, "one wait between two attempts, none after the last")
	assert.Equal(t, backoffTotal(deniedAttempts, deniedFirstDelay), total)
}

// TestRemoveFileSleeping_ANonTransientErrorReturnsAtOnce: an error the predicate does
// not accept is not waited on, and it is not rewritten.
func TestRemoveFileSleeping_ANonTransientErrorReturnsAtOnce(t *testing.T) {
	var s sleeps
	permanent := &fs.PathError{Op: "remove", Path: "grant", Err: errPermanent}
	r := &scriptedRemove{t: t, results: []error{permanent}}

	err := removeFileSleeping("grant", r.remove, s.sleep, isTransient)
	assert.Same(t, permanent, err)
	assert.Equal(t, 1, r.calls)
	assert.Empty(t, s.waits)
}

// TestRemoveFileSleeping_TheRetryRemovesTheNewerFile states the contract the doc of
// RemoveFile declares, and it does not prove a race: it documents what the retry
// DOES when the refusal came from the second way in (a replace landed between the
// delete's open and its disposition, so the new file stands under the name). The
// first answer is the refusal, with the new file already there; the second call is
// the real os.Remove, and it removes by name. The new file is gone, and the call
// reports success.
//
// This is the test a future caller has to answer to: if losing the newer file is not
// acceptable for the name it removes, RemoveFile is the wrong function. The reply
// journal was refused for exactly this reason (see the doc of RemoveFile).
func TestRemoveFileSleeping_TheRetryRemovesTheNewerFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop-grant.json")
	calls := 0
	replaceThenRefuse := func(p string) error {
		calls++
		if calls == 1 {
			require.NoError(t, os.WriteFile(p, []byte("NEWER"), 0o600))
			return errRefused
		}
		return os.Remove(p)
	}

	var s sleeps
	require.NoError(t, removeFileSleeping(path, replaceThenRefuse, s.sleep, isTransient))

	assert.Equal(t, 2, calls)
	_, err := os.Lstat(path)
	assert.True(t, errors.Is(err, fs.ErrNotExist), "the newer file was removed by name: %v", err)
}
