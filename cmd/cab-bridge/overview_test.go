package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// plantOverviewSession writes a fresh-heartbeat manifest with explicit
// role/scope/team/state so buildOverview can be driven directly (no os.Chdir);
// the cwd->session resolution itself is covered elsewhere.
func plantOverviewSession(t *testing.T, dataDir, id, role, agentName, scope, team, state string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "sessions", id), 0o700))
	mgr := session.NewManager(dataDir, time.Second)
	now := time.Now().UTC()
	mf := &session.Manifest{
		SessionID:     id,
		SchemaVersion: session.SchemaVersionV2,
		ProjectName:   "proj-" + id,
		ProjectPath:   filepath.Join(dataDir, "proj-"+id),
		AgentName:     agentName,
		Role:          role,
		PID:           os.Getpid(),
		StartedAt:     now,
		LastHeartbeat: now,
		Status:        session.StatusActive,
		Capabilities:  []string{"query"},
		Scope:         scope,
		TeamID:        team,
		State:         state,
	}
	require.NoError(t, mgr.SaveManifest(mf))
}

func overviewTestCfg(dataDir string) config.Config {
	return config.Config{DataDir: dataDir, StaleSeconds: 300, MaxMessageBytes: 65536}
}

// TestBuildOverview_MePeerAndPendingInbox is the F-42 core: me + the
// complementary peer in my scope + only the PENDING inbox (processed/ excluded).
func TestBuildOverview_MePeerAndPendingInbox(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	scope := "/repo/x"
	plantOverviewSession(t, dataDir, "escov01", session.RoleEsc, "ESC-x", scope, "", "working")
	plantOverviewSession(t, dataDir, "valov01", session.RoleVal, "VAL-x", scope, "", session.StateOrchestrating)
	plantMsg(t, dataDir, "escov01", "inbox", "msg-aaaaaaaaaaaa", "valov01", "VAL-x", message.TypeQuery, "brief")
	plantMsg(t, dataDir, "escov01", "inbox", "msg-bbbbbbbbbbbb", "valov01", "VAL-x", message.TypeResponse, "follow")
	plantMsg(t, dataDir, "escov01", "processed", "msg-cccccccccccc", "valov01", "VAL-x", message.TypeResponse, "old")

	mgr := session.NewManager(dataDir, time.Second)
	rep, err := buildOverview(mgr, overviewTestCfg(dataDir), "escov01")
	require.NoError(t, err)

	assert.Equal(t, "escov01", rep.Me.SessionID)
	assert.Equal(t, session.RoleEsc, rep.Me.Role)
	assert.Equal(t, scope, rep.Me.Scope)
	assert.Equal(t, "working", rep.Me.State)
	assert.False(t, rep.Me.Stale)

	require.NotNil(t, rep.Peer, "the complementary val in the same scope must be paired")
	assert.Equal(t, "valov01", rep.Peer.SessionID)
	assert.Equal(t, session.RoleVal, rep.Peer.Role)
	assert.Equal(t, session.StateOrchestrating, rep.Peer.State)

	require.Len(t, rep.Inbox, 2, "only inbox/ (pending) messages, never processed/")
	gotIDs := map[string]bool{}
	for _, m := range rep.Inbox {
		gotIDs[m.MsgID] = true
	}
	assert.True(t, gotIDs["msg-aaaaaaaaaaaa"])
	assert.True(t, gotIDs["msg-bbbbbbbbbbbb"])
	assert.False(t, gotIDs["msg-cccccccccccc"], "processed message must not appear")
}

func TestBuildOverview_NoPeerInScope(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantOverviewSession(t, dataDir, "escov02", session.RoleEsc, "ESC-y", "/repo/y", "", "")

	mgr := session.NewManager(dataDir, time.Second)
	rep, err := buildOverview(mgr, overviewTestCfg(dataDir), "escov02")
	require.NoError(t, err)
	assert.Nil(t, rep.Peer)
	assert.Empty(t, rep.Inbox)
}

// A val in a DIFFERENT scope must not be paired (scope isolation holds for the
// overview peer selection too).
func TestBuildOverview_PeerInOtherScope_NotSelected(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantOverviewSession(t, dataDir, "escov03", session.RoleEsc, "ESC-z", "/repo/a", "", "")
	plantOverviewSession(t, dataDir, "valov03", session.RoleVal, "VAL-z", "/repo/b", "", session.StateOrchestrating)

	mgr := session.NewManager(dataDir, time.Second)
	rep, err := buildOverview(mgr, overviewTestCfg(dataDir), "escov03")
	require.NoError(t, err)
	assert.Nil(t, rep.Peer, "a val in a different scope must not be paired")
}

// A same-role session (and my own manifest) must never be selected as the peer.
func TestBuildOverview_SameRolePeer_NotSelected(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantOverviewSession(t, dataDir, "escov04", session.RoleEsc, "ESC-a", "/repo/c", "", "")
	plantOverviewSession(t, dataDir, "escov05", session.RoleEsc, "ESC-b", "/repo/c", "", "")

	mgr := session.NewManager(dataDir, time.Second)
	rep, err := buildOverview(mgr, overviewTestCfg(dataDir), "escov04")
	require.NoError(t, err)
	assert.Nil(t, rep.Peer, "neither a same-role session nor myself is a peer")
}

func TestPrintOverviewHuman_WithPeerAndInbox(t *testing.T) {
	t.Parallel()
	rep := overviewReport{
		Me:    overviewSelf{SessionID: "esc12345", AgentName: "ESC-x", Role: "esc", Scope: "/repo/x", State: "working", Stale: false},
		Peer:  &overviewPeer{SessionID: "val12345", AgentName: "VAL-x", Role: "val", State: "orchestrating", Stale: false},
		Inbox: []overviewMsg{{MsgID: "msg-aaaaaaaaaaaa", From: "val12345", FromAgentName: "VAL-x", Type: "query"}},
	}
	var b bytes.Buffer
	printOverviewHuman(&b, rep)
	out := b.String()
	assert.Contains(t, out, "me:    ESC-x  (esc12345)")
	assert.Contains(t, out, "scope /repo/x")
	assert.Contains(t, out, "state working")
	assert.Contains(t, out, "[live]")
	assert.Contains(t, out, "peer:  VAL-x  (val12345)")
	assert.Contains(t, out, "channel ok")
	assert.Contains(t, out, "inbox: 1 pending")
	assert.Contains(t, out, "msg-aaaaaaaaaaaa from VAL-x  type query")
}

func TestPrintOverviewHuman_NoPeerEmptyInbox(t *testing.T) {
	t.Parallel()
	rep := overviewReport{
		Me:    overviewSelf{SessionID: "esc12345", AgentName: "ESC-x", Role: "esc", Scope: "", State: "", Stale: true},
		Inbox: []overviewMsg{},
	}
	var b bytes.Buffer
	printOverviewHuman(&b, rep)
	out := b.String()
	assert.Contains(t, out, "scope -", "empty scope renders as dash")
	assert.Contains(t, out, "state idle", "empty state renders as idle")
	assert.Contains(t, out, "[stale]")
	assert.Contains(t, out, "peer:  (none paired in this scope yet)")
	assert.Contains(t, out, "inbox: empty")
}
