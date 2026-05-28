package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMigrate_SessionIDDivergenceRejected covers FINDING-4 / NEW-1: a Patil
// manifest whose internal sessionId differs from its directory name must be
// rejected (skippedInvalid), never migrated. Otherwise an incoherent manifest
// (dir name != sessionId) would land in the v2 namespace and its sessionId
// could later be propagated as an unvalidated path component.
func TestMigrate_SessionIDDivergenceRejected(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	patilDir := filepath.Join(t.TempDir(), "fake-patil")

	// Directory name "diverge1" (SC-4 valid) but manifest claims "otherid9".
	base := filepath.Join(patilDir, "sessions", "diverge1")
	require.NoError(t, os.MkdirAll(filepath.Join(base, "inbox"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(base, "outbox"), 0o755))
	mf := map[string]interface{}{
		"sessionId":     "otherid9",
		"schemaVersion": 1,
		"projectName":   "legacy",
		"projectPath":   "/Users/alan/develop/legacy-x",
		"startedAt":     "2026-01-15T10:00:00Z",
		"lastHeartbeat": "2026-01-15T10:00:00Z",
		"status":        "active",
	}
	data, _ := json.MarshalIndent(mf, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(base, "manifest.json"), data, 0o600))

	out, errOut, exit := run(t, []string{
		"migrate-from-patil",
		"--patil-dir=" + patilDir,
	}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "migrate must exit 0 even when skipping invalid: %s", errOut)

	// Must not be migrated under either identity.
	_, err := os.Stat(filepath.Join(dataDir, "sessions", "diverge1"))
	assert.True(t, os.IsNotExist(err), "divergent session must not be migrated under its dir name")
	_, err = os.Stat(filepath.Join(dataDir, "sessions", "otherid9"))
	assert.True(t, os.IsNotExist(err), "divergent session must not be migrated under its manifest sessionId")

	assert.Contains(t, out, "diverge1", "report must mention the rejected session")
	assert.Contains(t, out, "!= dir name", "skip reason must surface the sessionId/dir divergence")
}

// TestMigrate_CoherentManifestStillMigrates is the positive control: a manifest
// whose sessionId matches its dir name migrates normally and the written v2
// manifest passes validation (NEW-2 path does not reject valid input).
func TestMigrate_CoherentManifestStillMigrates(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	patilDir := filepath.Join(t.TempDir(), "fake-patil")

	base := filepath.Join(patilDir, "sessions", "coher001")
	require.NoError(t, os.MkdirAll(filepath.Join(base, "inbox"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(base, "outbox"), 0o755))
	mf := map[string]interface{}{
		"sessionId":     "coher001",
		"schemaVersion": 1,
		"projectName":   "legacy",
		"projectPath":   "/Users/alan/develop/legacy-coherent",
		"startedAt":     "2026-01-15T10:00:00Z",
		"lastHeartbeat": "2026-01-15T10:00:00Z",
		"status":        "active",
	}
	data, _ := json.MarshalIndent(mf, "", "  ")
	require.NoError(t, os.WriteFile(filepath.Join(base, "manifest.json"), data, 0o600))

	_, errOut, exit := run(t, []string{
		"migrate-from-patil",
		"--patil-dir=" + patilDir,
	}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "coherent migrate must exit 0: %s", errOut)

	mfPath := filepath.Join(dataDir, "sessions", "coher001", "manifest.json")
	written, err := os.ReadFile(mfPath)
	require.NoError(t, err, "coherent session must be migrated")
	var got map[string]interface{}
	require.NoError(t, json.Unmarshal(written, &got))
	assert.Equal(t, "coher001", got["sessionId"])
	assert.Equal(t, float64(2), got["schemaVersion"], "migrated manifest must be v2")
}
