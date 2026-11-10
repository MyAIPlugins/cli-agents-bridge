package session

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/fslock"
)

// The session lock is a kernel lock (flock / LockFileEx, see internal/fslock) on an
// EMPTY file, <session>/lock. Nothing is ever written to it and nobody reads it.
//
// It used to be a PID file created with O_EXCL and recovered with kill(pid, 0). A
// PID file needs a judgement about the holder ("is that process still alive?"),
// and the recovery of a dead holder is "remove the file and create it again",
// a step two processes can both perform. F-160 reproduced three ways in which two
// processes held the lock together. A kernel lock has no such step: it is gone when
// its holder dies (a fork in flight in the same process keeps it for the
// milliseconds until its exec, see internal/fslock), and a contender never removes
// or replaces the file.

// DefaultLockWait is how long a waiting caller keeps trying before giving up. It is
// a default: config session_lock_wait_ms (env CAB_SESSION_LOCK_WAIT_MS) sets it, and
// a value <= 0 there means "do not wait". Manager.LockWait carries the value.
const DefaultLockWait = 5 * time.Second

// lockPoll is the pause between two attempts while the lock is busy. The number of
// attempts is bounded by wait/lockPoll+1, so every wait has a numeric limit.
const lockPoll = 10 * time.Millisecond

// ErrLockHeld means another open file holds the lock for the whole wait.
//
// IT CARRIES NO REMEDY (F-126). The message used to end with "use --force-new to
// override", which is true at one call site out of eleven and was printed at all of
// them, five of which belong to loop verbs that have no flags. A primitive that does
// not know its caller states facts: the path and how long it waited.
var ErrLockHeld = errors.New("session lock held by live process")

// ErrSessionGone means the session was retired: its manifest is gone, so the lock
// that was about to be taken protects nothing. It is also how a caller that waited
// on an inode that cleanup has since retired learns to stop (see acquire).
var ErrSessionGone = errors.New("session no longer exists")

// errLockFileReplaced is internal to acquire and never leaves it: the file that was
// just locked is not the one the name points to any more. The name was unlinked, or
// unlinked and created again, between the open and the lock (cleanup retires a
// session that way, and the same id can exist again later: a re-import, for
// instance). A kernel lock on such a file excludes nobody who opens the name now.
var errLockFileReplaced = errors.New("session lock: the locked file is not the one the name points to")

// maxReplaced bounds how many times acquire takes the lock again after finding the
// file replaced. One replacement is a retirement or a re-creation that happened to
// overlap; three in a row mean the name is being replaced as fast as it is opened,
// and that is an error to report, not a reason to keep going.
const maxReplaced = 3

// LockFileName is the name of the lock file inside a session directory. cleanup
// needs it to leave the file alone while it holds it and to remove it afterwards.
const LockFileName = "lock"

// SessionLockBase is the path of the lock file of a session. The one constructor:
// the registry below is keyed by this path, so two spellings would be two locks.
func SessionLockBase(dataDir, sessionID string) string {
	return filepath.Join(dataDir, "sessions", sessionID, LockFileName)
}

func (m *Manager) lockBase(sessionID string) string {
	return SessionLockBase(m.DataDir, sessionID)
}

// acquireOpts is what varies between the callers of acquire.
type acquireOpts struct {
	// wait is how long to keep trying while the lock is busy; <= 0 is one attempt.
	wait time.Duration
	// precheck runs before the lock file is opened, so a retired session gets
	// ErrSessionGone and nothing is created in its directory.
	precheck func() error
	// validate runs on the lock already taken, before anything else touches the
	// session. It exists because precheck cannot be enough: cleanup removes the
	// manifest under the lock and releases it before it unlinks the lock file, so a
	// waiter can take the lock in between, on a file that is still the one the name
	// points to, and find a session that is no longer there. (A waiter that locks a
	// file the name has already left is caught earlier, by checkNamed.)
	validate func() error
	// afterOpen is a test seam, nil in production: it runs between open and lock.
	afterOpen func()
}

// processHold is a lock this process holds, shared by every acquisition of the same
// path until the last of them is released.
type processHold struct {
	f    *os.File
	refs int
}

// processHolds makes the lock re-entrant inside one process: next.go takes the
// session lock and calls CommitWakeCursor, which takes it again, and the second
// acquisition must not block on the first. It is a package variable and not a
// Manager field because the lock is a fact about a file and a process: the cleanup
// builds its own Manager, tests build many, and two Managers over one directory
// that did not share the registry would take two kernel locks and exclude each
// other, which is the opposite of re-entrancy. Only lock.go touches it.
//
// Re-entrancy is per process, not per goroutine: a second goroutine that asks for a
// lock the first holds gets it. Exclusion is between processes, and that is what
// the kernel gives.
var (
	processHoldsMu sync.Mutex
	processHolds   = map[string]*processHold{}
)

