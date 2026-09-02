//go:build !windows

package session

import (
	"errors"
	"syscall"
)

// IsProcessAlive returns true if a process with pid exists. Uses kill(pid, 0)
// which is a no-op signal that only checks process existence + sendability.
//
// Exported because the auto-gc orphan sweep (internal/cleanup.GCOrphans, v0.2.1)
// shares this liveness probe: an orphan is a session whose owning PID is no
// longer alive AND whose heartbeat is stale (the double condition guards the
// register-then-die window, LL-10).
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
//
// The Windows implementation in alive_windows.go answers the same question with
// the same conservative bias — an unrecognised failure means alive — because
// four callers depend on that bias, not on the syscall.
func IsProcessAlive(pid int) bool {
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
