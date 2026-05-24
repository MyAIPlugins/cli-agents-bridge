package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScenario1_OneValOneEscRoundTrip implements PLAN §7.3 scenario 1:
// 1 VAL + 1 ESC round-trip 10 messages (baseline end-to-end smoke).
//
// We compress the scenario from a stateful long-running listen subprocess
// into a sequential drive-the-binary loop: the integration test calls
// `cab-bridge ask` from VAL ten times, each delivering a message to ESC's
// inbox. We then assert ESC's inbox contains all 10 (PollInbox-style
// consumption is exercised separately in transport/fs unit tests).
//
// This shape avoids spinning up a true second subprocess per message
// (which would dominate wall-clock with subprocess startup overhead) while
// still exercising the full ask→atomic-write→inbox path via the production
// binary.
func TestScenario1_OneValOneEscRoundTrip(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	projVal := t.TempDir()
	projEsc := t.TempDir()

	// Register VAL
	out, errOut, exit := run(t, []string{"register", "--role=val", "--agent-name=VAL-int", "--project-path=" + projVal}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "register VAL must succeed (stderr: %s)", errOut)
	valID := mustJSONField(t, out, "sessionId")
	require.NotEmpty(t, valID)

	// Register ESC
	out, errOut, exit = run(t, []string{"register", "--role=esc", "--agent-name=ESC-int", "--project-path=" + projEsc}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "register ESC must succeed (stderr: %s)", errOut)
	escID := mustJSONField(t, out, "sessionId")
	require.NotEmpty(t, escID)

	// VAL sends 10 messages alternating type=query / ping / notify
	types := []string{"query", "ping", "notify", "query", "ping", "notify", "query", "query", "ping", "notify"}
	sentIDs := make([]string, 0, len(types))
	for i, ty := range types {
		content := "msg-" + string(rune('a'+i))
		args := []string{
			"ask",
			"--to=" + escID,
			"--type=" + ty,
			"--content=" + content,
			"--session-id=" + valID,
		}
		out, errOut, exit = run(t, args, dataDirEnv(dataDir))
		require.Equal(t, 0, exit, "ask %d must succeed (stderr: %s)", i, errOut)
		msgID := strings.TrimSpace(out)
		require.True(t, strings.HasPrefix(msgID, "msg-"), "ask must emit message ID; got %q", msgID)
		sentIDs = append(sentIDs, msgID)
	}

	// Verify ESC's inbox contains all 10 messages
	escInbox := filepath.Join(dataDir, "sessions", escID, "inbox")
	entries, err := os.ReadDir(escInbox)
	require.NoError(t, err)
	gotJSON := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			gotJSON++
		}
	}
	assert.Equal(t, 10, gotJSON, "ESC inbox must contain all 10 dispatched messages")

	// status on ESC reports inboxCount=10
	out, _, exit = run(t, []string{"status", "--session-id=" + escID}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit)
	assert.Contains(t, out, `"inboxCount": 10`, "status must report inboxCount=10")
}
