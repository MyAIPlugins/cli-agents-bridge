//go:build windows

package findings

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// The Windows half of the registry lock. It was written on a Mac and
// cross-compiled; it has run on Windows since the windows-latest leg of the CI on
// 2026-11-09 (run 42539467090, commit 17e090f): `go test -race -count=1 ./...`
// was green for this package, with the contention test
// (TestTryLockFile_FirstHolderGetsItSecondIsBusyAndReleaseFreesIt) and the one in
// which the holder dies (TestTryLockFile_SecondHolderIsBusyAndDeathFreesIt) among
// them. The error texts name the Win32 call, so that a failure says which one.
//
// ERROR_LOCK_VIOLATION is the answer to "someone else holds it" under
// LOCKFILE_FAIL_IMMEDIATELY, as the documentation says. The contention test is
// the evidence that the call returns it: any other code is returned as an error,
// not as contention, so the test could not pass otherwise. If a Windows version
// ever returns another code, the fix is HERE and in the plan, by naming that code:
// not by treating every error as contention.
//
// The lock is MANDATORY on Windows: a process that tries to read or write the
// locked byte gets an error. So the lock file never holds anything and nothing
// ever reads it, and nobody should put a PID in it the way session.IdentityLock
// does on Unix, where the lock is advisory and the content is free.

// lockRange is one byte at offset zero: what is locked is a token, not data.
const lockRange = 1

func lockFile(f *os.File) error {
	var ov windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, lockRange, 0, &ov)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return ErrBusy
	default:
		return fmt.Errorf("findings: LockFileEx: %w", err)
	}
}

// unlockFile unlocks the byte and closes the handle. Both are attempted and both
// errors are reported: a failed unlock still has to close, and a lock that
// closed by itself is not a reason to hide that the unlock failed.
func unlockFile(f *os.File) error {
	var ov windows.Overlapped
	var unlockErr, closeErr error
	if err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockRange, 0, &ov); err != nil {
		unlockErr = fmt.Errorf("findings: UnlockFileEx: %w", err)
	}
	if err := f.Close(); err != nil {
		closeErr = fmt.Errorf("findings: closing the lock file: %w", err)
	}
	return errors.Join(unlockErr, closeErr)
}
