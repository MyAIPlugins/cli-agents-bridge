//go:build windows

package session

// identityLocksSupported is false on Windows on purpose, although internal/fslock
// does lock there (LockFileEx, proven in CI for the findings registry): the MCP
// port is out of scope on Windows (F-72), and the identity lock records its holder's
// pid in the locked file for the next contender to read (readHolderPID), while the
// Windows lock is mandatory: a process that reads the locked byte gets an error
// (see fslock_windows.go). Turning it on is a design (a second file for the pid,
// or no pid), not a constant. AcquireIdentityLock returns ErrIdentityLockUnsupported
// before it touches the filesystem, so its call to fslock.Lock is never reached here.
const identityLocksSupported = false
