//go:build !windows

package fslock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// maxFlockAttempts bounds the retry on EINTR: a signal landing during the syscall
// is not a reason to give up, and not a reason to loop forever either.
const maxFlockAttempts = 5

// lockFile takes flock(LOCK_EX|LOCK_NB) through the standard library's syscall
// package, on purpose: golang.org/x/sys/unix would be the unix binary's first
// dependency outside the standard library (before it, x/sys was imported by
// Windows files only), and tests/integration/unix_deps_test.go forbids that.
func lockFile(f *os.File) error {
	var err error
	for attempt := 0; attempt < maxFlockAttempts; attempt++ {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	return classify(f.Name(), err)
}

// classify says what a flock error means. Only EWOULDBLOCK is contention: it is
// the answer to "someone else holds it" under LOCK_NB. EINVAL, ENOTSUP, ENOLCK,
// EBADF, ENOSYS and the rest are failures of the call (a filesystem without
// locks, a bad descriptor), and reading them as "busy" would turn a broken
// environment into an eternal wait for a holder that does not exist.
func classify(name string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return ErrBusy
	default:
		return fmt.Errorf("fslock: lock %s: %w", name, err)
	}
}

// unlockFile releases the lock by closing the descriptor that holds it.
func unlockFile(f *os.File) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("fslock: releasing the lock on %s: %w", f.Name(), err)
	}
	return nil
}
