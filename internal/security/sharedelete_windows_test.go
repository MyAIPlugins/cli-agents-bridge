//go:build windows

package security

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// F-133, the half that lives in this package.
//
// The defect takes TWO changes and they are not interchangeable: the readers
// here have to grant FILE_SHARE_DELETE, and the replace primitive over in
// internal/transport/fs has to use POSIX rename semantics. Neither works alone —
// measured, not reasoned: with the old reader even the POSIX replace fails, and
// with the new reader os.Rename still fails.
//
// So this file pins the PRECONDITION, and rename_windows_test.go over there pins
// the property that depends on it. Splitting them this way is what keeps each
// test naming its own subject: if the share mode regresses, THIS goes red.

// plantFile writes content at path and returns path, so a fixture reads as one
// line at the call site.
func plantFile(t *testing.T, path, content string) string {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// TestOpenNoFollow_GrantsShareDeleteSoTheFileCanBeSuperseded is the
// precondition, and os.Remove is how it is observed.
//
// A delete is the same class of operation as the replace half of a rename, and
// unlike the rename it does not also depend on the caller using POSIX semantics
// — which makes it the cleanest available probe of the share mode alone. It
// succeeds here and fails in the control below, and that difference IS the fix.
func TestOpenNoFollow_GrantsShareDeleteSoTheFileCanBeSuperseded(t *testing.T) {
	dir := t.TempDir()
	target := plantFile(t, filepath.Join(dir, "manifest.json"), "OLD")

	held, err := openNoFollow(target)
	require.NoError(t, err, "the reader must open")
	defer func() { _ = held.Close() }()

	require.NoError(t, os.Remove(target),
		"a file this process is reading must remain removable: without FILE_SHARE_DELETE every atomic "+
			"write in the project races its own readers")
}

// TestOpenNoFollow_ThePlainOsOpenControlFreezesTheFile keeps the test above
// honest.
//
// A green test proves nothing until it has been seen to go red for the right
// reason. os.Open asks for FILE_SHARE_READ|WRITE and not DELETE, so it still
// reproduces the original defect on demand — and if this one ever passes, Go's
// own share mode changed and the test above has stopped having a subject.
func TestOpenNoFollow_ThePlainOsOpenControlFreezesTheFile(t *testing.T) {
	dir := t.TempDir()
	target := plantFile(t, filepath.Join(dir, "manifest.json"), "OLD")

	held, err := os.Open(target)
	require.NoError(t, err)
	defer func() { _ = held.Close() }()

	err = os.Remove(target)
	require.Error(t, err, "os.Open without FILE_SHARE_DELETE must still freeze the file: the defect, on purpose")
	assert.True(t,
		errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION),
		"and fail with the Win32 error F-133 is about, not some other one: %v", err)
}

// TestOpenNoFollow_StillRefusesASymlink covers the branch next door: the fix
// replaced the open, and the open was also where the symlink refusal lived.
//
// It is checked twice over now — the Lstat before, and the reparse TAG on the
// handle after — so what this asserts is the outcome the caller sees, which is
// the same error as before.
func TestOpenNoFollow_StillRefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	target := plantFile(t, filepath.Join(dir, "real.json"), "REAL")
	link := filepath.Join(dir, "link.json")

	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skipf("symlink fixture needs SeCreateSymbolicLinkPrivilege, which this host does not grant "+
				"(Developer Mode off and process not elevated); the refusal is NOT verified here: %v", err)
		}
		require.NoError(t, err)
	}

	f, err := openNoFollow(link)
	if err == nil {
		_ = f.Close()
	}
	require.Error(t, err, "a symlink must be refused, not opened")
	assert.ErrorIs(t, err, ErrOwnershipMismatch,
		"and refused as an ownership mismatch, the verdict every caller already handles")
}

// TestOpenNoFollow_ADirectoryStillReachesTheNotARegularFileVerdict guards the
// flag nobody would think to test.
//
// CreateFile refuses a directory outright unless FILE_FLAG_BACKUP_SEMANTICS is
// passed — which os.Open passes and the first draft of this fix did not. Drop
// that flag and a directory stops answering "not a regular file" and starts
// answering "Access is denied": the same refusal, wearing the error message of
// a permissions problem, sending the next reader of the log somewhere else
// entirely.
func TestOpenNoFollow_ADirectoryStillReachesTheNotARegularFileVerdict(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "inbox")
	require.NoError(t, os.MkdirAll(sub, 0o700))

	_, err := ReadOwnedFile(sub)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not a regular file",
		"a directory must be refused for what it is: %v", err)
}
