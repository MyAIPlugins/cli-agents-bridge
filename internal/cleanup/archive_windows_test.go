//go:build windows

package cleanup

// F-171, criterion 3 of the BAR: the rename of the archive step goes through the retry of
// F-133, on the production wiring. Only a Windows runner can run this.
//
// The source of a message is held open the way a reader that did not grant deletion holds
// it (os.Open, which does not, as rename_windows_test.go in internal/transport/fs relies
// on). The rename of F-133 asks for DELETE access and is refused with a sharing violation,
// which it treats as transient: six attempts, and then an error that says so. os.Rename is
// MoveFileEx, refuses at once, and its message has no "attempts" in it, so that word is what
// tells the two apart WITHOUT depending on time: a test that released the file after a delay
// would depend on the runner's scheduling.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArchiveAndRemoveSession_ASourceHeldOpenGoesThroughTheRetryOfF133(t *testing.T) {
	dataDir := t.TempDir()
	plantF171(t, dataDir)
	held := sessionCopy(dataDir, "inbox/msg-aaaaaaaaaaaa.json")
	h, err := os.Open(held)
	require.NoError(t, err)
	t.Cleanup(func() {
		if cerr := h.Close(); cerr != nil {
			t.Logf("close the held file: %v", cerr)
		}
	})
	require.Error(t, os.Rename(held, filepath.Join(t.TempDir(), "probe.json")),
		"premise: a file held open without sharing deletion cannot be renamed")

	got := archiveAndRemoveSession(dataDir, f171SID, fixedNow, nil)

	require.Error(t, got)
	assert.Contains(t, got.Error(), "msg-aaaaaaaaaaaa.json", "the error names the file")
	assert.Contains(t, got.Error(), "attempts", "and it is the error of the retry: six attempts were made before giving up")
	requireSessionIntact(t, dataDir)
	requireNothingMoved(t, dataDir)
	requireEveryMessageIsSomewhere(t, dataDir)
}
