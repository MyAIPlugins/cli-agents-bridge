//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// F-172, end to end through the real `next` tool of the MCP server: the unreadable
// file travels from the scan, through the pass and the view, into the result. The unit
// tests of the scan and of the notice cannot see a link in that chain that drops it.
func TestMCPNext_AnUnreadableFileIsNamedInTheResultWithItsCauseAndTheOthersStillArrive(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeQuery, "readable brief")
	j.put("msg-bbbbbbbbbbbb", j.valSID, message.TypeQuery, "denied brief")
	denyReading(t, filepath.Join(j.inbox, "msg-bbbbbbbbbbbb.json"))

	res := j.next(0)
	require.False(t, res.IsError, res.Text())
	out := res.Text()

	assert.Contains(t, out, "1 message(s) open for you", "the readable message still arrives")
	assert.Contains(t, out, "readable brief")
	assert.Contains(t, out, `"msg-bbbbbbbbbbbb.json" (permission denied)`, "the file and the cause")
	assert.Contains(t, out, "NOT shown or delivered")
	assert.NotContains(t, out, "left alone", "the old notice")
	assert.NotContains(t, out, j.e.dataDir, "no path in the result")
}

// The Major of the diff-gate (CRI), turned the right way up: the production server
// over its pipe, a real filesystem, five files the system really refuses whose names
// are 237 bytes, the smallest budget the project's tests use, and the same call three
// times with the same inbox. A notice that names five such files takes the whole
// budget, and the readable message was not even named in any of the calls.
func TestMCPNext_FiveLongUnreadableNamesDoNotKeepAReadableMessageFromBeingPresented(t *testing.T) {
	e := newMCPEnv(t)
	e.cfg.MCPMaxResultBytes = 2048
	e.val("valdemo01", "VAL-demo", "/repo/demo", 8*time.Hour)
	_, client := e.start(coworkClient)
	res := client.call("join", map[string]any{"project": "demo"})
	require.False(t, res.IsError, res.Text())
	arcs := architects(t, e)
	require.Len(t, arcs, 1)
	sid := arcs[0].SessionID
	inbox := filepath.Join(e.dataDir, "sessions", sid, "inbox")
	const goodID = "msg-aaaaaaaaaaaa"
	plantInboxAt(t, e.dataDir, sid, goodID, "valdemo01", message.TypeQuery, "READABLE-CONTROL", e.clock.now())
	for i := 0; i < 5; i++ {
		path := filepath.Join(inbox, strings.Repeat("x", 230)+fmt.Sprintf("%02d.json", i))
		require.NoError(t, os.WriteFile(path, []byte("{}"), 0o600))
		denyReading(t, path)
	}

	for call := 1; call <= 3; call++ {
		res := client.call("next", map[string]any{"wait_seconds": 0})
		require.False(t, res.IsError, res.Text())
		out := res.Text()

		require.Contains(t, out, "1 message(s) open for you")
		require.Contains(t, out, goodID, "call %d: the readable message is presented, in full or as a pointer line", call)
		assert.NotContains(t, out, "result cut by the server", "call %d: the notice is fitted by us, not cut by the server", call)
		assert.Contains(t, out, "5 file(s) in the inbox cannot be read and are NOT shown or delivered", "call %d", call)
		assert.Contains(t, out, "permission denied", "call %d", call)
		assert.Contains(t, out, "until then those messages will not arrive", "call %d", call)
	}
}
