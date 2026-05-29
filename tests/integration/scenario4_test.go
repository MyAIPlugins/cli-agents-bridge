package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScenario4_CleanupCrossProjectSafety implements PLAN §7.3 scenario 4:
// 3 projects (proj-a/b/c) registered, run cleanup my-session on proj-a,
// verify b+c untouched, archive/<date>/<proj-a-id>/ exists.
//
// This is the cmd-level mirror of bug4_test.go (which exercised the library
// path). Together they cover both layers of the BUG-4 fix.
func TestScenario4_CleanupCrossProjectSafety(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	register := func(name string) string {
		t.Helper()
		proj := t.TempDir()
		// Plant a processed file in advance so archive folder gets populated
		out, _, exit := run(t, []string{
			"register",
			"--role=val",
			"--agent-name=" + name,
			"--project-path=" + proj,
		}, dataDirEnv(dataDir))
		require.Equal(t, 0, exit, "register %s must succeed", name)
		return mustJSONField(t, out, "sessionId")
	}

	idA := register("proj-a")
	idB := register("proj-b")
	idC := register("proj-c")

	// Stage a processed file under proj-a so archive will receive it
	processedA := filepath.Join(dataDir, "sessions", idA, "processed")
	require.NoError(t, os.MkdirAll(processedA, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(processedA, "20260524T200000.000Z-msg-aaaaaaaaaaaa.json"),
		[]byte(`{"audit":true}`), 0o600))

	// Cleanup my-session targeting proj-a only
	out, errOut, exit := run(t, []string{
		"cleanup", "--session-id=" + idA,
	}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "cleanup my-session must succeed; stderr: %s", errOut)
	assert.Contains(t, out, idA, "result must report removed session")

	// proj-a gone, proj-b+c intact
	_, err := os.Stat(filepath.Join(dataDir, "sessions", idA))
	assert.True(t, os.IsNotExist(err), "BUG-4 fix: own session must be removed")

	for _, id := range []string{idB, idC} {
		_, err := os.Stat(filepath.Join(dataDir, "sessions", id))
		assert.NoError(t, err, "BUG-4 regression: other project session %s must survive", id)
	}

	// Archive populated with proj-a's processed content
	archRoot := filepath.Join(dataDir, "archive")
	dateDirs, err := os.ReadDir(archRoot)
	require.NoError(t, err)
	require.NotEmpty(t, dateDirs, "archive/ must contain a date dir")

	// Walk archive/<date>/<idA>/processed/ — must contain the staged processed
	// file (AUDIT-1: archive layout is per-subdir inbox/outbox/processed, not flat).
	archA := filepath.Join(archRoot, dateDirs[0].Name(), idA, "processed")
	archEntries, err := os.ReadDir(archA)
	require.NoError(t, err, "archive/<date>/<idA>/processed/ must exist")
	require.Len(t, archEntries, 1, "archive must contain exactly the staged processed file")
	assert.Contains(t, archEntries[0].Name(), "msg-aaaaaaaaaaaa",
		"archived filename preserves original message id")
}
