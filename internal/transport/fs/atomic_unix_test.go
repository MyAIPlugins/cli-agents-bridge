//go:build !windows

package fs

// The umask half of the atomic-write contract. Split out rather than tagging the
// whole file: the round-trip, the last-write-wins and the same-directory
// guarantee are portable claims and have to keep being asserted on Windows —
// where the rename underneath them is MoveFileEx, not rename(2), which is
// exactly the piece nobody has run yet.

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAtomicWriteBytes_EnforcesPerms(t *testing.T) {
	// Test mutates umask temporarily to prove explicit chmod works even
	// when the process umask is permissive. Must run serially.
	prev := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(prev) })

	target := filepath.Join(t.TempDir(), "perm-test.bin")
	require.NoError(t, AtomicWriteBytes(target, []byte("x"), 0o600))

	info, err := os.Stat(target)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"AtomicWriteBytes must produce 0o600 regardless of process umask")
}
