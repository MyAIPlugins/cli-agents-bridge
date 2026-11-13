//go:build !windows

package main

import (
	"path/filepath"
	"testing"

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
