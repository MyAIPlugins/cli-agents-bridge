//go:build windows

package findings

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// The Windows half of the registry lock. NOTHING HERE WAS EXECUTED ON WINDOWS
// when it was written: it was cross-compiled from a Mac. The proof is the
// windows-latest leg of the CI; until it is green this file is a claim, and the
// error texts name the Win32 call so that a failure says which one.
//
// ERROR_LOCK_VIOLATION as the answer to "someone else holds it" under
// LOCKFILE_FAIL_IMMEDIATELY is the code the documentation gives and has not been
// seen returned. If the CI shows another code, the fix is HERE and in the plan,
// by naming that code: not by treating every error as contention.
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