// acquire is the one place a session lock is taken: AcquireLock and
// Manager.AcquireSessionLock both come here, and a test fails if a second path appears.
//
// Every attempt, in this order:
//  1. take the registry mutex; if this process already holds the path, share it.
//  2. precheck, if any.
//  3. open the lock file (created empty and 0600 if missing).
//  4. lock it without waiting. Busy: close, release the mutex, sleep, try again.
//  5. check that the file just locked is the one the name points to now. A lock on
//     a file that has since been unlinked or replaced is a lock on nothing anybody
//     else will ever contend for. If it is not, release it and start again from the
//     name as it is now (at most maxReplaced times, without waiting: it is not
//     contention). Starting again, and not refusing, is what keeps a session that
//     exists again under the same id usable; the precheck of the new round refuses
//     one that does not exist.
//  6. validate, if any, on the lock just taken. This order is the point: validating
//     before the lock leaves a gap in which cleanup retires the session.
//  7. register the hold.
//
// The mutex is held across the I/O (an open and a non-blocking flock, microseconds)
// and never across the sleep.
func acquire(base string, o acquireOpts) (release func() error, err error) {
	key := filepath.Clean(base)
	attempts := 1
	if o.wait > 0 {
		attempts += int(o.wait / lockPoll)
	}
	replaced := 0
	for i := 0; i < attempts; i++ {
		release, err = acquireOnce(key, o)
		for errors.Is(err, errLockFileReplaced) && replaced < maxReplaced-1 {
			replaced++
			release, err = acquireOnce(key, o)
		}
		if errors.Is(err, errLockFileReplaced) {
			return nil, fmt.Errorf("session lock %q: the lock file was replaced %d times in a row while it was being taken: %w", base, maxReplaced, err)
		}
		if !errors.Is(err, fslock.ErrBusy) {
			return release, err
		}
		if i < attempts-1 {
			time.Sleep(lockPoll)
		}
	}
	return nil, fmt.Errorf("%w: lockPath=%q, still held after waiting %s", ErrLockHeld, base, max(o.wait, 0))
}

// acquireOnce is one attempt of acquire. It returns fslock.ErrBusy, unwrapped by
// design, when the lock is held by someone else: that is the only error the caller
// retries on, every other one is final.
func acquireOnce(key string, o acquireOpts) (func() error, error) {
	processHoldsMu.Lock()
	defer processHoldsMu.Unlock()

	if h, ok := processHolds[key]; ok {
		h.refs++
		return releaseHold(key, h), nil
	}
	if o.precheck != nil {
		if err := o.precheck(); err != nil {
			return nil, err
		}
	}
	f, err := fslock.Open(key)
	if err != nil {
		return nil, err
	}
	if o.afterOpen != nil {
		o.afterOpen()
	}
	if err := fslock.Lock(f); err != nil {
		if cerr := f.Close(); cerr != nil {
			// Final even when the lock was only busy: a descriptor that will not
			// close is a failure to report, not a reason to retry.
			return nil, fmt.Errorf("closing lock file %s after %v: %w", key, err, cerr)
		}
		return nil, err
	}
	if err := checkNamed(f, key); err != nil {
		return nil, unlockWith(f, err)
	}
	if o.validate != nil {
		if err := o.validate(); err != nil {
			return nil, unlockWith(f, err)
		}
	}
	h := &processHold{f: f, refs: 1}
	processHolds[key] = h
	return releaseHold(key, h), nil
}

// checkNamed returns errLockFileReplaced unless f, already locked, is the very file
// key names now. Any other failure of the two stats is a failure, never an answer to
// the question: "I could not look" is not "it is the same file".
func checkNamed(f *os.File, key string) error {
	held, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat the locked file %s: %w", key, err)
	}
	named, err := os.Lstat(key)
	switch {
	case err == nil && os.SameFile(held, named):
		return nil
	case err == nil || errors.Is(err, fs.ErrNotExist):
		return errLockFileReplaced
	default:
		return fmt.Errorf("lstat the lock file %s: %w", key, err)
	}
}

// unlockWith gives up a lock taken by an attempt that is not going to succeed, and
// returns err, together with the error of the release if that failed too.
func unlockWith(f *os.File, err error) error {
	if uerr := fslock.Unlock(f); uerr != nil {
		return errors.Join(err, uerr)
	}
	return err
}

