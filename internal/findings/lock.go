package findings

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
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
// WHY A KERNEL LOCK AND NOT A PID FILE. The lock is gone the instant its holder
// dies, for any reason, so there is no stale lock to judge and no "remove and
// recreate" step that two processes could both perform (the race a PID file
// has between O_EXCL and the PID write). The file is never removed and never
// holds content: it is only something to lock.
//
// The lock belongs to the descriptor the release function holds, so that
// function must stay reachable until the work is done: the caller defers it.
// If the file were closed early, by a finalizer for instance, the lock would drop.
func TryLockFile(path string) (release func() error, err error) {
	f, err := openLockFile(path)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		return nil, errors.Join(err, closeAfterFailure(f, path))
	}
	var once sync.Once
	var releaseErr error
	return func() error {
		once.Do(func() { releaseErr = unlockFile(f) })
		return releaseErr
	}, nil
}

func closeAfterFailure(f *os.File, path string) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("findings: closing lock file %s: %w", path, err)
	}
	return nil
}

// openLockFile opens the lock file through security.OpenOwned (no link
// followed, a regular file, owned by us, checked on the descriptor that will be
// locked). If it does not exist it is created with O_EXCL and 0600 and reopened
// the same way. Losing the creation race is not an error: someone else made it.
// An existing file needs no permission to create files in its directory.
func openLockFile(path string) (*os.File, error) {
	f, err := security.OpenOwned(path)
	if err == nil {
		return f, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("findings: lock file %s: %w", path, err)
	}

	created, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	switch {
	case err == nil:
		if err := created.Close(); err != nil {
			return nil, fmt.Errorf("findings: closing new lock file %s: %w", path, err)
		}
	case errors.Is(err, fs.ErrExist):
		// Another process created it between our open and our create.
	default:
		return nil, fmt.Errorf("findings: creating lock file %s: %w", path, err)
	}

	f, err = security.OpenOwned(path)
	if err != nil {
		return nil, fmt.Errorf("findings: lock file %s: %w", path, err)
	}
	return f, nil
}
