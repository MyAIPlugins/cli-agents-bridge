package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countJSONInbox counts *.json files under dir (an inbox or processed dir),
// skipping the .tmp.* atomic-write leftovers. Used to assert how many messages
// a --wait-one sweep consumed.
func countJSONInbox(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".tmp.") || !strings.HasSuffix(name, ".json") {
			continue
		}
		n++
	}
	return n
}

// registerPair registers a VAL and an ESC against dataDir and returns their IDs.
func registerPair(t *testing.T, dataDir, suffix string) (valID, escID string) {
	t.Helper()
	out, errOut, exit := run(t, []string{"register", "--role=val", "--agent-name=VAL-" + suffix, "--project-path=" + t.TempDir()}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "register VAL: %s", errOut)
	valID = mustJSONField(t, out, "sessionId")

	out, errOut, exit = run(t, []string{"register", "--role=esc", "--agent-name=ESC-" + suffix, "--project-path=" + t.TempDir()}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "register ESC: %s", errOut)
	escID = mustJSONField(t, out, "sessionId")
	return valID, escID
}

// TestScenarioWaitOne_SingleMessage_ExitsZero is the F-10 happy path via the
// real binary (LL-10). One message is already in the ESC inbox; `listen
// --wait-one` must consume it, print it, auto-ack the sender, and exit 0 — the
// behaviour that lets a run-in-background caller wake the instant a message
// arrives. Because --wait-one terminates on its own, run() (which blocks on the
// subprocess) returns without any Kill.
func TestScenarioWaitOne_SingleMessage_ExitsZero(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	valID, escID := registerPair(t, dataDir, "wo1")

	out, errOut, exit := run(t, []string{"ask", "--session-id=" + valID, "--to=" + escID, "--type=query", "--content=brief"}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "ask: %s", errOut)
	queryID := strings.TrimSpace(out)
	require.True(t, strings.HasPrefix(queryID, "msg-"))

	stdout, errOut, exit := run(t, []string{"listen", "--wait-one", "--session-id=" + escID},
		dataDirEnv(dataDir, "CAB_POLL_INTERVAL_MS=50"))
	require.Equal(t, 0, exit, "listen --wait-one must exit 0 after delivering a message; stderr: %s", errOut)
	assert.Contains(t, stdout, queryID, "the consumed message must be printed on stdout")

	// Message moved out of inbox into processed/.
	assert.Equal(t, 0, countJSONInbox(t, filepath.Join(dataDir, "sessions", escID, "inbox")), "inbox must be drained")
	assert.Equal(t, 1, countJSONInbox(t, filepath.Join(dataDir, "sessions", escID, "processed")), "message must be in processed/")

	// F-12: auto-ack reached the sender before exit.
	assert.NotNil(t, findAckInInbox(t, filepath.Join(dataDir, "sessions", valID, "inbox"), queryID),
		"VAL must have received the auto-ack before --wait-one exited")
}

// TestScenarioWaitOne_TwoMessages_NoneLost is the CORE F-10 regression: with two
// messages present in the same sweep, --wait-one must deliver BOTH and lose
// none. A naive "return after the first message" implementation over the
// move-before-emit channel poller would drop the second (moved to processed/ but
// never printed). This is the test the whole design exists to pass.
func TestScenarioWaitOne_TwoMessages_NoneLost(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	valID, escID := registerPair(t, dataDir, "wo2")

	out, errOut, exit := run(t, []string{"ask", "--session-id=" + valID, "--to=" + escID, "--type=query", "--content=first"}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "ask 1: %s", errOut)
	id1 := strings.TrimSpace(out)

	out, errOut, exit = run(t, []string{"ask", "--session-id=" + valID, "--to=" + escID, "--type=query", "--content=second"}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "ask 2: %s", errOut)
	id2 := strings.TrimSpace(out)
	require.NotEqual(t, id1, id2)

	stdout, errOut, exit := run(t, []string{"listen", "--wait-one", "--session-id=" + escID},
		dataDirEnv(dataDir, "CAB_POLL_INTERVAL_MS=50"))
	require.Equal(t, 0, exit, "listen --wait-one must exit 0; stderr: %s", errOut)

	assert.Contains(t, stdout, id1, "first message must be printed — no loss")
	assert.Contains(t, stdout, id2, "second message must be printed — no loss")

	assert.Equal(t, 0, countJSONInbox(t, filepath.Join(dataDir, "sessions", escID, "inbox")), "inbox must be fully drained")
	assert.Equal(t, 2, countJSONInbox(t, filepath.Join(dataDir, "sessions", escID, "processed")), "both messages must be in processed/")
}

// TestScenarioWaitOne_NoAutoAck_SuppressesReceipt verifies --wait-one composes
// with --no-auto-ack (DV-2): the message is still delivered and consumed, but no
// receipt is sent to the sender.
func TestScenarioWaitOne_NoAutoAck_SuppressesReceipt(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	valID, escID := registerPair(t, dataDir, "wona")

	out, errOut, exit := run(t, []string{"ask", "--session-id=" + valID, "--to=" + escID, "--type=query", "--content=brief"}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "ask: %s", errOut)
	queryID := strings.TrimSpace(out)

	stdout, errOut, exit := run(t, []string{"listen", "--wait-one", "--no-auto-ack", "--session-id=" + escID},
		dataDirEnv(dataDir, "CAB_POLL_INTERVAL_MS=50"))
	require.Equal(t, 0, exit, "listen --wait-one --no-auto-ack must exit 0; stderr: %s", errOut)
	assert.Contains(t, stdout, queryID, "message must still be delivered under --no-auto-ack")

	// The message was consumed (drained) but no receipt was sent.
	assert.Equal(t, 1, countJSONInbox(t, filepath.Join(dataDir, "sessions", escID, "processed")), "message must still be consumed")
	assert.Nil(t, findAckInInbox(t, filepath.Join(dataDir, "sessions", valID, "inbox"), queryID),
		"--no-auto-ack must suppress the delivery receipt")
}

// TestScenarioWaitOne_NoMessage_TimesOutPayloadExit0 verifies the F-24 change:
// an empty-inbox --wait-one window that expires is a valid result, not a
// failure. It now exits 0 with a {"status":"timeout","messages":[]} payload on
// stdout, so a run-in-background harness reads success (not "command failed")
// every idle cycle and tells a timeout from a delivered batch by the payload.
func TestScenarioWaitOne_NoMessage_TimesOutPayloadExit0(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	_, escID := registerPair(t, dataDir, "wot")

	stdout, errOut, exit := run(t, []string{"listen", "--wait-one", "--session-id=" + escID},
		dataDirEnv(dataDir, "CAB_POLL_INTERVAL_MS=50", "CAB_MAX_BLOCKING_SECONDS=1"))
	require.Equal(t, 0, exit, "empty inbox + timeout must now exit 0; stderr: %s", errOut)
	assert.Contains(t, stdout, `"status"`, "timeout must emit a status payload on stdout")
	assert.Contains(t, stdout, "timeout", "payload status must be timeout")
}
