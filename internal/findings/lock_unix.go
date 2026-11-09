//go:build !windows

package findings

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// maxFlockAttempts bounds the retry on EINTR: a signal landing during the
// syscall is not a reason to give up, nor to loop forever (same bound as
// session/identitylock_unix.go).
const maxFlockAttempts = 5

// lockFile takes flock(LOCK_EX|LOCK_NB) through the standard library's syscall
// package: golang.org/x/sys/unix would be the unix binary's first dependency
// outside the standard library, which tests/integration/unix_deps_test.go forbids.
func lockFile(f *os.File) error {
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
		return ErrBusy
	default:
		return fmt.Errorf("findings: flock: %w", err)
	}
}

// unlockFile releases the lock by closing the descriptor that holds it.
func unlockFile(f *os.File) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("findings: releasing the lock: %w", err)
	}
	return nil
}
