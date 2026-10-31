//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// Desktop starts one server process per surface (chat, Cowork) when the app launches
// and every conversation of that surface talks to it: the join, the identity and the
// inbox are the SURFACE's. The server cannot tell two conversations apart, so no text
// it writes may say that this conversation did or did not do something. A real Cowork
// opened a new conversation, was told "Already joined" and reported that it had never
// joined.

func TestMCPSurface_ASecondJoinSaysWhoseJoinItIsAndThatTheInboxIsShared(t *testing.T) {
	e := newMCPEnv(t)
	e.val("valdemo01", "VAL-demo", "/repo/demo", 8*time.Hour)
	_, c := e.start(coworkClient)

	first := c.call("join", map[string]any{"project": "demo"})
	require.False(t, first.IsError, first.Text())
	assert.Contains(t, first.Text(), "Joined demo as ARC-demo-cowork")
	assert.Contains(t, first.Text(), sharedBySurface, "the first join already says it is shared")

	// The call a NEW conversation of the same surface makes, on the same server.
	second := c.call("join", map[string]any{"project": "demo"})
	require.False(t, second.IsError, second.Text())
	assert.Contains(t, second.Text(), "Already joined to demo as ARC-demo-cowork, by this conversation earlier or by another one open on the same Claude Desktop surface (cowork)")
	assert.Contains(t, second.Text(), "shares the joined projects and the inbox")
	assert.Contains(t, second.Text(), "an `ack` or a `reply` in one removes the message for all of them")
}

// The sentence above is a claim about the server, so it is checked against it: there is
// one inbox behind every call, and an ack by one conversation is the end of the message
// for the next.
func TestMCPSurface_AnAckInOneCallTakesTheMessageAwayFromTheNext(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, "shared inbox")
	require.Contains(t, j.next(0).Text(), "shared inbox")
	other := j.c.call("join", map[string]any{"project": "demo"})
	require.Contains(t, other.Text(), "Already joined", "a second conversation arrives on the same identity")

	require.False(t, j.c.call("ack", map[string]any{"ids": []string{"msg-aaaaaaaaaaaa"}}).IsError)
	after := j.next(0)
	assert.Contains(t, after.Text(), "0 message(s) open for you", "gone for whoever asks next, whichever conversation it is")
}

func TestMCPSurface_TheRefusalsNameTheSurfaceNotTheConversation(t *testing.T) {
	e := newMCPEnv(t)
	e.val("valdemo01", "VAL-demo", "/repo/demo", 8*time.Hour)
	_, c := e.start(coworkClient)

	none := c.call("next", nil)
	require.True(t, none.IsError)
	assert.Contains(t, none.Text(), "no project is joined on this Claude Desktop surface yet")
	assert.Contains(t, none.Text(), "shared by all their conversations")

	require.False(t, c.call("join", map[string]any{"project": "demo"}).IsError)
	other := c.call("next", map[string]any{"project": "elsewhere"})
	require.True(t, other.IsError)
	assert.Contains(t, other.Text(), `project "elsewhere" is not joined on this Claude Desktop surface`)
	assert.Contains(t, other.Text(), "a join is shared by all its conversations")
}

func TestMCPSurface_AHeldIdentityIsAProcessNotAConversation(t *testing.T) {
	e := newMCPEnv(t)
	e.val("valdemo01", "VAL-demo", "/repo/demo", time.Hour)
	_, first := e.start(coworkClient)
	_, second := e.start(coworkClient)
	require.False(t, first.call("join", map[string]any{"project": "demo"}).IsError)

	res := second.call("join", map[string]any{"project": "demo"})
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "already speaking as this identity")
	assert.Contains(t, res.Text(), "only one process at a time can hold a project through a surface (chat, or Cowork)")
	assert.Contains(t, res.Text(), "Quit the other Claude Desktop")
	assert.NotContains(t, res.Text(), "one conversation can hold")
}

func TestMCPSurface_APointerSaysWhereTheMessageWasShown(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, "seen by someone on this surface")
	require.Contains(t, j.next(0).Text(), "untrusted content below")

	pointer := j.next(0)
	assert.NotContains(t, pointer.Text(), "untrusted content below")
	assert.Contains(t, pointer.Text(), "already shown on this Claude Desktop surface, perhaps in another conversation")
	assert.Contains(t, pointer.Text(), "was already shown on this Claude Desktop surface (perhaps in another conversation)")
	assert.NotContains(t, pointer.Text(), "shown to you")
}

func TestMCPSurface_TheToolsOwnDescriptionsSayWhatTheSurfaceSharesToo(t *testing.T) {
	e := newMCPEnv(t)
	app, _ := e.start(coworkClient)
	descriptions := map[string]string{}
	for _, tool := range app.tools() {
		descriptions[tool.Name] = tool.Description
	}
	assert.Contains(t, descriptions["join"], sharedBySurface)
	assert.Contains(t, descriptions["next"], "shown in full once per Claude Desktop surface (all its conversations share that)")
	assert.Contains(t, descriptions["ack"], "for every conversation on this Claude Desktop surface (they share the inbox)")
}

// "Nothing that is said to the model attributes surface state to the conversation" is a
// property of the source, and a property of the source can be checked: it is also what
// stops the next text written in a hurry from doing it again.
func TestMCPSurface_NoTextOfTheServerBlamesThisConversation(t *testing.T) {
	files, err := filepath.Glob("mcp*.go")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		require.NoError(t, err)
		for _, banned := range []string{"in this conversation", "one conversation can", "shown to you"} {
			assert.NotContains(t, string(data), banned, "%s: the server does not know conversations", f)
		}
	}
}
