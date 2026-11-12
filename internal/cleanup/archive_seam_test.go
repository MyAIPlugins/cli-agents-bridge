package cleanup

// F-171 with a seam: the three calls of the archive step (listing, creating a folder,
// renaming) are replaced one at a time by a call that fails, on every platform and without
// a permission. That is the only way to reproduce a refusal that is transient, and the one
// that stops a Windows runner, where a folder permission is not available to stand on.
//
// Every test counts how many times the failing call was made, and fails if it was not
// made: a failure that was never injected would leave a green test that proves nothing.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

var errInjected = errors.New("injected archive failure")

// failingRename makes the rename of the file called name fail and counts the attempts at
// it; every other rename is the production one. The error is a bare one on purpose: a
// *os.LinkError carries both paths in its own message, and then the test could not tell
// whether the retirement names the file or merely repeats an error that does.
func failingRename(name string, hits *int) func(string, string) error {
	return func(oldpath, newpath string) error {
		if filepath.Base(oldpath) != name {
			return transportfs.RenameAtomic(oldpath, newpath)
		}
		*hits++
		return errInjected
	}
}

func TestArchiveAndRemoveSession_ARenameThatFailsStopsBeforeTheManifestAndNamesTheFile(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantF171(t, dataDir)
	hits := 0

	err := retireWithSeams(dataDir, f171SID, fixedNow, retireSeams{rename: failingRename("msg-bbbbbbbbbbbb.json", &hits)})

	require.Equal(t, 1, hits, "premise: the injected failure was hit, once")
	requireEveryMessageIsSomewhere(t, dataDir)
	requireSessionIntact(t, dataDir)
	assert.FileExists(t, archiveCopy(dataDir, "inbox/msg-aaaaaaaaaaaa.json"), "the first message was moved before the failure")
	assert.NoFileExists(t, sessionCopy(dataDir, "inbox/msg-aaaaaaaaaaaa.json"))
	for _, rel := range f171Messages[1:] {
		assert.FileExists(t, sessionCopy(dataDir, rel), "%s was not reached, so it stays in the session", rel)
		assert.NoFileExists(t, archiveCopy(dataDir, rel))
	}
	require.Error(t, err)
	assert.ErrorIs(t, err, errInjected)
	assert.False(t, errors.Is(err, ErrSessionBusy), "a failed archive is a failure, not a busy session")
	assert.Contains(t, err.Error(), "msg-bbbbbbbbbbbb.json", "the error names the file")
	assert.Contains(t, err.Error(), "1 of 4 messages are already in the archive", "and says what state it left")
	assert.Contains(t, err.Error(), "run the full cleanup again", "and what to do")
}

func TestArchiveAndRemoveSession_AListingThatFailsStopsBeforeAnythingIsMoved(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantF171(t, dataDir)
	listings := 0
	seams := retireSeams{readDir: func(dir string) ([]os.DirEntry, error) {
		if filepath.Base(dir) != "processed" {
			return os.ReadDir(dir)
		}
		listings++
		return nil, &os.PathError{Op: "open", Path: dir, Err: fs.ErrPermission}
	}}

	err := retireWithSeams(dataDir, f171SID, fixedNow, seams)

	require.Equal(t, 1, listings, "premise: the listing of processed/ was refused")
	requireEveryMessageIsSomewhere(t, dataDir)
	requireNothingMoved(t, dataDir) // inbox/ and outbox/ were listed first and are still where they were
	requireSessionIntact(t, dataDir)
	assert.NoDirExists(t, filepath.Join(dataDir, "archive"), "not even an archive folder: the listing of all three comes first")
	require.Error(t, err)
	assert.ErrorIs(t, err, fs.ErrPermission)
	assert.Contains(t, err.Error(), "cannot list")
	assert.Contains(t, err.Error(), sessionPath(dataDir, f171SID, "processed"), "the error names the folder")
}

