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
	// One scope, two working directories: the shape the v0.8 verbs need to
	// resolve a recipient by agent name.
	dirs := sharedScopeDirs(t, "val", "esc")
	projVal, projEsc := dirs[0], dirs[1]

	valID := registerIn(t, dataDir, projVal, "val", "VAL-int")
	require.NotEmpty(t, valID)
	escID := registerIn(t, dataDir, projEsc, "esc", "ESC-int")
	require.NotEmpty(t, escID)
	_ = projEsc

	// VAL sends 10 messages alternating the two outbound verbs. The verb now
	// carries the type (ask=query, tell=notify), so the old --type list is
	// expressed as an alternation; what this scenario asserts is that none of
	// the ten is lost, not which type each one had.
	verbs := []string{"ask", "tell", "ask", "tell", "ask", "tell", "ask", "ask", "tell", "tell"}
	sentIDs := make([]string, 0, len(verbs))
	for i, verb := range verbs {
		content := "msg-" + string(rune('a'+i))
		out, errOut, exit := runInDir(t, projVal, []string{verb, "ESC-int", content}, dataDirEnv(dataDir))
		require.Equal(t, 0, exit, "%s %d must succeed (stderr: %s)", verb, i, errOut)
		msgID := extractMsgID(out)
		require.True(t, strings.HasPrefix(msgID, "msg-"), "%s must echo the message ID; got %q", verb, out)
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
	statusOut, _, statusExit := run(t, []string{"status", "--session-id=" + escID}, dataDirEnv(dataDir))
	require.Equal(t, 0, statusExit)
	assert.Contains(t, statusOut, `"inboxCount": 10`, "status must report inboxCount=10")
}
