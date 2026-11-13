package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/mcp"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// F-172 on the Claude Desktop surface (the MCP server).

func TestScanInboxWithin_ADeniedFileIsUnreadableAndNotCorrupt(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantInboxAt(t, dataDir, inboxSID, "msg-aaaaaaaaaaaa", "valinbox1", message.TypeQuery, "brief", inboxT0)
	plantInboxAt(t, dataDir, inboxSID, "msg-bbbbbbbbbbbb", "valinbox1", message.TypeQuery, "denied", inboxT0)
	inbox := filepath.Join(sessionDirOf(dataDir), "inbox")
	denyReading(t, filepath.Join(inbox, "msg-bbbbbbbbbbbb.json"))
	require.NoError(t, os.WriteFile(filepath.Join(inbox, "msg-broken000000.json"), []byte("{nope"), 0o600))

	scan, err := scanInboxWithin(context.Background(), &scanGuard{}, inbox, 65536)
	require.NoError(t, err)

	assert.Len(t, scan.entries, 1)
	assert.Equal(t, []string{"msg-broken000000.json"}, scan.corrupt, "only what was read and does not decode")
	assert.Equal(t, []unreadableFile{{File: "msg-bbbbbbbbbbbb.json", Cause: "permission denied"}}, scan.unreadable)
}

func TestRenderNext_UnreadableFilesAreNamedWithTheirCauseAndSaidNotToArrive(t *testing.T) {
	t.Parallel()
	v := view(nil, nil, 0, 24576)
	v.unreadable = []unreadableFile{{File: "msg-bbbbbbbbbbbb.json", Cause: "permission denied"}}

	out := joined(renderNext(mcp.NewPlanner(), v))

	assert.Contains(t, out, `1 file(s) in the inbox cannot be read and are NOT shown or delivered ("msg-bbbbbbbbbbbb.json" (permission denied))`)
	assert.Contains(t, out, "A person has to fix the cause on the machine or move the file(s) out of the inbox")
	assert.Contains(t, out, "until then those messages will not arrive")
	assert.NotContains(t, out, "left alone", "the old text, which said nothing was wrong")
	assert.NotContains(t, out, string(filepath.Separator)+"inbox", "ids, causes and counts only: no path")
}

func TestRenderNext_TheNoticeNamesAtMostFiveUnreadableFiles(t *testing.T) {
	t.Parallel()
	var files []unreadableFile
	for i := 1; i <= 8; i++ {
		files = append(files, unreadableFile{File: fmt.Sprintf("msg-%012d.json", i), Cause: "input/output error"})
	}
	v := view(nil, nil, 0, 24576)
	v.unreadable = files

	out := joined(renderNext(mcp.NewPlanner(), v))

	assert.Contains(t, out, "8 file(s)")
	assert.Contains(t, out, `"msg-000000000005.json" (input/output error)`)
	assert.NotContains(t, out, "msg-000000000006.json", "counted, not named")
	assert.Contains(t, out, "and 3 more")
}

func TestRenderNext_ACorruptFileIsSaidNotToArriveEither(t *testing.T) {
	t.Parallel()
	out := joined(renderNext(mcp.NewPlanner(), view(nil, nil, 2, 24576)))

	assert.Contains(t, out, "2 file(s) in the inbox are not valid messages and are NOT shown or delivered")
	assert.Contains(t, out, "the sender has to send the message again")
	assert.NotContains(t, out, "left alone")
}

// The notice is counted in the budget by its real text: with unreadable files named
// the result still fits every budget, and the messages are not admitted into the room
// the notice needs.
func TestRenderNext_TheResultFitsEveryBudgetWithUnreadableFilesNamed(t *testing.T) {
	t.Parallel()
	var msgs []visibleMessage
	for i, size := range []int{300, 900, 1500} {
		msgs = append(msgs, vis(fmt.Sprintf("msg-%012d", i), "query", strings.Repeat("a b\"c ", size/6), i+1))
	}
	var files []unreadableFile
	for i := 1; i <= 7; i++ {
		files = append(files, unreadableFile{File: fmt.Sprintf("msg-%012d.json", 100+i), Cause: "permission denied"})
	}
	for budget := 2048; budget <= 9000; budget += 7 {
		v := view(msgs, nil, 2, budget)
		v.unreadable = files
		res := renderNext(mcp.NewPlanner(), v)
		require.Contains(t, joined(res), "cannot be read and are NOT shown", "the premise: the notice is in the result (budget %d)", budget)
		line, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1234567890, "result": res})
		require.NoError(t, err)
		require.LessOrEqual(t, len(line), budget, "budget %d", budget)
	}
}
