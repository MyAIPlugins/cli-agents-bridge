package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type peerView struct {
	SessionID         string `json:"sessionId"`
	Role              string `json:"role"`
	InboxCount        int    `json:"inboxCount"`
	LastConsumedMsgID string `json:"lastConsumedMsgId"`
}

type statusView struct {
	InboxCount        int    `json:"inboxCount"`
	LastConsumedMsgID string `json:"lastConsumedMsgId"`
}

func peersByID(t *testing.T, dataDir string) map[string]peerView {
	t.Helper()
	out, errOut, exit := run(t, []string{"peers", "--json"}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "peers --json must succeed: %s", errOut)
	var list []peerView
	require.NoError(t, json.Unmarshal([]byte(out), &list))
	m := make(map[string]peerView, len(list))
	for _, p := range list {
		m[p.SessionID] = p
	}
	return m
}

func statusOf(t *testing.T, dataDir, sessionID string) statusView {
	t.Helper()
	out, errOut, exit := run(t, []string{"status", "--session-id=" + sessionID}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "status must succeed: %s", errOut)
	var sv statusView
	require.NoError(t, json.Unmarshal([]byte(out), &sv))
	return sv
}

// TestScenarioObserve_PeersAndStatusExposeInboxAndLastConsumed is the F-12
// deliverable 3 surface check: peers and status must expose inboxCount and
// lastConsumedMsgId so an orchestrator can read task-state from the CLI. Driven
// via the real binary with one query (deterministic lastConsumedMsgId).
func TestScenarioObserve_PeersAndStatusExposeInboxAndLastConsumed(t *testing.T) {
	// Not parallel: starts/kills a listen subprocess.
	dataDir := t.TempDir()
	bin := buildBinary(t)

	out, errOut, exit := run(t, []string{"register", "--role=val", "--agent-name=VAL-ob", "--project-path=" + t.TempDir()}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "register VAL: %s", errOut)
	valID := mustJSONField(t, out, "sessionId")

	out, errOut, exit = run(t, []string{"register", "--role=esc", "--agent-name=ESC-ob", "--project-path=" + t.TempDir()}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "register ESC: %s", errOut)
	escID := mustJSONField(t, out, "sessionId")

	out, errOut, exit = run(t, []string{"ask", "--session-id=" + valID, "--to=" + escID, "--type=query", "--content=brief"}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "ask: %s", errOut)
	queryID := trimSpaceLocal(out)

	// Before consumption: ESC has 1 pending, no lastConsumed.
	st := statusOf(t, dataDir, escID)
	assert.Equal(t, 1, st.InboxCount, "status inboxCount must be 1 before consume")
	assert.Empty(t, st.LastConsumedMsgID, "no lastConsumedMsgId before consume")

	peers := peersByID(t, dataDir)
	require.Contains(t, peers, escID)
	assert.Equal(t, 1, peers[escID].InboxCount, "peers inboxCount must be 1 before consume")
	assert.Empty(t, peers[escID].LastConsumedMsgID)

	// Consume via a brief listen (--no-auto-ack to keep VAL's inbox clean).
	listenCmd := exec.Command(bin, "listen", "--session-id="+escID, "--no-auto-ack")
	listenCmd.Env = append(os.Environ(), dataDirEnv(dataDir)...)
	require.NoError(t, listenCmd.Start())
	t.Cleanup(func() {
		_ = listenCmd.Process.Kill()
		_, _ = listenCmd.Process.Wait()
	})

	require.Eventually(t, func() bool {
		return statusOf(t, dataDir, escID).LastConsumedMsgID == queryID
	}, 5*time.Second, 50*time.Millisecond, "status must report lastConsumedMsgId after consume")

	// After consumption: inbox drained to 0, lastConsumed set, in both views.
	st = statusOf(t, dataDir, escID)
	assert.Equal(t, 0, st.InboxCount, "status inboxCount must be 0 after consume")
	assert.Equal(t, queryID, st.LastConsumedMsgID)

	peers = peersByID(t, dataDir)
	require.Contains(t, peers, escID)
	assert.Equal(t, 0, peers[escID].InboxCount, "peers inboxCount must be 0 after consume")
	assert.Equal(t, queryID, peers[escID].LastConsumedMsgID)
}

// trimSpaceLocal trims surrounding whitespace (kept local to minimize this
// file's imports).
func trimSpaceLocal(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\n' || s[start] == '\t' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\n' || s[end-1] == '\t' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
