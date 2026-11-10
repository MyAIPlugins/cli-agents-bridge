//go:build windows

package fslock

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// The Windows half of the kernel lock. It was written on a Mac and cross-compiled;
// it has run on Windows since the windows-latest leg of the CI on 2026-11-09 (run
// 42539467090, commit 17e090f), when `go test -race -count=1 ./...` was green for
// the findings package with its contention test
// (TestTryLockFile_FirstHolderGetsItSecondIsBusyAndReleaseFreesIt) and the one in
// which the holder dies (TestTryLockFile_SecondHolderIsBusyAndDeathFreesIt) among
// them. The same two properties are pinned here, in this package, by
// TestTryLock_SecondHolderIsBusyAndDeathFreesIt. The error texts name the Win32
// call, so that a failure says which one.
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
	return classify(f.Name(), err)
}

// classify says what a LockFileEx error means. Only ERROR_LOCK_VIOLATION is
// contention; every other code is a failure of the call.
func classify(name string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return ErrBusy
	default:
		return fmt.Errorf("fslock: LockFileEx %s: %w", name, err)
	}
}

// unlockFile unlocks the byte and closes the handle. Both are attempted and both
// errors are reported: a failed unlock still has to close, and a lock that closed
// by itself is not a reason to hide that the unlock failed.
func unlockFile(f *os.File) error {
	var ov windows.Overlapped
	var unlockErr, closeErr error
	if err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockRange, 0, &ov); err != nil {
		unlockErr = fmt.Errorf("fslock: UnlockFileEx %s: %w", f.Name(), err)
	}
	if err := f.Close(); err != nil {
		closeErr = fmt.Errorf("fslock: closing the lock file %s: %w", f.Name(), err)
	}
	return errors.Join(unlockErr, closeErr)
}