// releaseHold returns the release of ONE acquisition: idempotent, so calling it twice
// cannot lower the count of somebody else's. The last release unlocks and closes.
func releaseHold(key string, h *processHold) func() error {
	var once sync.Once
	var releaseErr error
	return func() error {
		once.Do(func() {
			processHoldsMu.Lock()
			defer processHoldsMu.Unlock()
			h.refs--
			if h.refs > 0 {
				return
			}
			delete(processHolds, key)
			releaseErr = fslock.Unlock(h.f)
		})
		return releaseErr
	}
}

// AcquireLock takes the lock at base, waiting up to wait for it (<= 0: one attempt),
// and runs validate, if any, on the lock once held. A session lock is taken through
// Manager.AcquireSessionLock, which validates that the session exists; this
// function is for the one caller that cannot (Register: the manifest does not exist
// yet) and for tests.
//
// The returned release is idempotent. The lock file is not removed on release: only
// cleanup removes it, after retiring the session.
func AcquireLock(base string, wait time.Duration, validate func() error) (release func() error, err error) {
	return acquire(base, acquireOpts{wait: wait, validate: validate})
}

// AcquireSessionLock takes the lock of an existing session. It checks that the
// manifest exists before opening the lock file and again on the lock once held, so a
// session that cleanup retired is refused with ErrSessionGone and nothing is created
// in its directory. Every caller that locks a session comes through here.
func (m *Manager) AcquireSessionLock(sessionID string, wait time.Duration) (release func() error, err error) {
	return m.acquireSessionLock(sessionID, wait, nil)
}

// acquireSessionLock is AcquireSessionLock with the test seam of acquire, so a
// test can stop a waiter between the open and the lock and retire the session.
func (m *Manager) acquireSessionLock(sessionID string, wait time.Duration, afterOpen func()) (release func() error, err error) {
	exists := func() error { return m.requireSession(sessionID) }
	return acquire(m.lockBase(sessionID), acquireOpts{wait: wait, precheck: exists, validate: exists, afterOpen: afterOpen})
}

// requireSession is the existence check of AcquireSessionLock: the manifest must
// be there. Lstat and not a read: whether it parses is for the caller's own load.
func (m *Manager) requireSession(sessionID string) error {
	_, err := os.Lstat(filepath.Join(m.sessionDir(sessionID), "manifest.json"))
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%w: %s", ErrSessionGone, sessionID)
	default:
		return fmt.Errorf("session %s: manifest: %w", sessionID, err)
	}
}

// NotifyWatchDir is the per-session directory of the notify-watch locks and state.
const NotifyWatchDir = "notify-watch"

// AcquireWatcherLock takes the lock of the watcher named watchName of a session. It
// is held for the life of the watcher process and means "this session already has a
// watcher with that name": one attempt, and a second watcher is refused at once.
//
// It runs inside the session lock, so a session that was retired is refused with
// ErrSessionGone before anything is created, and the watcher directory is created
// with a non-recursive Mkdir. The ENOENT of that Mkdir cannot happen while the
// session lock is held (cleanup removes the session directory only after releasing
// it); it is a belt, and it must never recreate the directory of a vanished session.
func (m *Manager) AcquireWatcherLock(sessionID, watchName string) (release func() error, err error) {
	return m.acquireWatcherLock(sessionID, watchName, nil)
}

// acquireWatcherLock is AcquireWatcherLock with a test seam: between, nil in
// production, runs before the session lock is taken and stands for a watcher that
// was stopped after its initial LoadManifest.
func (m *Manager) acquireWatcherLock(sessionID, watchName string, between func()) (release func() error, err error) {
	if between != nil {
		between()
	}
	err = m.WithSessionLock(sessionID, func() error {
		watchDir := filepath.Join(m.sessionDir(sessionID), NotifyWatchDir)
		if merr := os.Mkdir(watchDir, 0o700); merr != nil && !errors.Is(merr, fs.ErrExist) {
			if errors.Is(merr, fs.ErrNotExist) {
				return fmt.Errorf("%w: %s", ErrSessionGone, sessionID)
			}
			return fmt.Errorf("watcher dir %s: %w", watchDir, merr)
		}
		var aerr error
		release, aerr = acquire(filepath.Join(watchDir, watchName+".lock"), acquireOpts{})
		if errors.Is(aerr, ErrLockHeld) {
			return fmt.Errorf("watcher %q of session %s is held by another process: %w", watchName, sessionID, aerr)
		}
		return aerr
	})
	if err != nil {
		return nil, err
	}
	return release, nil
}