func TestArchiveAndRemoveSession_AnArchiveFolderThatCannotBeCreatedStopsBeforeAnythingIsMoved(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantF171(t, dataDir)
	refused := 0
	seams := retireSeams{mkdirAll: func(dir string, perm os.FileMode) error {
		if filepath.Base(dir) != "outbox" {
			return os.MkdirAll(dir, perm)
		}
		refused++
		return &os.PathError{Op: "mkdir", Path: dir, Err: fs.ErrPermission}
	}}

	err := retireWithSeams(dataDir, f171SID, fixedNow, seams)

	require.Equal(t, 1, refused, "premise: the creation of the outbox archive folder was refused")
	requireEveryMessageIsSomewhere(t, dataDir)
	requireNothingMoved(t, dataDir)
	requireNoFileInArchive(t, dataDir) // the inbox archive folder, made first, may be left empty: declared
	requireSessionIntact(t, dataDir)
	require.Error(t, err)
	assert.ErrorIs(t, err, fs.ErrPermission)
	assert.Contains(t, err.Error(), "cannot create the archive folder")
	assert.Contains(t, err.Error(), filepath.Join("archive", archiveDate, f171SID, "outbox"), "the error names the folder")
}

// A stop is not the end of the session: the pass after it, with nothing failing, finishes
// the job, and every message ends up in the archive intact.
func TestArchiveAndRemoveSession_ThePassAfterAFailureFinishesTheJob(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantF171(t, dataDir)
	hits := 0
	first := retireWithSeams(dataDir, f171SID, fixedNow, retireSeams{rename: failingRename("msg-bbbbbbbbbbbb.json", &hits)})
	require.Error(t, first)
	require.Equal(t, 1, hits, "premise: the first pass failed on the injected rename")

	second := archiveAndRemoveSession(dataDir, f171SID, fixedNow, nil)

	require.NoError(t, second)
	assert.NoDirExists(t, sessionPath(dataDir, f171SID), "the session is retired now")
	for _, rel := range f171Messages {
		got, err := os.ReadFile(archiveCopy(dataDir, rel))
		require.NoError(t, err, "%s is in the archive", rel)
		assert.Equal(t, "content of "+rel, string(got))
	}
}

// Criterion 4 of the BAR: with nothing failing the step makes one rename per file and
// nothing else. The wait of F-133 is inside RenameAtomic and is spent only on an error;
// that this package has no wait of its own is fixed in structure_test.go.
func TestArchiveAndRemoveSession_TheNormalRetirementRenamesEachFileOnce(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantF171(t, dataDir)
	var sources []string
	seams := retireSeams{rename: func(oldpath, newpath string) error {
		sources = append(sources, oldpath)
		return transportfs.RenameAtomic(oldpath, newpath)
	}}

	err := retireWithSeams(dataDir, f171SID, fixedNow, seams)

	require.NoError(t, err)
	var want []string
	for _, rel := range f171Messages {
		want = append(want, sessionCopy(dataDir, rel))
	}
	assert.ElementsMatch(t, want, sources, "one rename per message file, none twice")
	assert.NoDirExists(t, sessionPath(dataDir, f171SID))
	for _, rel := range f171Messages {
		assert.FileExists(t, archiveCopy(dataDir, rel))
	}
}

// A folder that is not there is the ordinary case (a session that never sent anything has
// no outbox/): it must not be taken for a failure now that a failure stops the retirement.
func TestArchiveAndRemoveSession_AMissingMessageFolderIsNotAFailure(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantStale(t, dataDir, f171SID)
	writeFileIn(t, sessionPath(dataDir, f171SID, "inbox", "msg-aaaaaaaaaaaa.json"), "content of inbox/msg-aaaaaaaaaaaa.json")
	for _, sub := range []string{"outbox", "processed"} {
		require.NoDirExists(t, sessionPath(dataDir, f171SID, sub), "premise: no %s/ in this session", sub)
	}

	err := archiveAndRemoveSession(dataDir, f171SID, fixedNow, nil)

	require.NoError(t, err)
	assert.FileExists(t, archiveCopy(dataDir, "inbox/msg-aaaaaaaaaaaa.json"))
	assert.False(t, strings.Contains(strings.Join(listTree(t, filepath.Join(dataDir, "archive")), "\n"), "outbox"),
		"and no archive folder is made for a folder that was not there")
}
