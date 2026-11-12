package cleanup

// F-171: a retirement that cannot archive a message stops before it deletes anything.
//
// The oracle is the one the defect is stated in: every message the session held must be
// somewhere afterwards, in the session or in the archive. "In neither" is the loss. The
// tests in this file need no seam, so they run against the code before the fix too, and
// that is where they are red.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

const f171SID = "f1710001"

// f171Messages are the four messages every F-171 test plants, one per place a message
// can sit, with two in inbox/ so a failure on the second one leaves a prefix moved.
var f171Messages = []string{
	"inbox/msg-aaaaaaaaaaaa.json",
	"inbox/msg-bbbbbbbbbbbb.json",
	"outbox/msg-cccccccccccc.json",
	"processed/msg-dddddddddddd.json",
}

func plantF171(t *testing.T, dataDir string) {
	t.Helper()
	plantStale(t, dataDir, f171SID)
	for _, rel := range f171Messages {
		writeFileIn(t, sessionPath(dataDir, f171SID, filepath.FromSlash(rel)), "content of "+rel)
	}
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func sessionCopy(dataDir, rel string) string {
	return sessionPath(dataDir, f171SID, filepath.FromSlash(rel))
}

func archiveCopy(dataDir, rel string) string {
	return filepath.Join(dataDir, "archive", archiveDate, f171SID, filepath.FromSlash(rel))
}

// requireEveryMessageIsSomewhere is the oracle of the defect: a message in neither
// place is gone, and no error said so. Where it is, its content must be intact.
func requireEveryMessageIsSomewhere(t *testing.T, dataDir string) {
	t.Helper()
	for _, rel := range f171Messages {
		where := ""
		switch {
		case pathExists(sessionCopy(dataDir, rel)):
			where = sessionCopy(dataDir, rel)
		case pathExists(archiveCopy(dataDir, rel)):
			where = archiveCopy(dataDir, rel)
		}
		if !assert.NotEmpty(t, where, "%s is in neither the session nor the archive: that is the loss F-171 is about", rel) {
			continue
		}
		got, err := os.ReadFile(where)
		require.NoError(t, err)
		assert.Equal(t, "content of "+rel, string(got), "%s must be intact wherever it is", rel)
	}
}

// requireSessionIntact is what "the retirement stopped before it retired anything" means
// on disk: the manifest, the lock file and the folder are all still there.
func requireSessionIntact(t *testing.T, dataDir string) {
	t.Helper()
	assert.FileExists(t, sessionPath(dataDir, f171SID, "manifest.json"), "the manifest is what keeps the session collectable")
	assert.FileExists(t, sessionPath(dataDir, f171SID, session.LockFileName))
}

// requireNothingMoved: every message is still in the session and none is in the archive.
func requireNothingMoved(t *testing.T, dataDir string) {
	t.Helper()
	for _, rel := range f171Messages {
		assert.FileExists(t, sessionCopy(dataDir, rel), "%s must not have been moved", rel)
		assert.False(t, pathExists(archiveCopy(dataDir, rel)), "%s must not be in the archive", rel)
	}
}

// requireNoFileInArchive: an EMPTY archive folder may be left by a pre-flight that got as
// far as creating it (archiveMessages says so), a file may not.
func requireNoFileInArchive(t *testing.T, dataDir string) {
	t.Helper()
	root := filepath.Join(dataDir, "archive")
	if !pathExists(root) {
		return
	}
	require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		assert.True(t, d.IsDir(), "a file reached the archive: %s", p)
		return nil
	}))
}

// plantUnexpectedDir puts a directory with a file inside a message folder, which no code
// of this project ever creates.
func plantUnexpectedDir(t *testing.T, dataDir string) string {
	t.Helper()
	odd := sessionPath(dataDir, f171SID, "inbox", "unexpected-dir")
	writeFileIn(t, filepath.Join(odd, "x.json"), "nested")
	return odd
}

func TestArchiveAndRemoveSession_ASubdirectoryInAMessageFolderStopsTheRetirementAndIsNamed(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantF171(t, dataDir)
	odd := plantUnexpectedDir(t, dataDir)

	err := archiveAndRemoveSession(dataDir, f171SID, fixedNow, nil)

	requireEveryMessageIsSomewhere(t, dataDir)
	assert.FileExists(t, filepath.Join(odd, "x.json"), "the file inside is still there")
	requireNothingMoved(t, dataDir) // the pre-flight stops before the first move, so the regular messages stay too
	requireSessionIntact(t, dataDir)
	require.Error(t, err, "a directory that cannot be archived must not be deleted in silence")
	assert.Contains(t, err.Error(), odd, "the error names the directory")
	assert.Contains(t, err.Error(), "by hand", "and says what to do about it")
}

func TestRun_MySession_ARetirementThatCannotArchiveIsAnErrorAndTheSessionStays(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantF171(t, dataDir)
	odd := plantUnexpectedDir(t, dataDir)

	res, err := Run(context.Background(), Options{
		DataDir: dataDir, Scope: ScopeMySession, OwnSessionID: f171SID,
		Now: func() time.Time { return failuresBase },
	})

	requireEveryMessageIsSomewhere(t, dataDir)
	assert.FileExists(t, filepath.Join(odd, "x.json"))
	requireSessionIntact(t, dataDir)
	requireNothingMoved(t, dataDir)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), odd)
}

func TestRun_Global_ARetirementThatCannotArchiveIsAFailureNamingTheFile(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantF171(t, dataDir)
	odd := plantUnexpectedDir(t, dataDir)

	res, err := Run(context.Background(), sweepOpts(dataDir))

	requireEveryMessageIsSomewhere(t, dataDir)
	assert.FileExists(t, filepath.Join(odd, "x.json"))
	requireSessionIntact(t, dataDir)
	requireNothingMoved(t, dataDir)
	require.NoError(t, err, "one session that cannot be retired does not abort the sweep")
	assert.Empty(t, res.SessionsRemoved)
	require.Len(t, res.SessionsFailed, 1, "and it is reported, with the reason")
	assert.Equal(t, f171SID, res.SessionsFailed[0].ID)
	assert.Contains(t, res.SessionsFailed[0].Error, odd)
}
