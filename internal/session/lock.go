package session

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ErrLockHeld is returned by AcquireLock when the lock file exists and is
// held by a live process (verified via kill(pid, 0)). Callers should surface
// this with the hint "use --force-new to override".
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

	if isProcessAlive(existingPID) {
		return nil, fmt.Errorf("%w: lockPath=%q holder pid=%d (use --force-new to override)",
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
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return 0, err
	}
	s := strings.TrimRight(string(data), "\n")
	return strconv.Atoi(s)
}

// isProcessAlive returns true if a process with pid exists. Uses kill(pid, 0)
// which is a no-op signal that only checks process existence + sendability.
//
// Return semantics:
//   - nil err: process exists and we have permission to signal it.
//   - EPERM:    process exists but is owned by another UID. Still "alive".
//   - ESRCH:    no such process. Stale lock.
//   - other:    unexpected — log warning and conservatively report alive
//     (false positive is safer than overwriting a live lock).
//
// Negative or zero pid is treated as not-alive (PID=0 cannot be killed,
// PID<0 means "process group" which we never write).
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.EPERM) {
		return true
	}
	if errors.Is(err, syscall.ESRCH) {
		return false
	}
	// Conservative: unknown error → assume alive to avoid overwriting a
	// legitimate live lock by mistake.
	return true
}
