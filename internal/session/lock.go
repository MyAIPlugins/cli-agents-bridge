package session

import (
	"errors"
	"fmt"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"os"
	"strconv"
	"strings"
)

// ErrLockHeld is returned by AcquireLock when the lock file exists and is
// held by a live process (verified via IsProcessAlive — kill(pid, 0) on
// Unix, OpenProcess on Windows; see alive_unix.go / alive_windows.go).
//
// IT CARRIES NO REMEDY, and that is the fix for F-126. This line used to say
// «callers should surface this with the hint "use --force-new to override"»,
// and AcquireLock put that hint in the message itself — so it travelled to all
// eleven call sites. It is true at exactly one of them: Register, the only
// caller whose forceNew argument comes from a flag. The other ten pass a hard
// -coded false, and five of them are reached from the five loop verbs, which
// v0.8 gave NO FLAGS AT ALL: `cab-bridge ask --force-new` answers "takes no
// flags". v0.8 removed the flags from the loop and this message did not hear
// about it.
//
// An error with no remedy leaves the reader stopped; an error with a remedy
// that does not exist sends them down a closed road while making them believe
// there is one. So the remedy belongs to whoever knows the command — and the
// two remedies are opposites: at Register the holder is a LIVE SESSION and
// retrying never helps (hence --force-new), everywhere else the hold lasts one
// short operation and retrying is exactly right.
var ErrLockHeld = errors.New("session lock held by live process")

// AcquireLock attempts to atomically create lockPath with the current PID.
// Implements Security Control SC-6 (PLAN §9):
//
//   - os.O_CREATE|os.O_EXCL|os.O_WRONLY with mode 0o600: atomic creation,
//     no race with other processes attempting the same lock.
//   - Stale recovery: if the lock exists, read its PID and probe with
//     kill(pid, 0). ESRCH (no such process) → remove + retry once. EPERM
//     (process exists, different UID) → treat as alive (return ErrLockHeld).
//     Same PID as ours → re-entrant acquire, treat as success.
//
// forceNew, when true, unconditionally removes any existing lockPath before
// acquiring. Use only via explicit CLI --force-new flag — never default.
// (BUG-6 fix per PLAN §4.5.)
//
// On success returns a release function that removes the lock file. The
// caller MUST defer release() and additionally install a signal handler
// (SIGTERM/SIGINT) for abnormal exit — installed by cmd/cab-bridge, not
// here, to keep this helper testable in isolation.
func AcquireLock(lockPath string, forceNew bool) (release func() error, err error) {
	if forceNew {
		// Best-effort remove. Failure is non-fatal: O_EXCL below will retry
		// the attempt anyway.
		_ = os.Remove(lockPath)
	}

	if rel, err := tryCreate(lockPath); err == nil {
		return rel, nil
	} else if !errors.Is(err, os.ErrExist) {
		// Genuine I/O failure (permission denied, parent dir missing, etc.)
		return nil, err
	}

	// Lock exists. Check whether it is stale.
	existingPID, perr := readPIDFromLock(lockPath)
	if perr != nil {
		// Unreadable or malformed lock file — treat as stale and try once.
		_ = os.Remove(lockPath)
		return tryCreate(lockPath)
	}

	if existingPID == os.Getpid() {
		// Re-entrant: we already hold this lock from an earlier call.
		// Return a no-op release to avoid removing the lock from under
		// the original holder.
		return func() error { return nil }, nil
	}

	if IsProcessAlive(existingPID) {
		// Facts only: the primitive does not know which command is calling it,
		// and any remedy it names is wrong for most of them (see ErrLockHeld).
		return nil, fmt.Errorf("%w: lockPath=%q holder pid=%d",
			ErrLockHeld, lockPath, existingPID)
	}

	// Stale: holder pid not alive. Remove + retry exactly once. If the
	// retry races with another acquirer, we surrender (no infinite loop).
	_ = os.Remove(lockPath)
	return tryCreate(lockPath)
}

// tryCreate is the single O_EXCL atomic attempt. Returns os.ErrExist if the
// lock file already exists (caller decides whether to do stale recovery).
func tryCreate(lockPath string) (func() error, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	pid := os.Getpid()
	if _, werr := fmt.Fprintf(f, "%d\n", pid); werr != nil {
		_ = os.Remove(lockPath)
		return nil, fmt.Errorf("write PID to lock %q: %w", lockPath, werr)
	}

	return func() error {
		if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("release lock %q: %w", lockPath, err)
		}
		return nil
	}, nil
}

// readPIDFromLock reads lockPath and parses the PID written by tryCreate.
// Trims a single trailing newline if present (the format we write).
func readPIDFromLock(lockPath string) (int, error) {
	data, err := security.ReadOwnedFile(lockPath)
	if err != nil {
		return 0, err
	}
	s := strings.TrimRight(string(data), "\n")
	return strconv.Atoi(s)
}
