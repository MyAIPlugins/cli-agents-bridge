package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBUGB_PeersEmptyJSONIsArray: `peers --json` with no sessions must emit []
// (not null), so a `jq '. | length'` consumer does not break.
func TestBUGB_PeersEmptyJSONIsArray(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	out, errOut, exit := run(t, []string{"peers", "--json"}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "peers --json must succeed on empty data dir: %s", errOut)
	assert.Equal(t, "[]", strings.TrimSpace(out), "empty peers --json must be [] not null")
}

// TestBUGB_MigrateReportSlicesAreArrays: the migrate report must serialize its
// empty slice buckets as [] (not null).
func TestBUGB_MigrateReportSlicesAreArrays(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	patilDir := filepath.Join(t.TempDir(), "fake-patil")
	require.NoError(t, os.MkdirAll(filepath.Join(patilDir, "sessions"), 0o755))

	out, errOut, exit := run(t, []string{
		"migrate-from-patil", "--patil-dir=" + patilDir, "--dry-run",
	}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "migrate dry-run must succeed: %s", errOut)
	assert.NotContains(t, out, "null", "no migrate report slice may be null: %s", out)
	assert.Contains(t, out, `"migrated": []`, "empty migrated bucket must be [] not null")
}
