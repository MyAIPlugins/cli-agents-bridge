//go:build !windows

package fslock

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTryLock_CreatesTheLockFileWithMode0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	release, err := TryLock(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, release()) }()

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestTryLock_RefusesASymlinkedLockFileAndLeavesTheTargetAlone(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "victim")
	require.NoError(t, os.WriteFile(target, []byte("keep"), 0o600))

	cases := map[string]string{
		"valid link":  target,
		"broken link": filepath.Join(dir, "does-not-exist"),
	}
	for name, dest := range cases {
		link := filepath.Join(dir, "x-"+filepath.Base(dest)+".lock")
		require.NoError(t, os.Symlink(dest, link), name)

		_, err := TryLock(link)
		require.Error(t, err, name)
		assert.NotErrorIs(t, err, ErrBusy, name)
		assert.Contains(t, err.Error(), "symlink", name)
	}

	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "keep", string(data))
	assert.NoFileExists(t, filepath.Join(dir, "does-not-exist"), "a broken link must not be followed into a creation")
}

// An existing lock file needs no permission to create files in its directory: the
// lock keeps working when the folder is read-only.
func TestTryLock_ExistingFileInAReadOnlyDirectoryStillLocks(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "x.lock")
	release, err := TryLock(path)
	require.NoError(t, err)
	require.NoError(t, release())

	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	release, err = TryLock(path)
	require.NoError(t, err)
	require.NoError(t, release())
}

func TestTryLock_ReadOnlyDirectoryWithoutTheFileIsAnError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, err := TryLock(filepath.Join(dir, "x.lock"))
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrBusy)
}

// The rule of D7: only the errno that means "someone else holds it" is busy. The
// others are the call failing, and reading one of them as contention would turn a
// filesystem without locks into a wait for a holder that does not exist.
func TestClassify_OnlyTheBusyErrnoIsBusy(t *testing.T) {
	assert.NoError(t, classify("n", nil))
	assert.Equal(t, ErrBusy, classify("n", syscall.EWOULDBLOCK))
	assert.Equal(t, ErrBusy, classify("n", syscall.EAGAIN), "EAGAIN is EWOULDBLOCK")

	for _, errno := range []syscall.Errno{syscall.EINVAL, syscall.ENOTSUP, syscall.ENOLCK, syscall.EBADF, syscall.ENOSYS, syscall.EINTR} {
		err := classify("the-file", errno)
		require.Error(t, err, errno.Error())
		assert.NotErrorIs(t, err, ErrBusy, errno.Error())
		assert.True(t, errors.Is(err, errno), "the cause stays reachable: %v", errno)
		assert.Contains(t, err.Error(), "the-file", "and the file is named")
	}
}
