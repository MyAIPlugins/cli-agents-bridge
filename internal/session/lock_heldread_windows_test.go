//go:build windows

package session

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// readHeldLockFile reads the lock file while another open file holds the kernel lock
// on it. On Windows the lock is MANDATORY (LockFileEx on byte 0, see
// internal/fslock/fslock_windows.go): a read that reaches the locked byte through
// another handle fails with ERROR_LOCK_VIOLATION, even in the same process and even
// when the file is empty. That is the documented behaviour and it is pinned here
// instead of skipped; the callers then check what they can without reading the
// bytes (the size, from Stat, which is not blocked) and read them after the release.
func readHeldLockFile(t *testing.T, path string) (data []byte, readable bool) {
	t.Helper()
	_, err := os.ReadFile(path)
	require.Error(t, err, "the lock is mandatory on Windows: reading a lock file that is held must fail")
	require.ErrorIs(t, err, windows.ERROR_LOCK_VIOLATION, "and it must fail because of the lock, not for another reason")
	return nil, false
}
