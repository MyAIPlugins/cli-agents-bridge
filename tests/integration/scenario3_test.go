package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScenario3_LongRunHeartbeatPersistence implements PLAN §7.3 scenario 3:
// 30-min listen idle, heartbeat must stay <90s for the whole duration.
//
// Wall-clock compression: we use CAB_HEARTBEAT_TICK_MS=50 (vs production
// 30000 = 30s) and CAB_STALE_SECONDS=10. With those overrides the test
// runs in ~2s while still exercising the structural property: while the
// listen subprocess is alive, its lastHeartbeat is ALWAYS younger than
// StaleSeconds.
//
// We spawn a real cab-bridge listen subprocess, wait 1.5s, sample manifest
// 3 times, then kill the listen and assert all samples are within the
// staleness window.
func TestScenario3_LongRunHeartbeatPersistence(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	proj := t.TempDir()

	out, errOut, exit := run(t, []string{
		"register",
		"--role=esc",
		"--agent-name=ESC-hb",
		"--project-path=" + proj,
	}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "register must succeed: %s", errOut)
	sid := mustJSONField(t, out, "sessionId")

	// Spawn listen subprocess with compressed clocks
	bin := buildBinary(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "listen", "--session-id="+sid)
	cmd.Env = append(os.Environ(),
		"CAB_DATA_DIR="+dataDir,
		"CAB_HEARTBEAT_TICK_MS=50",
		"CAB_STALE_SECONDS=2",
		"CAB_POLL_INTERVAL_MS=100",
		"CAB_MAX_BLOCKING_SECONDS=10",
	)
	// listen prints to stdout when messages arrive; we don't care, swallow
	cmd.Stdout = nil
	cmd.Stderr = nil
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// Wait for at least 5 heartbeat ticks (~250ms with 50ms tick)
	time.Sleep(300 * time.Millisecond)

	manifestPath := filepath.Join(dataDir, "sessions", sid, "manifest.json")

	// Sample 3 times across 500ms — every sample must show heartbeat within 1s
	for i := 0; i < 3; i++ {
		info, err := os.Stat(manifestPath)
		require.NoError(t, err)
		mtimeAge := time.Since(info.ModTime())
		assert.Less(t, mtimeAge, time.Second,
			"sample %d: manifest mtime age %v exceeds 1s — heartbeat goroutine not ticking", i, mtimeAge)
		time.Sleep(150 * time.Millisecond)
	}
}
