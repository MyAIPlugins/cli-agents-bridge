//go:build !windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// wireText is everything the server ever wrote to this client.
func (c *mcpClient) wireText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.wire, "\n")
}

// What Desktop must never learn, through any tool, in any state: a held message's
// text, its author's name (the real one and the one the message claims), its author's
// session id; the text and addressee of a recovered journal. The test calls EVERY tool
// with arguments aimed at those messages and checks the whole wire and the log, not
// the places a result "should" have been careful.
func TestMCPNothingHeldBackReachesDesktopThroughAnyTool(t *testing.T) {
	j := newJoinedDemo(t)
	e := j.e
	plantOverviewSession(t, e.dataDir, "escsecret", session.RoleEsc, "ESC-SECRET-NAME", "/repo/demo", "", "working")
	// A val of another project: with its bridge closed it is not even a project Desktop
	// is told about, so its name has no legitimate place in anything the server says.
	plantOverviewSession(t, e.dataDir, "valother01", session.RoleVal, "VAL-OTHER-PROJECT", "/repo/other", "", session.StateOrchestrating)
	plantOverviewSession(t, e.dataDir, "valclosed1", session.RoleVal, "VAL-CLOSED-ONE", "/repo/demo", "", session.StateOrchestrating)

	// Held back, five different ways. plantInboxAt makes every message CLAIM to be from
	// "ESC-y", a plausible-looking name that must not come back either.
	held := map[string]string{
		"msg-a00000000001": "escsecret",  // an executor
		"msg-a00000000002": "valother01", // a val of another project
		"msg-a00000000003": "valclosed1", // a val of this project, bridge closed
		"msg-a00000000004": "ghostsess1", // an author with no session at all
		"msg-a00000000005": "../escape",  // an author that is a path
	}
	bodies := map[string]string{
		"msg-a00000000001": "BODY-FROM-EXECUTOR",
		"msg-a00000000002": "BODY-FROM-OTHER-PROJECT",
		"msg-a00000000003": "BODY-FROM-CLOSED-VAL",
		"msg-a00000000004": "BODY-FROM-NOBODY",
		"msg-a00000000005": "BODY-FROM-A-PATH",
	}
	for id, from := range held {
		j.put(id, from, message.TypeQuery, bodies[id])
	}
	j.put("msg-b00000000001", j.valSID, message.TypeNotify, "the one visible message")

	// A recovered journal addressed to the executor, already sent: what it holds is the
	// kind of thing a stale life leaves behind.
	require.NoError(t, e.manager().WriteReplyTxn(j.arcSID, &session.ReplyTxn{
		ResponseID: session.DeterministicResponseID(j.arcSID, "msg-a00000000001"), To: "escsecret", Anchor: "msg-a00000000001",
		CloseIDs: []string{"msg-a00000000001"}, State: session.ReplyTxnSent, Timestamp: e.clock.now(), Content: "JOURNAL-SECRET-TEXT",
	}))

	// Every tool, aimed at the secrets.
	j.c.call("join", nil)
	j.c.call("join", map[string]any{"project": "demo"})
	for i := 0; i < 3; i++ {
		j.next(0)
	}
	for id := range held {
		j.c.call("read", map[string]any{"id": id})
		j.ack(id)
		j.reply(id, "an answer")
	}
	j.ack("msg-b00000000001")
	for _, to := range []string{"ESC-SECRET-NAME", "VAL-OTHER-PROJECT", "VAL-CLOSED-ONE", "ghostsess1", "escsecret"} {
		j.c.call("ask", map[string]any{"to": to, "message": "hello"})
		j.c.call("tell", map[string]any{"to": to, "message": "hello"})
	}
	j.c.call("next", map[string]any{"wait_seconds": 1})

	everything := j.c.wireText() + "\n" + e.logs.all()
	for _, secret := range []string{
		"BODY-FROM-EXECUTOR", "BODY-FROM-OTHER-PROJECT", "BODY-FROM-CLOSED-VAL", "BODY-FROM-NOBODY", "BODY-FROM-A-PATH", "JOURNAL-SECRET-TEXT",
		"ESC-SECRET-NAME", "VAL-OTHER-PROJECT", "VAL-CLOSED-ONE", "ESC-y",
		"escsecret", "valother01", "valclosed1", "ghostsess1", "../escape",
	} {
		// The model's own arguments are echoed in the refusals it provoked, and that is
		// the one place a name may reappear: it typed it. So the recipients it was told
		// about by nobody are checked in the results of the calls that did NOT type them.
		if secretTypedByTheCaller(secret) {
			continue
		}
		assert.NotContains(t, everything, secret, "%q reached Desktop or its log", secret)
	}
	assert.Empty(t, j.c.strayLines())

	// And the visible message was still served: the care did not turn into silence.
	assert.Contains(t, j.c.wireText(), "the one visible message")
}

