//go:build !windows

package cleanup

// F-171 with the failures the operating system itself produces, on the production wiring:
// no seam, the real calls refused by a real permission. Each test PROVES its premise (the
// same operation, made by the test, is refused) before it runs the retirement, and a
// premise that does not hold fails the test; the only skip is the one every permission
// test of this package has, for root, which is not refused anything.

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// restrict sets a mode on dir and puts 0o700 back at the end, so the temp dir can be removed.
func restrict(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.Chmod(dir, mode))
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Logf("restore permissions of %s: %v", dir, err)
		}
	})
}

func TestArchiveAndRemoveSession_ARenameRefusedByTheOSKeepsTheMessageAndTheSession(t *testing.T) {
	requirePOSIXNonRoot(t)
	dataDir := t.TempDir()
	plantF171(t, dataDir)
	dst := filepath.Join(dataDir, "archive", archiveDate, f171SID, "inbox")
	require.NoError(t, os.MkdirAll(dst, 0o700))
	restrict(t, dst, 0o500)

	probe := filepath.Join(t.TempDir(), "probe.json")
	require.NoError(t, os.WriteFile(probe, []byte("x"), 0o600))
	require.ErrorIs(t, os.Rename(probe, filepath.Join(dst, "probe.json")), fs.ErrPermission,
		"premise: the archive folder refuses a rename into it")

	err := archiveAndRemoveSession(dataDir, f171SID, fixedNow, nil)

	requireEveryMessageIsSomewhere(t, dataDir)
	requireNothingMoved(t, dataDir)
	requireSessionIntact(t, dataDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "msg-aaaaaaaaaaaa.json", "the first message of the first folder is the one named")
}

func TestArchiveAndRemoveSession_AnArchiveFolderTheOSWillNotCreateKeepsTheMessageAndTheSession(t *testing.T) {
	requirePOSIXNonRoot(t)
	dataDir := t.TempDir()
	plantF171(t, dataDir)
	root := filepath.Join(dataDir, "archive")
	require.NoError(t, os.MkdirAll(root, 0o700))
	restrict(t, root, 0o500)
	require.ErrorIs(t, os.MkdirAll(filepath.Join(root, archiveDate), 0o700), fs.ErrPermission,
		"premise: the archive root refuses a new folder")

	err := archiveAndRemoveSession(dataDir, f171SID, fixedNow, nil)

	requireEveryMessageIsSomewhere(t, dataDir)
	requireNothingMoved(t, dataDir)
	requireSessionIntact(t, dataDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot create the archive folder")
}

// With a real permission the listing and the later removal are refused together, so this
// case never lost the message even before the fix: it left it in a folder without a
// manifest that no pass collects. The test stands on the manifest for that reason.
func TestArchiveAndRemoveSession_AMessageFolderTheOSWillNotListKeepsTheMessageAndTheSession(t *testing.T) {
	requirePOSIXNonRoot(t)
	dataDir := t.TempDir()
	plantF171(t, dataDir)
	inbox := sessionPath(dataDir, f171SID, "inbox")
	restrict(t, inbox, 0o000)
	_, perr := os.ReadDir(inbox)
	require.ErrorIs(t, perr, fs.ErrPermission, "premise: the folder cannot be listed")

	err := archiveAndRemoveSession(dataDir, f171SID, fixedNow, nil)

	_ = os.Chmod(inbox, 0o700) // to look inside it, whatever happened
	requireEveryMessageIsSomewhere(t, dataDir)
	requireNothingMoved(t, dataDir)
	requireSessionIntact(t, dataDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot list")
	assert.Contains(t, err.Error(), inbox)
}
