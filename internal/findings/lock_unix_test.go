//go:build !windows

package findings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTryLockFile_CreatesTheLockFileWithMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "findings.lock")
	release, err := TryLockFile(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, release()) }()

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestTryLockFile_RefusesASymlinkedLockFileAndLeavesTheTargetAlone(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "victim")
	require.NoError(t, os.WriteFile(target, []byte("keep"), 0o600))

	cases := map[string]string{
		"valid link":  target,
		"broken link": filepath.Join(dir, "does-not-exist"),
	}
	for name, dest := range cases {
		link := filepath.Join(dir, "findings-"+filepath.Base(dest)+".lock")
		require.NoError(t, os.Symlink(dest, link), name)

		_, err := TryLockFile(link)
		require.Error(t, err, name)
		assert.NotErrorIs(t, err, ErrBusy, name)
		assert.Contains(t, err.Error(), "symlink", name)
	}

	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "keep", string(data))
	assert.NoFileExists(t, filepath.Join(dir, "does-not-exist"), "a broken link must not be followed into a creation")
}

// An existing lock file needs no permission to create files in its directory:
// the lock keeps working when the folder is read-only (and `stale` does not take
// it at all).
func TestTryLockFile_ExistingFileInAReadOnlyDirectoryStillLocks(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "findings.lock")
	release, err := TryLockFile(path)
	require.NoError(t, err)
	require.NoError(t, release())

	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	release, err = TryLockFile(path)
	require.NoError(t, err)
	require.NoError(t, release())
}

func TestTryLockFile_ReadOnlyDirectoryWithoutTheFileIsAnError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, err := TryLockFile(filepath.Join(dir, "findings.lock"))
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrBusy)
}
