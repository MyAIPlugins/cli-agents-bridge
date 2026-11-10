//go:build !windows

package session

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// readHeldLockFile reads the lock file while another open file holds the kernel lock
// on it. On Unix the lock is advisory, so the read works and the caller compares the
// bytes with what it expects. The Windows half (lock_heldread_windows_test.go)
// asserts the opposite, because there the lock is mandatory.
func readHeldLockFile(t *testing.T, path string) (data []byte, readable bool) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, "the lock is advisory on Unix: a lock file that is held can be read")
	return data, true
}
