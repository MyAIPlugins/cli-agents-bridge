package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScenario5_MigrateFromPatil implements PLAN §7.3 scenario 5:
// stage a fake ~/.claude/session-bridge/ tree with 3 Patil v1 sessions,
// run migrate-from-patil, verify v2 manifests + idempotency + RC-3
// path-traversal rejection.
//
// Uses the --patil-dir override flag (added in cmd/cab-bridge/migrate.go
// specifically to make this test self-contained without touching real $HOME).
func TestScenario5_MigrateFromPatil(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	patilDir := filepath.Join(t.TempDir(), "fake-patil")

	// Stage 3 valid v1 sessions + 1 RC-3 traversal attempt
	stageV1 := func(sid string, projectPath string, inboxMessages []string) {
		t.Helper()
		base := filepath.Join(patilDir, "sessions", sid)
		require.NoError(t, os.MkdirAll(filepath.Join(base, "inbox"), 0o755))
		require.NoError(t, os.MkdirAll(filepath.Join(base, "outbox"), 0o755))

		v1Manifest := map[string]interface{}{
			"sessionId":     sid,
			"schemaVersion": 1,
			"projectName":   filepath.Base(projectPath),
			"projectPath":   projectPath,
			"startedAt":     "2026-01-15T10:00:00Z",
			"lastHeartbeat": "2026-01-15T10:00:00Z",
			"status":        "active",
		}
		data, _ := json.MarshalIndent(v1Manifest, "", "  ")
		require.NoError(t, os.WriteFile(filepath.Join(base, "manifest.json"), data, 0o600))

		for _, mid := range inboxMessages {
			require.NoError(t, os.WriteFile(
				filepath.Join(base, "inbox", mid+".json"),
				[]byte(`{"id":"`+mid+`","content":"legacy"}`),
				0o600))
		}
	}

	stageV1("legac001", "/Users/alan/develop/legacy-a", []string{"msg-legacymsg001", "msg-legacymsg002"})
	stageV1("legac002", "/Users/alan/develop/legacy-b", []string{"msg-legacymsg003"})
	stageV1("legac003", "/Users/alan/develop/legacy-c", nil)
	// RC-3: path traversal attempt in corrupted manifest
	stageV1("evilss1", "../../etc/passwd", nil)

	// Run 1: dry-run (no writes to target)
	out, errOut, exit := run(t, []string{
		"migrate-from-patil",
		"--patil-dir=" + patilDir,
		"--dry-run",
	}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "dry-run must exit 0: %s", errOut)
	assert.Contains(t, out, "legac001 (dry-run)", "dry-run output must enumerate planned sessions")
	assert.Contains(t, out, "evilss1", "dry-run must mention rejected RC-3 traversal session")

	// Verify NO target writes happened (dry-run)
	_, err := os.Stat(filepath.Join(dataDir, "sessions", "legac001"))
	assert.True(t, os.IsNotExist(err), "dry-run must NOT write to target")

	// Run 2: real migration
	out, errOut, exit = run(t, []string{
		"migrate-from-patil",
		"--patil-dir=" + patilDir,
	}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "migration must exit 0: %s", errOut)

	// Verify 3 legitimate sessions migrated
	for _, sid := range []string{"legac001", "legac002", "legac003"} {
		mfPath := filepath.Join(dataDir, "sessions", sid, "manifest.json")
		_, err := os.Stat(mfPath)
		require.NoError(t, err, "session %s manifest must exist post-migration", sid)

		data, err := os.ReadFile(mfPath)
		require.NoError(t, err)
		var mf map[string]interface{}
		require.NoError(t, json.Unmarshal(data, &mf))

		assert.Equal(t, float64(2), mf["schemaVersion"], "session %s schemaVersion must be 2 after migration", sid)
		assert.Equal(t, "neutral", mf["role"], "session %s role must default to neutral (v1 backward-compat)", sid)
	}

	// RC-3: traversal session must NOT have been migrated
	_, err = os.Stat(filepath.Join(dataDir, "sessions", "evilss1"))
	assert.True(t, os.IsNotExist(err),
		"RC-3 security: session with projectPath containing '..' must be REJECTED, not migrated")
	assert.Contains(t, out, "evilss1", "result must report skippedInvalid for traversal attempt")
	assert.Contains(t, out, "contains '..'", "skip reason must surface the path-traversal rejection")

	// Verify .migrated marker present
	for _, sid := range []string{"legac001", "legac002", "legac003"} {
		_, err := os.Stat(filepath.Join(dataDir, "sessions", sid, ".migrated"))
		assert.NoError(t, err, "session %s must have .migrated marker for idempotency", sid)
	}

	// Verify backup folder created
	entries, err := os.ReadDir(dataDir)
	require.NoError(t, err)
	foundBackup := false
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "migration-backup-") {
			foundBackup = true
			break
		}
	}
	assert.True(t, foundBackup, "migration must create a backup folder")

	// Run 3: idempotent re-run (must skip all sessions)
	out, errOut, exit = run(t, []string{
		"migrate-from-patil",
		"--patil-dir=" + patilDir,
	}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "re-run must exit 0 (idempotent): %s", errOut)

	// All 3 legitimate sessions should appear in skippedExisting
	for _, sid := range []string{"legac001", "legac002", "legac003"} {
		assert.Contains(t, out, sid, "re-run must report session %s as skippedExisting", sid)
	}
}
