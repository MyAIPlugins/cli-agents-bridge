//go:build windows

package session

import (
	"errors"

	"golang.org/x/sys/windows"
)

// stillActive is STILL_ACTIVE from kernel32: the exit code GetExitCodeProcess
// reports for a process that has not exited. It is defined here because NEITHER
// syscall NOR x/sys/windows exports it — verified by compiling, not assumed.
const stillActive = 259

// IsProcessAlive answers the same question as the Unix version (alive_unix.go)
// with the same CONSERVATIVE BIAS: anything we fail to understand counts as
// alive, because all four callers — stale-lock recovery (lock.go), ownership of
// the next wait (listener.go), collision/resume in join (manager.go) and the
// auto-gc sweep (cleanup/gc.go) — would rather leave a dead session alone than
// evict a live one.
//
// The mapping, and each line is the counterpart of one on Unix:
//
//	OpenProcess succeeds          →  ask GetExitCodeProcess
//	ERROR_INVALID_PARAMETER       →  no such process (Unix ESRCH) → dead
//	ERROR_ACCESS_DENIED           →  it exists, another user owns it (EPERM) → alive
//	any other OpenProcess failure →  unknown → alive, like Unix's default branch
//
// PROCESS_QUERY_LIMITED_INFORMATION rather than PROCESS_QUERY_INFORMATION on
// purpose: it is the right designed to be grantable across integrity levels, so
// the "exists but is not ours" case reaches ERROR_ACCESS_DENIED instead of
// failing for a reason we would have to guess at.
//
// KNOWN LIMIT, and it is worse here than on Unix: Windows recycles PIDs
// aggressively (multiples of 4, reassigned as soon as they are free), so a stale
// lock whose PID has been handed to an unrelated process reads as ALIVE — the
// session is never recovered, the wait never reclaimed, the gc never sweeps it,
// and every command reports ok. Nothing in this function can see that; closing
// it needs the process start time stored alongside the PID, which changes the
// on-disk format. That is lot 2 of the port, not this one.
//
// Second, smaller limit: a process that exits WITH code 259 is indistinguishable
// from a running one. It falls on the conservative side, which is the side this
// function is meant to fall on.
//
// [DEDUCED, not executed] — written on a Mac, cross-compiled, never run on
// Windows. Every error path names its Win32 call so the first failure on the
// machine says which one, instead of just "not alive".
func IsProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// The only verdict of "dead" this function is willing to reach from a
		// failed open: Windows reports a PID that does not exist as an invalid
		// parameter. Everything else — access denied included — means the process
		// is there, or that we do not know.
		return !errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer func() { _ = windows.CloseHandle(h) }()

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		// The handle opened, so something is there. Conservative, as above.
		return true
	}
	return code == stillActive
}