// The names the test types itself as `to` arguments come back inside the refusals
// they provoke; those are the model's own words.
func secretTypedByTheCaller(secret string) bool {
	switch secret {
	case "ESC-SECRET-NAME", "VAL-OTHER-PROJECT", "VAL-CLOSED-ONE", "ghostsess1", "escsecret":
		return true
	}
	return false
}

// The names a caller did not type are the ones that matter, and they are checked
// where they would leak: results of calls that never mention them.
func TestMCPNamesNobodyTypedNeverAppearInTheListsTheServerBuilds(t *testing.T) {
	j := newJoinedDemo(t)
	plantOverviewSession(t, j.e.dataDir, "escsecret", session.RoleEsc, "ESC-SECRET-NAME", "/repo/demo", "", "working")
	plantOverviewSession(t, j.e.dataDir, "valclosed1", session.RoleVal, "VAL-CLOSED-ONE", "/repo/demo", "", session.StateOrchestrating)
	j.put("msg-a00000000001", "escsecret", message.TypeQuery, "BODY-FROM-EXECUTOR")

	for _, res := range []toolResult{
		j.c.call("join", nil),
		j.c.call("join", map[string]any{"project": "demo"}),
		j.next(0),
		j.c.call("tell", map[string]any{"to": "-h", "message": "x"}),
		j.c.call("ask", map[string]any{"to": "nobody-at-all", "message": "x"}),
		j.c.call("join", map[string]any{"project": "no-such-project"}),
	} {
		for _, secret := range []string{"ESC-SECRET-NAME", "VAL-CLOSED-ONE", "escsecret", "valclosed1", "BODY-FROM-EXECUTOR"} {
			assert.NotContains(t, res.Raw, secret)
		}
	}
}

// SC-4 at the place the MCP side reads a session id out of a manifest: a val whose
// manifest names a path as its id does not become an open project, even with a perfectly
// good grant waiting at the place that path points to.
func TestMCPAValWhoseManifestNamesAPathIsNotAnOpenProject(t *testing.T) {
	e := newMCPEnv(t)
	e.val("valreal001", "VAL-real", "/repo/real", time.Hour)
	plantOverviewSession(t, e.dataDir, "valevil001", session.RoleVal, "VAL-evil", "/repo/evil", "", session.StateOrchestrating)

	// The manifest of valevil001 now claims another id, aimed at the grant of valreal001.
	manifest := filepath.Join(e.dataDir, "sessions", "valevil001", "manifest.json")
	data, err := os.ReadFile(manifest)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	raw["sessionId"] = "../sessions/valreal001"
	forged, err := json.Marshal(raw)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifest, forged, 0o600))

	_, c := e.start(coworkClient)
	res := c.call("join", nil)
	require.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "VAL-real")
	assert.NotContains(t, res.Text(), "VAL-evil", "its grant was read through a path, and a path is refused")
	assert.NotContains(t, res.Text(), "evil")
}

