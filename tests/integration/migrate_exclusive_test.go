package integration

// F-160, CRI diff-gate round 2, blocker R2-1: migrate published the manifest of a
// session into a folder that already existed. Two windows, one root. After a
// complete retirement the Patil source can be imported again under the SAME id, and a
// process that had opened the old lock file locked the old inode next to the holder of
// the new one. And in the middle of a retirement, with the manifest and the marker
// already gone and the folder still there, migrate succeeded and left the retirement
// unable to finish.
//
// The lock protocol rests on "the manifest of a retired session does not come back".
// migrate now creates the folder of the session exclusively, like Register does, so a
// folder that exists is never written into. The binary is the real one; the
// retirement in the last test is cleanup.Run on a temporary directory (the library, not
// the cab-bridge cleanup command).
//
// Not covered here, and why: an importer inside the cleanup seam afterRelease, and a
// waiter stopped between open and lock, need seams that are unexported in other
// packages. The first half of each is covered where the seam lives (cleanup:
// TestRetirement_ASetStateAfterTheRetirementIsRefusedAndCreatesNothing; session:
// TestAcquire_AWaiterOnAReplacedFileDoesNotShareTheLockWithItsNewHolder), the second
// half is this file: the importer is refused, whatever the moment.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/cleanup"
	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

const migrateSID = "legacy01"

type migrateReportOut struct {
	Migrated        []string `json:"migrated"`
	SkippedExisting []string `json:"skippedExisting"`
	Errors          []string `json:"errors"`
}

// plantPatilSession writes a valid Patil session under patilDir and returns it.
func plantPatilSession(t *testing.T, patilDir string) {
	t.Helper()
	dir := filepath.Join(patilDir, "sessions", migrateSID)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "inbox"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "outbox"), 0o755))
	manifest := map[string]interface{}{
		"sessionId":     migrateSID,
		"schemaVersion": 1,
		"projectName":   "legacy",
		"projectPath":   "/Users/alan/develop/legacy-reimport",
		"startedAt":     "2026-01-15T10:00:00Z",
		"lastHeartbeat": "2026-01-15T10:00:00Z",
		"status":        "active",
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o600))
}

func runMigrateBinary(t *testing.T, dataDir, patilDir string) migrateReportOut {
	t.Helper()
	out, errOut, exit := run(t, []string{"migrate-from-patil", "--patil-dir=" + patilDir}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "migrate reports per-session failures in its report and exits 0: %s", errOut)
	var report migrateReportOut
	require.NoError(t, json.Unmarshal([]byte(out), &report), "the report is JSON: %s", out)
	return report
}

// treeDigest lists every entry under dir with the hash of each file, so "nothing was
// written" is a comparison and not a feeling.
func treeDigest(t *testing.T, dir string) map[string]string {
	t.Helper()
	digest := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(dir, p)
		require.NoError(t, rerr)
		if d.IsDir() {
			digest[rel+"/"] = "dir"
			return nil
		}
		data, rerr := os.ReadFile(p)
		require.NoError(t, rerr)
		sum := sha256.Sum256(data)
		digest[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	require.NoError(t, err)
	return digest
}

func TestMigrate_NeverWritesIntoAFolderThatAlreadyExistsWithoutItsMarker(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		plant func(t *testing.T, dir string)
	}{
		{"a retirement in progress: the lock file is all that is left", func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, session.LockFileName), nil, 0o600))
		}},
		{"a live session: its own manifest and inbox", func(t *testing.T, dir string) {
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "inbox"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"sessionId":"legacy01","note":"alive"}`), 0o600))
		}},
		{"an earlier import that was interrupted before it left its marker", func(t *testing.T, dir string) {
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "inbox"), 0o700))
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "outbox"), 0o700))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dataDir, patilDir := t.TempDir(), t.TempDir()
			plantPatilSession(t, patilDir)
			folder := filepath.Join(dataDir, config.LayoutDir, "sessions", migrateSID)
			require.NoError(t, os.MkdirAll(folder, 0o700))
			tc.plant(t, folder)
			before := treeDigest(t, folder)

			report := runMigrateBinary(t, dataDir, patilDir)

			require.Len(t, report.Errors, 1, "the refusal is reported: %+v", report)
			assert.Contains(t, report.Errors[0], migrateSID)
			assert.Contains(t, report.Errors[0], "already exists")
			assert.Contains(t, report.Errors[0], "nothing was written")
			assert.Empty(t, report.Migrated, "and it is not reported as imported")
			assert.Equal(t, before, treeDigest(t, folder), "not one byte of the folder changed")
			assert.NoFileExists(t, filepath.Join(folder, ".migrated"))
		})
	}
}

// The idempotence that was always declared stays: a folder with the marker is skipped,
// not an error, and nothing in it is touched.
func TestMigrate_AFolderMarkedAsMigratedIsStillSkipped(t *testing.T) {
	t.Parallel()
	dataDir, patilDir := t.TempDir(), t.TempDir()
	plantPatilSession(t, patilDir)
	first := runMigrateBinary(t, dataDir, patilDir)
	require.Equal(t, []string{migrateSID}, first.Migrated, "premise: the first import worked")
	folder := filepath.Join(dataDir, config.LayoutDir, "sessions", migrateSID)
	before := treeDigest(t, folder)

	second := runMigrateBinary(t, dataDir, patilDir)

	assert.Equal(t, []string{migrateSID}, second.SkippedExisting)
	assert.Empty(t, second.Errors)
	assert.Empty(t, second.Migrated)
	assert.Equal(t, before, treeDigest(t, folder))
}

// The positive control: refusing to write into an existing folder must not forbid the
// legitimate case. Once cleanup has retired the session completely there is no folder,
// and the same Patil session can be imported again; the new session works and is
// locked like any other.
func TestMigrate_ASessionThatCleanupRetiredCompletelyCanBeImportedAgain(t *testing.T) {
	t.Parallel()
	dataDir, patilDir := t.TempDir(), t.TempDir()
	plantPatilSession(t, patilDir)
	require.Equal(t, []string{migrateSID}, runMigrateBinary(t, dataDir, patilDir).Migrated)
	state := filepath.Join(dataDir, config.LayoutDir)
	folder := filepath.Join(state, "sessions", migrateSID)

	_, err := cleanup.Run(t.Context(), cleanup.Options{DataDir: state, Scope: cleanup.ScopeMySession, OwnSessionID: migrateSID})
	require.NoError(t, err, "premise: the retirement went through")
	require.NoDirExists(t, folder, "premise: and left nothing")

	again := runMigrateBinary(t, dataDir, patilDir)

	assert.Equal(t, []string{migrateSID}, again.Migrated)
	assert.Empty(t, again.Errors)
	assert.FileExists(t, filepath.Join(folder, "manifest.json"))
	assert.FileExists(t, filepath.Join(folder, ".migrated"))
	release, err := session.NewManager(state, time.Second).AcquireSessionLock(migrateSID, 0)
	require.NoError(t, err, "the imported session can be locked")
	require.NoError(t, release())
}
