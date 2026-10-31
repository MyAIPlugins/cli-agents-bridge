//go:build windows

package session

import (
	"errors"
	"os"
)

// There is no flock here and no replacement has been designed or tested: the
// MCP port is out of scope on Windows (F-72), and a lock that is not exclusive
// would be worse than none. AcquireIdentityLock returns ErrIdentityLockUnsupported
// before it touches the filesystem, so this file only has to compile.
const identityLocksSupported = false

var errLockContended = errors.New("lock contended")

func lockExclusiveNonBlocking(*os.File) error { return ErrIdentityLockUnsupported }
