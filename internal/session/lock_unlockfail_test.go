package session

// F-160, CRI diff-gate round 3 (R3-1, R3-2). An attempt that is not going to succeed
// gives the lock back (unlockWith). If that release FAILS, the failure is final and
// the caller has to see it: acquire retries on errors.Is(err, errLockFileReplaced), so
// an error that still matched the sentinel would have been retried, a later round would
// have succeeded, and the failed release would have been thrown away with it.
// The release is made to fail through the seam acquireOpts.unlock, which does the real
// release first: the descriptor is closed and the lock is free, only the answer is an
// error, so no test leaves a lock behind.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/fslock"
)

var errInjectedUnlock = errors.New("injected: the release failed")

// failingUnlock releases the lock for real and then answers with errInjectedUnlock.
// calls counts how many times the release was asked for.
func failingUnlock(calls *int) func(f *os.File) error {
	return func(f *os.File) error {
		*calls++
		if err := fslock.Unlock(f); err != nil {
			return err
		}
		return errInjectedUnlock
	}
}

// The case CRI probed on 88f2edd: the file is found replaced, the release fails, and
// the second round would have taken the lock. Red there: opened=2, a release func and
// err == nil.
func TestAcquire_AReleaseThatFailsAfterAReplacementIsFinalAndSeen(t *testing.T) {
	t.Parallel()
	lockPath := filepath.Join(t.TempDir(), "session.lock")
	opened, unlocked, validated := 0, 0, 0

	release, err := acquire(lockPath, acquireOpts{
		afterOpen: func() {
			opened++
			if opened == 1 {
				replaceTheFile(t, lockPath)
			}
		},
		validate: func() error { validated++; return nil },
		unlock:   failingUnlock(&unlocked),
	})

	require.Error(t, err, "the failed release is what comes back, not the success of a later round")
	assert.Nil(t, release)
	assert.ErrorIs(t, err, errInjectedUnlock)
	assert.NotErrorIs(t, err, errLockFileReplaced, "it must not look like a replacement: acquire would retry and lose it")
	assert.Contains(t, err.Error(), errLockFileReplaced.Error(), "the cause that led to the release stays in the text, for whoever reads the log")
	assert.Equal(t, 1, opened, "no second round after a release that failed")
	assert.Equal(t, 1, unlocked)
	assert.Zero(t, validated)
	assert.False(t, heldInThisProcess(lockPath), "and nothing is registered")
	assertLockIsFree(t, lockPath)
}

// The other caller of unlockWith: validate refuses the session. cleanup and the gc
// read ErrSessionGone as "somebody retired it already" and carry on, so a release
// failure that still matched it would be swallowed there.
func TestAcquire_AReleaseThatFailsAfterAValidateRefusalIsNotTakenForTheRefusal(t *testing.T) {
	t.Parallel()
	lockPath := filepath.Join(t.TempDir(), "session.lock")
	unlocked := 0

	release, err := acquire(lockPath, acquireOpts{
		validate: func() error { return ErrSessionGone },
		unlock:   failingUnlock(&unlocked),
	})

	require.Error(t, err)
	assert.Nil(t, release)
	assert.ErrorIs(t, err, errInjectedUnlock)
	assert.NotErrorIs(t, err, ErrSessionGone, "a failed release is not \"somebody retired it already\"")
	assert.Contains(t, err.Error(), ErrSessionGone.Error(), "the refusal that led to the release stays in the text")
	assert.Equal(t, 1, unlocked)
	assert.False(t, heldInThisProcess(lockPath))
	assertLockIsFree(t, lockPath)
}

// The ordinary path keeps its shape: when the release works, the cause comes back
// as it was, not wrapped, so errors.Is and == still see it.
func TestAcquire_AReleaseThatWorksReturnsTheCauseUnchanged(t *testing.T) {
	t.Parallel()
	lockPath := filepath.Join(t.TempDir(), "session.lock")
	unlocked := 0
	counting := func(f *os.File) error { unlocked++; return fslock.Unlock(f) }

	_, err := acquire(lockPath, acquireOpts{
		validate: func() error { return ErrSessionGone },
		unlock:   counting,
	})

	assert.Same(t, ErrSessionGone, err, "the very error validate returned")
	assert.Equal(t, 1, unlocked, "the seam is what gave the lock back")
	assertLockIsFree(t, lockPath)
}

// R3-2. maxReplaced is a limit on the work of one acquire, not on a run of
// consecutive replacements: a wait for a busy lock in between does not give the
// count back. Two replacements, then a holder (busy), then a third replacement on the
// next poll: the bound is reached although the replacements were not in a row.
func TestAcquire_TheReplacementBoundIsNotGivenBackByABusyWait(t *testing.T) {
	t.Parallel()
	lockPath := filepath.Join(t.TempDir(), "session.lock")
	opened := 0
	var holder func() error

	release, err := acquire(lockPath, acquireOpts{
		wait: time.Second,
		afterOpen: func() {
			opened++
			switch opened {
			case 1, 2:
				replaceTheFile(t, lockPath)
			case 3:
				var terr error
				holder, terr = fslock.TryLock(lockPath)
				require.NoError(t, terr, "a holder on the file just opened, so this round is busy")
			case 4:
				require.NoError(t, holder(), "the holder lets go")
				// A lock just released in this process is free only when it BECOMES free
				// (a fork in flight keeps a copy of the descriptor until its exec): if
				// this round found it busy, the next one would not be replaced and would
				// succeed, and the test would end without the error it is about.
				require.NoError(t, settleLock(lockPath), "the file is free before the name moves")
				replaceTheFile(t, lockPath)
			}
		},
	})

	require.Error(t, err)
	assert.Nil(t, release)
	assert.ErrorIs(t, err, errLockFileReplaced)
	assert.NotErrorIs(t, err, ErrLockHeld)
	assert.Equal(t, maxReplaced+1, opened, "three rounds that found a replacement, one that found the lock busy")
	assert.False(t, heldInThisProcess(lockPath))
}

// What the final message says: how many replacements, that it was one acquisition (and
// not "in a row", which is not what is counted), and the session lock named once.
func TestAcquire_TheBoundMessageSaysWhatIsCounted(t *testing.T) {
	t.Parallel()
	lockPath := filepath.Join(t.TempDir(), "session.lock")

	_, err := acquire(lockPath, acquireOpts{afterOpen: func() { replaceTheFile(t, lockPath) }})

	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, fmt.Sprintf("replaced %d times during one acquisition", maxReplaced))
	assert.NotContains(t, msg, "in a row")
	assert.Equal(t, 1, strings.Count(msg, "session lock"), "named once, not once by the prefix and once by the sentinel: %q", msg)
}