// Nothing in the server serializes the tools behind a call that waits: `next` holds no
// lock, so while it waits the same project is acked and answered, another project is
// joined and read, and a message sent to the first project ends the wait.
func TestMCPInterleavings_ACallThatWaitsBlocksNothingElse(t *testing.T) {
	e := newMCPEnv(t)
	e.val("valaaaaa01", "VAL-a", "/repo/a", 8*time.Hour)
	e.val("valbbbbb01", "VAL-b", "/repo/b", 8*time.Hour)
	app, c := e.start(coworkClient)
	require.False(t, c.call("join", map[string]any{"project": "a"}).IsError)
	require.False(t, c.call("join", map[string]any{"project": "b"}).IsError)
	pa, pb := app.lookup(projectKey("/repo/a", surfaceCowork)), app.lookup(projectKey("/repo/b", surfaceCowork))
	require.NotNil(t, pa)
	require.NotNil(t, pb)
	put := func(p *mcpProject, id, from, typ, body string) {
		plantInboxAt(t, e.dataDir, p.sid, id, from, typ, body, e.clock.now().Add(-time.Minute))
	}
	put(pa, "msg-aaaaaaaaaaa1", "valaaaaa01", message.TypeNotify, "tell for A")
	put(pa, "msg-aaaaaaaaaaa2", "valaaaaa01", message.TypeQuery, "ask for A")

	// A's backlog is shown once, then a long wait starts.
	require.Contains(t, c.call("next", map[string]any{"project": "a", "wait_seconds": 0}).Text(), "tell for A")
	waiting := make(chan toolResult, 1)
	started := time.Now()
	go func() { waiting <- c.call("next", map[string]any{"project": "a", "wait_seconds": 4}) }()
	time.Sleep(150 * time.Millisecond)

	// While it waits: ack and reply on the same project, join/next/read on the other.
	require.False(t, c.call("ack", map[string]any{"project": "a", "ids": []string{"msg-aaaaaaaaaaa1"}}).IsError)
	res := c.call("reply", map[string]any{"project": "a", "ask_id": "msg-aaaaaaaaaaa2", "message": "answered while the other call waits"})
	require.False(t, res.IsError, res.Text())
	put(pb, "msg-bbbbbbbbbbb1", "valbbbbb01", message.TypeNotify, "tell for B")
	res = c.call("next", map[string]any{"project": "b", "wait_seconds": 0})
	assert.Contains(t, res.Text(), "tell for B")
	assert.Less(t, time.Since(started), 3*time.Second, "all of that happened inside the other call's wait")

	select {
	case early := <-waiting:
		t.Fatalf("the waiting call returned before anything new arrived: %s", early.Text())
	default:
	}
	put(pa, "msg-aaaaaaaaaaa3", "valaaaaa01", message.TypeNotify, "the one that ends the wait")
	select {
	case got := <-waiting:
		assert.Contains(t, got.Text(), "the one that ends the wait")
	case <-time.After(3 * time.Second):
		t.Fatal("the waiting call did not return when a message arrived")
	}
}

// A mailbox of thousands of files is read within the scan budget and the result still
// fits: what does not fit is declared, not dropped. Separate from the tests that inject
// a timeout, because this one says something about the real cost.
func TestMCPLoad_AThousandsStrongInboxIsReadInTimeAndTheResultFits(t *testing.T) {
	if testing.Short() {
		t.Skip("plants 5,000 files")
	}
	j := newJoinedDemo(t)
	j.app.cfg.MCPScanBudgetSeconds = 5 // the production value
	const n = 5000
	for i := 0; i < n; i++ {
		j.put(fmt.Sprintf("msg-%012x", i+1), j.valSID, message.TypeNotify, fmt.Sprintf("tell number %d", i))
	}

	start := time.Now()
	res := j.next(0)
	elapsed := time.Since(start)
	require.False(t, res.IsError, res.Text())
	assert.Less(t, elapsed, 5*time.Second, "the scan finished inside the budget")
	assert.LessOrEqual(t, len(res.Raw), j.app.cfg.MCPMaxResultBytes, "and the answer fits the result budget")
	assert.Contains(t, res.Text(), fmt.Sprintf("%d message(s) open for you", n))
	assert.Contains(t, res.Text(), "did not fit in this result", "what does not fit is declared")

	// Acknowledging a page of them works at that size too.
	ids := make([]string, 20)
	for i := range ids {
		ids[i] = fmt.Sprintf("msg-%012x", i+1)
	}
	ackRes := j.ack(ids...)
	require.False(t, ackRes.IsError, ackRes.Text())
	assert.True(t, j.archived(ids[0]))
	t.Logf("scan of %d files, answer built in %s", n, elapsed)
}
