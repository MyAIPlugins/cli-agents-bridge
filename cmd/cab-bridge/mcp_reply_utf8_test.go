//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// F-176, criterion 8, on the Claude Desktop door. The server hands the arguments of a call
// over as raw bytes, and a byte that is not UTF-8 is still in them; decoding them into a
// string would turn it into U+FFFD without a word, so `toolReply` checks the bytes BEFORE it
// decodes. json.Marshal can never produce such a frame (it writes U+FFFD itself), so the
// test writes the line by hand.

// callRawArguments sends a tools/call whose `arguments` are exactly the bytes given, and
// returns the tool result the same way mcpClient.call does.
func (c *mcpClient) callRawArguments(tool string, rawArgs []byte) toolResult {
	c.t.Helper()
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan rpcReply, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	line := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":`, id, tool))
	line = append(line, rawArgs...)
	line = append(line, '}', '}', '\n')
	_, err := c.stdin.Write(line)
	require.NoError(c.t, err)

	select {
	case reply := <-ch:
		require.Nil(c.t, reply.Error, reply.Raw)
		var res struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		}
		require.NoError(c.t, json.Unmarshal(reply.Result, &res), reply.Raw)
		out := toolResult{IsError: res.IsError, Raw: reply.Raw}
		for _, b := range res.Content {
			out.Blocks = append(out.Blocks, b.Text)
		}
		return out
	case <-time.After(30 * time.Second):
		c.t.Fatalf("tool %s did not answer within 30s", tool)
		return toolResult{}
	}
}

// replyWithBadByte is a `reply` call for askID whose message carries the byte 0xff.
func (j *joinedDemo) replyWithBadByte(askID string) toolResult {
	j.e.t.Helper()
	raw := []byte(fmt.Sprintf(`{"ask_id":%q,"message":"r`, askID))
	raw = append(raw, 0xff)
	raw = append(raw, []byte(`x"}`)...)
	require.False(j.e.t, utf8.Valid(raw), "premise: the frame really carries a byte that is not UTF-8")
	require.True(j.e.t, bytes.Contains(raw, []byte{0xff}))
	return j.c.callRawArguments("reply", raw)
}

func TestMCPReply_AMessageThatIsNotUTF8IsRefusedBeforeAnyEffect(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeQuery, "ask")

	res := j.replyWithBadByte("msg-aaaaaaaaaaaa")
	require.True(t, res.IsError, "the call must be refused, got: %s", res.Text())
	assert.Contains(t, res.Text(), "UTF-8")

	_, found := j.journal()
	assert.False(t, found, "no journal was created")
	assert.Empty(t, j.valInboxFiles(), "nothing was delivered")
	assert.True(t, j.inInbox("msg-aaaaaaaaaaaa"), "the ask is still open")
	assert.False(t, j.archived("msg-aaaaaaaaaaaa"))
}

// With a journal in flight the same call is refused all the same, and the journal is byte for
// byte what it was: this door reads the raw bytes always, so it has no "recovery" exception.
func TestMCPReply_AMessageThatIsNotUTF8IsRefusedEvenWithAJournalInFlight(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeQuery, "ask A")
	j.put("msg-bbbbbbbbbbbb", j.valSID, message.TypeQuery, "ask B")

	selected := false
	_, err := beginReply(j.e.manager(), j.arcSID,
		j.app.replySelector(j.app.lookup(projectKey("/repo/demo", surfaceCowork)), newAuthorizer(j.e.manager(), "/repo/demo", j.e.clock.now()), "msg-aaaaaaaaaaaa", "the answer to A", &selected), nil)
	require.NoError(t, err)
	before, err := os.ReadFile(filepath.Join(j.e.dataDir, "sessions", j.arcSID, "reply-txn.json"))
	require.NoError(t, err)

	res := j.replyWithBadByte("msg-bbbbbbbbbbbb")
	require.True(t, res.IsError, "the call must be refused, got: %s", res.Text())
	assert.Contains(t, res.Text(), "UTF-8")

	after, err := os.ReadFile(filepath.Join(j.e.dataDir, "sessions", j.arcSID, "reply-txn.json"))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "the journal in flight is untouched")
	assert.Empty(t, j.valInboxFiles(), "nothing was delivered, not even the journal's own reply")
	assert.True(t, j.inInbox("msg-aaaaaaaaaaaa"))
	assert.True(t, j.inInbox("msg-bbbbbbbbbbbb"))
	var txn session.ReplyTxn
	require.NoError(t, json.Unmarshal(after, &txn))
	assert.Equal(t, session.ReplyTxnPending, txn.State)
}
