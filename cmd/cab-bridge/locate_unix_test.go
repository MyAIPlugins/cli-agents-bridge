//go:build !windows

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-178 / N1: an entry of processed/ whose ownership check fails. Not ours (a symlink: the same
// refusal as another owner, security.ErrOwnershipMismatch) is skipped by both looks: a name
// alone does not forge "archived". An entry that cannot be OPENED is skipped by the readers and is
// an error for the delivery, which acts on "nothing found".
func TestLocate_AnEntryThatCannotBeCheckedIsSkippedByReadersAndRefusedByTheDelivery(t *testing.T) {
	t.Run("not ours: a symlink is skipped by both", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(t.TempDir(), "elsewhere.json")
		require.NoError(t, os.WriteFile(target, []byte("{}"), 0o600))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "processed"), 0o700))
		require.NoError(t, os.Symlink(target, filepath.Join(dir, "processed", "20260101T000000.000000000Z-"+locateID+".json")))

		_, err := locateMessage(dir, locateID)
		require.ErrorIs(t, err, errMessageNotFound)
		copies, err := locateCopiesStrict(dir, locateID)
		require.NoError(t, err)
		assert.Empty(t, copies, "a symlink is not ours: the same answer as no entry")
	})

	t.Run("cannot be opened: skipped by the readers, an error for the delivery", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root opens a file with no permission")
		}
		dir := t.TempDir()
		path := plantProcessedFile(t, dir, "20260101T000000.000000000Z", locateID, "{}")
		require.NoError(t, os.Chmod(path, 0o000))
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

		_, err := locateMessage(dir, locateID)
		require.ErrorIs(t, err, errMessageNotFound, "the readers skip it, as before")

		_, err = locateCopiesStrict(dir, locateID)
		require.Error(t, err, "it is NOT 'nothing found': it may be the copy that was delivered")
		assert.ErrorIs(t, err, fs.ErrPermission)
	})
}
