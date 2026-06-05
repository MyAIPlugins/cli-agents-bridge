package fs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAtomicWriteJSON_RoundTrip(t *testing.T) {
	t.Parallel()

	target := filepath.Join(t.TempDir(), "manifest.json")
	in := map[string]interface{}{
		"sessionId":     "abc123",
		"schemaVersion": float64(2),
		"role":          "val",
	}

	require.NoError(t, AtomicWriteJSON(target, in))

	var out map[string]interface{}
	require.NoError(t, ReadJSON(target, &out))
	assert.Equal(t, in, out)
}

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

func TestAtomicWriteIdempotent_LastWriteWins(t *testing.T) {
	t.Parallel()

	target := filepath.Join(t.TempDir(), "overwrite.json")

	require.NoError(t, AtomicWriteJSON(target, map[string]int{"v": 1}))
	require.NoError(t, AtomicWriteJSON(target, map[string]int{"v": 2}))

	var got map[string]int
	require.NoError(t, ReadJSON(target, &got))
	assert.Equal(t, 2, got["v"], "second write must win")
}

func TestAtomicWriteSameDirGuarantee(t *testing.T) {
	t.Parallel()

	// Verify the tempfile created internally lives in the target's parent
	// directory (same-filesystem guarantee). We can't observe the tempfile
	// directly because it's renamed before AtomicWriteBytes returns, but we
	// can confirm no stale temp file leaked into TMPDIR after success.
	target := filepath.Join(t.TempDir(), "guard.txt")
	tmpDir := os.TempDir()
	beforeCount := countTmpFiles(t, tmpDir, ".tmp.")

	require.NoError(t, AtomicWriteBytes(target, []byte("ok"), 0o600))

	afterCount := countTmpFiles(t, tmpDir, ".tmp.")
	assert.Equal(t, beforeCount, afterCount,
		"AtomicWriteBytes must not leak temp files into TMPDIR — temp must live in target's directory")

	// Confirm target written
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "ok", string(data))
}

func TestReadJSON_BadPath(t *testing.T) {
	t.Parallel()

	var v map[string]interface{}
	err := ReadJSON(filepath.Join(t.TempDir(), "missing.json"), &v)
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestReadJSON_MalformedJSON(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "malformed.json")
	require.NoError(t, os.WriteFile(path, []byte("{not valid json"), 0o600))

	var v map[string]interface{}
	err := ReadJSON(path, &v)
	require.Error(t, err)
}

func countTmpFiles(t *testing.T, dir, prefix string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		// e.g. /tmp not readable in sandboxed CI — return 0 and skip check
		return 0
	}
	n := 0
	for _, e := range entries {
		if len(e.Name()) >= len(prefix) && e.Name()[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}
