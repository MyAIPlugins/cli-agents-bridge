package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// findAckInInbox scans inboxDir for a type=ack message whose inReplyTo equals
// replyTo. Returns nil if none yet (caller polls).
func findAckInInbox(t *testing.T, inboxDir, replyTo string) *message.Message {
	t.Helper()
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(inboxDir, e.Name()))
		if err != nil {
			continue
		}
		m, err := message.DecodeLenient(data, 65536)
		if err != nil {
			continue
		}
		if m.Type == message.TypeAck && m.InReplyTo != nil && *m.InReplyTo == replyTo {
			return m
		}
	}
	return nil
}

func readLastConsumed(t *testing.T, dataDir, sessionID string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dataDir, "sessions", sessionID, "manifest.json"))
	if err != nil {
		return ""
	}
	var mf struct {
		LastConsumedMsgID string `json:"lastConsumedMsgId"`
	}
	if json.Unmarshal(data, &mf) != nil {
		return ""
	}
	return mf.LastConsumedMsgID
}

// TestScenarioAutoAck_ListenAcksQuerySender is the F-12 end-to-end via the real
// binary (LL-10: real subprocesses, ephemeral/live PIDs, the actual consume
// path). VAL asks a query; ESC's listen consumes it and must auto-emit a
// type=ack back to VAL, and record the query as its lastConsumedMsgId.
func TestScenarioAutoAck_ListenAcksQuerySender(t *testing.T) {
	// Not parallel: starts/kills a background listen subprocess.
	dataDir := t.TempDir()
	bin := buildBinary(t)

	out, errOut, exit := run(t, []string{"register", "--role=val", "--agent-name=VAL-aa", "--project-path=" + t.TempDir()}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "register VAL: %s", errOut)
	valID := mustJSONField(t, out, "sessionId")

	out, errOut, exit = run(t, []string{"register", "--role=esc", "--agent-name=ESC-aa", "--project-path=" + t.TempDir()}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "register ESC: %s", errOut)
	escID := mustJSONField(t, out, "sessionId")

	out, errOut, exit = run(t, []string{"ask", "--session-id=" + valID, "--to=" + escID, "--type=query", "--content=brief"}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "ask: %s", errOut)
	queryID := strings.TrimSpace(out)
	require.True(t, strings.HasPrefix(queryID, "msg-"), "ask must emit a message id; got %q", queryID)

	listenCmd := exec.Command(bin, "listen", "--session-id="+escID)
	listenCmd.Env = append(os.Environ(), dataDirEnv(dataDir)...)
	require.NoError(t, listenCmd.Start())
	t.Cleanup(func() {
		_ = listenCmd.Process.Kill()
		_, _ = listenCmd.Process.Wait()
	})

	valInbox := filepath.Join(dataDir, "sessions", valID, "inbox")
	var ack *message.Message
	require.Eventually(t, func() bool {
		ack = findAckInInbox(t, valInbox, queryID)
		return ack != nil
	}, 5*time.Second, 50*time.Millisecond, "VAL must receive an auto-ack for its query")

	assert.Equal(t, message.TypeAck, ack.Type)
	assert.Equal(t, escID, ack.From, "ack must come from the ESC listener")
	assert.Equal(t, valID, ack.To)

	require.Eventually(t, func() bool {
		return readLastConsumed(t, dataDir, escID) == queryID
	}, 5*time.Second, 50*time.Millisecond, "ESC manifest must record the consumed query as lastConsumedMsgId")
}

// TestScenarioAutoAck_NoAutoAckFlagSuppresses verifies the opt-out: with
// --no-auto-ack the query is still consumed but NO receipt is sent to VAL.
func TestScenarioAutoAck_NoAutoAckFlagSuppresses(t *testing.T) {
	dataDir := t.TempDir()
	bin := buildBinary(t)

	out, errOut, exit := run(t, []string{"register", "--role=val", "--agent-name=VAL-na", "--project-path=" + t.TempDir()}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "register VAL: %s", errOut)
	valID := mustJSONField(t, out, "sessionId")

	out, errOut, exit = run(t, []string{"register", "--role=esc", "--agent-name=ESC-na", "--project-path=" + t.TempDir()}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "register ESC: %s", errOut)
	escID := mustJSONField(t, out, "sessionId")

	out, errOut, exit = run(t, []string{"ask", "--session-id=" + valID, "--to=" + escID, "--type=query", "--content=brief"}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "ask: %s", errOut)
	queryID := strings.TrimSpace(out)

	listenCmd := exec.Command(bin, "listen", "--session-id="+escID, "--no-auto-ack")
	listenCmd.Env = append(os.Environ(), dataDirEnv(dataDir)...)
	require.NoError(t, listenCmd.Start())
	t.Cleanup(func() {
		_ = listenCmd.Process.Kill()
		_, _ = listenCmd.Process.Wait()
	})

	// Wait until the query is consumed (proves listen processed it). In the
	// consume loop SetLastConsumed runs immediately before the (suppressed)
	// auto-ack, so once this is set no ack will ever be emitted.
	require.Eventually(t, func() bool {
		return readLastConsumed(t, dataDir, escID) == queryID
	}, 5*time.Second, 50*time.Millisecond, "ESC must consume the query under --no-auto-ack")

	valInbox := filepath.Join(dataDir, "sessions", valID, "inbox")
	assert.Nil(t, findAckInInbox(t, valInbox, queryID), "--no-auto-ack must suppress the delivery receipt")
}
