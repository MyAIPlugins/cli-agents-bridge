//go:build !windows

package session

// identityLocksSupported: the kernel lock behind the identity lock is internal/fslock
// (flock here). The file is advisory on Unix, so the holder can write its pid in it.
const identityLocksSupported = true
