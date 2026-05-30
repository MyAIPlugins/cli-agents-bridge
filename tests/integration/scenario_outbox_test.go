package integration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScenarioOutbox_AskPopulatesSenderOutbox is the F-9 happy path via the real
// binary (LL-10): after an `ask`, the SENDER can see its own send — status
// reports outboxCount==1 and `cab sent` lists the message with recipient+type.
func TestScenarioOutbox_AskPopulatesSenderOutbox(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	valID, escID := registerPair(t, dataDir, "ob1")

	out, errOut, exit := run(t, []string{"ask", "--session-id=" + valID, "--to=" + escID, "--type=query", "--content=brief"}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "ask: %s", errOut)
	msgID := strings.TrimSpace(out)

	out, errOut, exit = run(t, []string{"status", "--session-id=" + valID}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "status: %s", errOut)
	assert.Equal(t, "1", mustJSONField(t, out, "outboxCount"), "sender outboxCount must be 1 after ask")

	out, errOut, exit = run(t, []string{"sent", "--session-id=" + valID, "--json"}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "sent: %s", errOut)
	assert.Contains(t, out, msgID, "cab sent must list the sent message id")
	assert.Contains(t, out, escID, "cab sent must show the recipient")
	assert.Contains(t, out, "query", "cab sent must show the type")
}

// TestScenarioOutbox_AutoAckPopulatesListenerOutbox: the F-9 copy also applies
// to auto-acks (which go through sendMessage). After ESC consumes a query with
// --wait-one, the auto-ack it sent to VAL appears in ESC's OWN outbox.
func TestScenarioOutbox_AutoAckPopulatesListenerOutbox(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	valID, escID := registerPair(t, dataDir, "ob2")

	_, errOut, exit := run(t, []string{"ask", "--session-id=" + valID, "--to=" + escID, "--type=query", "--content=brief"}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "ask: %s", errOut)

	_, errOut, exit = run(t, []string{"listen", "--wait-one", "--session-id=" + escID}, dataDirEnv(dataDir, "CAB_POLL_INTERVAL_MS=50"))
	require.Equal(t, 0, exit, "listen --wait-one: %s", errOut)

	out, errOut, exit := run(t, []string{"sent", "--session-id=" + escID, "--json"}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "sent: %s", errOut)
	assert.Contains(t, out, valID, "listener outbox must record the ack sent to the query sender")
	assert.Contains(t, out, "ack", "listener outbox must contain a type=ack message")
}

// TestScenarioOutbox_SentEmptyJSONIsArray: `cab sent --json` for a session that
// has sent nothing must emit [] (BUG-B hygiene), not null.
func TestScenarioOutbox_SentEmptyJSONIsArray(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	_, escID := registerPair(t, dataDir, "ob3")

	out, errOut, exit := run(t, []string{"sent", "--session-id=" + escID, "--json"}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "sent --json: %s", errOut)
	assert.Equal(t, "[]", strings.TrimSpace(out), "empty sent --json must be [] not null")
}
