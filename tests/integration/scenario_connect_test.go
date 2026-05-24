package integration

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScenarioConnect_BUG9CmdLevel covers the cmd-level wiring of BUG-9:
// `cab-bridge connect <target>` must refresh sender's lastHeartbeat
// AND validate the role pair (BUG-3 enforcement carried over from ask).
//
// Test flow:
//  1. Register VAL with a backdated manifest (heartbeat 5 min ago).
//  2. Register ESC fresh.
//  3. From VAL, run `cab-bridge connect <ESC-id>`.
//  4. Assert VAL's manifest lastHeartbeat is now fresh (<5s old) — proves
//     Touch was invoked from the cmd path, not just library.
func TestScenarioConnect_BUG9CmdLevel(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	// Register VAL via subprocess (real cmd path)
	out, _, exit := run(t, []string{
		"register", "--role=val", "--agent-name=VAL-conn",
		"--project-path=" + t.TempDir(),
	}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit)
	valID := mustJSONField(t, out, "sessionId")

	out, _, exit = run(t, []string{
		"register", "--role=esc", "--agent-name=ESC-conn",
		"--project-path=" + t.TempDir(),
	}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit)
	escID := mustJSONField(t, out, "sessionId")

	// Backdate VAL's manifest by overwriting it with an old heartbeat.
	// We can't easily mock time inside a subprocess, so we tamper with
	// the on-disk manifest directly — this is the same shape the bug
	// would produce in production (a long-idle session).
	manifestPath := filepath.Join(dataDir, "sessions", valID, "manifest.json")
	backdateManifest(t, manifestPath, time.Now().Add(-5*time.Minute))

	// Run connect from VAL
	out, errOut, exit := run(t, []string{
		"connect", "--session-id=" + valID, escID,
	}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "connect must succeed: %s", errOut)
	assert.Contains(t, out, `"status": "connected"`)
	assert.Contains(t, out, escID, "target session ID must appear in report")
	assert.Contains(t, out, valID, "sender session ID must appear in report")

	// Verify VAL's heartbeat refreshed (BUG-9 cmd-level fix)
	freshTs := manifestHeartbeat(t, manifestPath)
	assert.WithinDuration(t, time.Now(), freshTs, 5*time.Second,
		"BUG-9 cmd-level regression: connect must Touch own heartbeat (got age=%v)",
		time.Since(freshTs))
}

// TestScenarioConnect_RoleViolation reuses the BUG-3 contract at the
// connect path: an observer must not be able to connect to anything as
// "sender" (observers are structural read-only sinks).
func TestScenarioConnect_RoleViolation(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	out, _, exit := run(t, []string{
		"register", "--role=observer", "--agent-name=OBS",
		"--project-path=" + t.TempDir(),
	}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit)
	obsID := mustJSONField(t, out, "sessionId")

	out, _, exit = run(t, []string{
		"register", "--role=val", "--agent-name=VAL",
		"--project-path=" + t.TempDir(),
	}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit)
	valID := mustJSONField(t, out, "sessionId")

	_, errOut, exit := run(t, []string{
		"connect", "--session-id=" + obsID, valID,
	}, dataDirEnv(dataDir))
	assert.NotEqual(t, 0, exit, "observer connect must fail structurally")
	assert.Contains(t, errOut, "observer", "error must mention the offending role")
}
