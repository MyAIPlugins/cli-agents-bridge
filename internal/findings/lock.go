package findings

import (
	"errors"
	"fmt"

	"github.com/myAIPlugins/cli-agents-bridge/internal/fslock"
)

// ErrBusy means another process holds the registry lock and the retry budget
// ran out. Nothing was written: the caller can simply run the command again.
var ErrBusy = errors.New("findings: the registry is locked by another process, try again")

// TryLockFunc takes the registry lock without waiting. It is a type so tests can
// inject a lock that is busy, slow or fatal.
type TryLockFunc func(path string) (release func() error, err error)

// TryLockFile takes an exclusive kernel lock on path and returns the function
// that releases it, or ErrBusy if another open file holds it. It never waits.
//
// The lock itself is internal/fslock, the only copy in the module (why a kernel
// lock and not a PID file is written there). This is the registry's envelope: it
// keeps findings' own ErrBusy, which the retry loop in store.go and the callers
// compare against, and names the package in every other failure.
func TryLockFile(path string) (release func() error, err error) {
	release, err = fslock.TryLock(path)
	switch {
	case err == nil:
		return release, nil
	case err == fslock.ErrBusy:
		// The bare sentinel: the ordinary contention, with the message callers know.
		// Compared by identity and not with errors.Is on purpose: fslock returns ErrBusy
		// bare, and an error that merely wraps it (a close that failed too) is a failure
		// that this retry must not repeat.
		return nil, ErrBusy
	default:
		return nil, fmt.Errorf("findings: %w", err)
	}
}
