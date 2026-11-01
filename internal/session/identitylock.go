package session

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
)

// identityLockDir holds one lock file per identity, directly under the data dir.
const identityLockDir = "mcp-owners"

// ErrIdentityHeld is returned when another live process already holds the identity.
var ErrIdentityHeld = errors.New("identity is held by another process")

// ErrIdentityLockUnsupported is returned where the platform has no kernel lock
// this package can use (Windows, today). The caller refuses to start; it does not
// carry on without the exclusion.
var ErrIdentityLockUnsupported = errors.New("identity locks are not supported on this platform")

// HeldError says who holds the identity. PID is 0 when the holder wrote nothing
// readable: the number is a diagnostic, the lock itself is the kernel's.
type HeldError struct {
	PID  int
	Path string
}

func (e *HeldError) Error() string {
	if e.PID <= 0 {
		return fmt.Sprintf("%v (lock file %s; the holder's pid could not be read)", ErrIdentityHeld, e.Path)
	}
	return fmt.Sprintf("%v: pid %d (lock file %s)", ErrIdentityHeld, e.PID, e.Path)
}

// Is makes errors.Is(err, ErrIdentityHeld) true for a *HeldError.
func (e *HeldError) Is(target error) bool { return target == ErrIdentityHeld }

// IdentityLock is the exclusive right to speak as one identity (F-72). It is a
// kernel advisory lock (flock) on a file kept open for the life of the holder, and
// that is the point of choosing it over a PID file:
//
//   - the kernel drops it when the process dies, however it dies (kill -9
//     included), so there is no stale lock to detect and no takeover to race;
//   - a PID file recovers a stale lock by "remove, then create", and two
//     acquirers that read the same dead pid can each remove the other's fresh
//     file and both believe they hold it;
//   - the descriptor is close-on-exec, so a child the holder starts (the MCP
//     server runs `cab-bridge ask` as a child) cannot keep the lock alive after
//     the holder is gone.
//
// The file is never removed. An unlocked file is a free identity, and removing it
// would put back the very race the lock exists to avoid.
type IdentityLock struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// Path is the lock file, for diagnostics.
func (l *IdentityLock) Path() string { return l.path }

// Release gives the identity up. It is idempotent, and it closes the descriptor
// without unlinking the file.
func (l *IdentityLock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	if err != nil {
		return fmt.Errorf("identity lock: release %s: %w", l.path, err)
	}
	return nil
}

// AcquireIdentityLock takes the exclusive lock for identity, or fails at once:
// it never waits. identity is any string that names who is speaking (the MCP
// server passes the project's scope and the surface it is reached through, one
// identity per pair: projectKey in cmd/cab-bridge/mcp_app.go); the file name is a
// hash of it.
//
// A second process, or a second call in this one, gets a *HeldError. The lock
// directory is created at 0700 and refused if it is a symlink or belongs to
// somebody else (SC-2, SC-3).
func (m *Manager) AcquireIdentityLock(identity string) (*IdentityLock, error) {
	if !identityLocksSupported {
		return nil, ErrIdentityLockUnsupported
	}
	if strings.TrimSpace(identity) == "" {
		return nil, errors.New("identity lock: an identity is required")
	}
	dir := filepath.Join(m.DataDir, identityLockDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("identity lock: create %s: %w", dir, err)
	}
	if err := security.CheckOwnedDir(dir); err != nil {
		return nil, fmt.Errorf("identity lock: %w", err)
	}
	if err := security.EnforceDirPerms(dir, 0o700); err != nil {
		return nil, fmt.Errorf("identity lock: %w", err)
	}

	sum := sha256.Sum256([]byte(identity))
	path := filepath.Join(dir, hex.EncodeToString(sum[:16])+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("identity lock: open %s: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("identity lock: stat %s: %w", path, err)
	}
	// On the descriptor just opened, not on the path: what is checked is what is locked.
	if err := security.CheckOwnedInfo(path, info); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("identity lock: %w", err)
	}

	if err := lockExclusiveNonBlocking(f); err != nil {
		held := errors.Is(err, errLockContended)
		_ = f.Close()
		if !held {
			return nil, fmt.Errorf("identity lock: lock %s: %w", path, err)
		}
		return nil, &HeldError{PID: readHolderPID(path), Path: path}
	}

	// Who holds it, for the next contender's error message. Written after the lock
	// is taken; if it cannot be written the lock is given back, because a holder
	// nobody can name is exactly what this record is for.
	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("identity lock: record holder in %s: %w", path, err)
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("identity lock: record holder in %s: %w", path, err)
	}
	return &IdentityLock{f: f, path: path}, nil
}

func readHolderPID(path string) int {
	data, err := security.ReadOwnedFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
