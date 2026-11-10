// Package fslock is the only copy of the kernel file lock in this module: flock on
// Unix, LockFileEx on Windows. The registry lock of findings, the identity lock of
// the MCP port and the session lock of F-160 all take it from here, and a test
// (tests/integration/lock_single_copy_test.go) fails if a second copy appears.
//
// WHY A KERNEL LOCK AND NOT A PID FILE. The lock is gone the instant its holder
// dies, for any reason, so there is no stale lock to judge and no "remove and
// recreate" step that two processes could both perform (the race a PID file has
// between O_EXCL and the PID write). The file holds nothing and nobody reads it:
// it is only something to lock.
//
// "Gone" means the last descriptor of the open file is closed, and on Unix that is
// not always the holder's own. A fork in flight in the same process (another
// goroutine starting a child) carries a copy of every descriptor until its exec,
// so for that long, milliseconds, a release or a death does not free the lock yet.
// The product never needs it freed at that very instant: an acquirer that waits
// polls, and one that does not wait skips and tries again. A test that asks
// "is it free right after the release?" must ask "does it become free?" instead
// (internal/fslock/locktest).
//
// The lock belongs to the OPEN FILE DESCRIPTION, not to the process: a second
// open of the same file in the same process contends too. That is what stops one
// process from taking the same lock twice by accident, and it is why a caller that
// wants re-entrancy keeps its own registry on top (internal/session does).
package fslock

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
)

// ErrBusy means another open file description holds the lock. It is the ordinary
// "someone else has it", as opposed to a failure of the system call: only the
// errno (Unix) or the Win32 code (Windows) that means contention produces it, and
// every other error comes back wrapped, with the path, as a failure.
var ErrBusy = errors.New("fslock: the lock is held by another open file")

// openAttempts bounds the open-or-create loop of Open. One lap is "open; if the
// file is missing, create it"; a second lap is needed when the file is created and
// removed again before it could be reopened. A third means something is removing
// it as fast as it is created, and that is an error, not a reason to keep going.
const openAttempts = 3

// Open opens the lock file at path and returns the descriptor to lock. It goes
// through security.OpenOwned, so no link is followed, the file must be regular and
// owned by us, and the checks run on the descriptor that will be locked. If the
// file does not exist it is created with O_EXCL and 0600 and opened the same way.
// Losing the creation race is not an error: someone else made it.
//
// An existing file needs no permission to create files in its directory. A missing
// parent directory is an error: Open never creates directories.
func Open(path string) (*os.File, error) {
	for attempt := 0; attempt < openAttempts; attempt++ {
		f, err := security.OpenOwned(path)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("fslock: open %s: %w", path, err)
		}
		if err := create(path); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("fslock: open %s: the file was missing again after being created, %d times in a row", path, openAttempts)
}

func create(path string) error {
	created, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	switch {
	case err == nil:
		if err := created.Close(); err != nil {
			return fmt.Errorf("fslock: closing new lock file %s: %w", path, err)
		}
		return nil
	case errors.Is(err, fs.ErrExist):
		return nil // another process created it between our open and our create
	default:
		return fmt.Errorf("fslock: creating lock file %s: %w", path, err)
	}
}

// Lock takes an exclusive lock on f without waiting. It returns nil, ErrBusy when
// another open file holds it, or an error naming the file for any other failure.
func Lock(f *os.File) error { return lockFile(f) }

// Unlock releases the lock and closes f. After it f is unusable. On Unix the lock
// goes with the descriptor; on Windows the byte is unlocked first, and both steps
// are attempted and both errors reported.
func Unlock(f *os.File) error { return unlockFile(f) }

// TryLock opens path (see Open), locks it without waiting, and returns the
// function that releases it, or ErrBusy if another open file holds it. It never
// waits. The lock belongs to the descriptor the release function holds, so that
// function must stay reachable until the work is done: the caller defers it. If
// the file were closed early, by a finalizer for instance, the lock would drop.
//
// The release is idempotent. When the lock is busy and closing the descriptor also
// fails, the error carries both; when only the lock is busy it is ErrBusy itself.
func TryLock(path string) (release func() error, err error) {
	f, err := Open(path)
	if err != nil {
		return nil, err
	}
	if err := Lock(f); err != nil {
		if cerr := f.Close(); cerr != nil {
			return nil, errors.Join(err, fmt.Errorf("fslock: closing lock file %s: %w", path, cerr))
		}
		return nil, err
	}
	var once sync.Once
	var releaseErr error
	return func() error {
		once.Do(func() { releaseErr = Unlock(f) })
		return releaseErr
	}, nil
}
