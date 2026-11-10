package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
)

// F-160 C2: GCOrphans now returns the orphans it removed TOGETHER with the
// failures. runAutoGC used to drop the list whenever there was an error, which
// hid that some orphans had been swept while others had not.
func TestRunAutoGC_APartialFailureStillReportsWhatWasRemoved(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX directory permissions and a non-root user")
	}
	dataDir := t.TempDir()
	old := time.Now().UTC().Add(-48 * time.Hour)
	plantSession(t, dataDir, "orphan01", deadPID, old)
	plantSession(t, dataDir, "orphan02", deadPID, old)
	failing := filepath.Join(dataDir, "sessions", "orphan02")
	require.NoError(t, os.Chmod(failing, 0o500))
	t.Cleanup(func() {
		if err := os.Chmod(failing, 0o700); err != nil {
			t.Logf("restore permissions of %s: %v", failing, err)
		}
	})

	var buf bytes.Buffer
	removed := runAutoGC(config.Config{DataDir: dataDir, AutoGCHours: 24}, "", &buf)

	require.Len(t, removed, 1)
	assert.Equal(t, "orphan01", removed[0].SessionID)
	log := buf.String()
	assert.Contains(t, log, "auto-gc finished with errors")
	assert.Contains(t, log, "orphan02", "the failing session must be named")
	assert.Contains(t, log, "auto-gc removed orphan session orphan01", "the removal is still reported")
	assert.True(t, sessionExists(dataDir, "orphan02"))
}
