package codexws

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSmoke_AgainstRealAppServer talks to a live `codex app-server`. Unit tests
// prove the codec against frames we build ourselves, which cannot catch a wrong
// assumption about what the vendor actually sends — only a real process can.
//
// Opt-in, because it needs a running server and a Codex install:
//
//	codex app-server --listen unix:///Users/<you>/.codex/smoke/as.sock
//	CAB_CODEX_SMOKE=unix:///Users/<you>/.codex/smoke/as.sock go test -run Smoke ./internal/codexws/
func TestSmoke_AgainstRealAppServer(t *testing.T) {
	endpoint := os.Getenv("CAB_CODEX_SMOKE")
	if endpoint == "" {
		t.Skip("set CAB_CODEX_SMOKE=<unix://path|ws://host:port> to run against a live app-server")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := Dial(ctx, endpoint, 0)
	require.NoError(t, err, "handshake against the real server must succeed")
	defer func() { _ = conn.Close() }()

	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{"clientInfo": map[string]string{"name": "cab-smoke", "version": "0.1.0"}},
	})
	require.NoError(t, err)
	require.NoError(t, conn.WriteMessage(req))

	// The server interleaves notifications with responses, so read until the
	// reply carrying our id shows up rather than assuming it arrives first.
	for {
		raw, err := conn.ReadMessage()
		require.NoError(t, err)

		var msg struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		require.NoError(t, json.Unmarshal(raw, &msg))
		if msg.ID == nil || *msg.ID != 1 {
			continue // a notification such as remoteControl/status/changed
		}
		require.Nil(t, msg.Error, "initialize returned an error: %s", msg.Error)
		require.NotEmpty(t, msg.Result, "initialize must carry a result")
		t.Logf("initialize result: %s", msg.Result)
		break
	}

	// A large real response exercises the extended length paths against bytes
	// the vendor actually produces, not frames we built ourselves.
	list, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "thread/list", "params": map[string]any{"limit": 20},
	})
	require.NoError(t, err)
	require.NoError(t, conn.WriteMessage(list))

	for {
		raw, err := conn.ReadMessage()
		require.NoError(t, err)

		var msg struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		require.NoError(t, json.Unmarshal(raw, &msg))
		if msg.ID == nil || *msg.ID != 2 {
			continue
		}
		require.Nil(t, msg.Error, "thread/list returned an error: %s", msg.Error)
		t.Logf("thread/list payload: %d bytes reassembled", len(raw))
		return
	}
}
