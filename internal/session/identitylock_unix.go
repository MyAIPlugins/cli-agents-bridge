//go:build !windows

package session

import (
	"errors"
	"os"
	"syscall"
)

const identityLocksSupported = true

// errLockContended means another open file description holds the lock. It is the
// ordinary "someone else has it", as opposed to a failure of the syscall.
var errLockContended = errors.New("lock contended")

// lockExclusiveNonBlocking takes flock(LOCK_EX|LOCK_NB) through the standard
// library's syscall package, on purpose: this is the first thing the unix binary
// needs from the kernel that the standard library already offers, and importing
// golang.org/x/sys/unix for it would have been the binary's FIRST dependency
// outside the standard library (before it, x/sys was imported by Windows files
// only). The Makefile integration test that builds the binary in a throw-away HOME
// caught exactly that: a download into a read-only module cache that a temp dir
// cannot remove. The lock belongs to the
// open file description, so a second open of the same file in the same process
// contends too, which is what stops one process from joining twice.
func lockExclusiveNonBlocking(f *os.File) error {
	var err error
	for attempt := 0; attempt < maxFlockAttempts; attempt++ {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return errLockContended
	default:
		return err
	}
}
