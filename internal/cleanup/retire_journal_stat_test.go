//go:build !windows

package cleanup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-176: the retirement asks whether a reply is in flight with a Stat of reply-txn.json. It used
// to read the answer "no" from ANY error, so a journal that could not be examined (a loop, a
// permission, an I/O error) let the retirement go on and delete a session that had a reply in
// flight. An error that is not "it is not there" now stops the retirement before it touches
// anything, with the name of the file, as F-171 did for the archive: a retirement that cannot
// know does not delete.
//
// The journal is a symbolic link to itself, so Stat answers ELOOP. Symbolic links ask for a
// privilege on Windows, and the code under test is the same on every system.
func TestArchiveAndRemoveSession_AJournalThatCannotBeExaminedStopsTheRetirement(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantStale(t, dataDir, "aaaaaaag")
	writeFileIn(t, sessionPath(dataDir, "aaaaaaag", "inbox", "msg-aaaaaaaaaaaa.json"), "unread")
	journal := sessionPath(dataDir, "aaaaaaag", "reply-txn.json")
	require.NoError(t, os.Symlink("reply-txn.json", journal))

	_, statErr := os.Stat(journal)
	require.Error(t, statErr, "premise: the journal cannot be examined")
	require.False(t, errors.Is(statErr, fs.ErrNotExist), "premise: the error is not 'it is not there', got %v", statErr)

	err := archiveAndRemoveSession(dataDir, "aaaaaaag", func() time.Time { return failuresBase }, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "reply-txn.json", "the error names the file it could not examine")
	assert.False(t, errors.Is(err, ErrSessionBusy), "it is a failure with a reason, not contention: %v", err)
	assert.FileExists(t, sessionPath(dataDir, "aaaaaaag", "manifest.json"), "the session was not retired")
	assert.FileExists(t, sessionPath(dataDir, "aaaaaaag", "inbox", "msg-aaaaaaaaaaaa.json"), "and no message was touched")
	assert.NoDirExists(t, filepath.Join(dataDir, "archive"), "and nothing was archived")
}
