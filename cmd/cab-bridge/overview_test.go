package main

import (
	"bytes"
	"encoding/json"
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

// F-81: a live PID + a future listen window → the session is actively listening,
// and overview reports the pid and the window.
func TestBuildOverview_ListenerActive(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantOverviewSession(t, dataDir, "lsnov01", session.RoleEsc, "ESC-x", "/repo/x", "", "working") // PID = os.Getpid() (live)
	mgr := session.NewManager(dataDir, time.Second)
	until := time.Now().UTC().Add(time.Hour)
	require.NoError(t, mgr.SetListenUntil("lsnov01", until))

	rep, err := buildOverview(mgr, overviewTestCfg(dataDir), "lsnov01")
	require.NoError(t, err)
	assert.True(t, rep.ListenerActive, "live PID + future window → listening")
	assert.Equal(t, os.Getpid(), rep.ListenerPid)
	require.NotNil(t, rep.ListenerUntil)
	assert.WithinDuration(t, until, *rep.ListenerUntil, time.Second, "the window survives the manifest round-trip")
}

// F-81: a past listen window (the listen exited or its window expired) → not
// listening, and pid/until are suppressed.
func TestBuildOverview_ListenerExpiredWindow(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantOverviewSession(t, dataDir, "lsnov02", session.RoleEsc, "ESC-x", "/repo/x", "", "")
	mgr := session.NewManager(dataDir, time.Second)
	require.NoError(t, mgr.SetListenUntil("lsnov02", time.Now().UTC().Add(-time.Minute)))

	rep, err := buildOverview(mgr, overviewTestCfg(dataDir), "lsnov02")
	require.NoError(t, err)
	assert.False(t, rep.ListenerActive, "an expired window → not listening")
	assert.Zero(t, rep.ListenerPid)
	assert.Nil(t, rep.ListenerUntil)
}

// F-81: a legacy/non-listen manifest has no listenUntil at all → not listening,
// no crash on the nil pointer.
func TestBuildOverview_ListenerAbsentLegacy(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantOverviewSession(t, dataDir, "lsnov03", session.RoleEsc, "ESC-x", "/repo/x", "", "") // no SetListenUntil

	mgr := session.NewManager(dataDir, time.Second)
	rep, err := buildOverview(mgr, overviewTestCfg(dataDir), "lsnov03")
	require.NoError(t, err)
	assert.False(t, rep.ListenerActive, "no listen window (legacy/non-listen) → not listening")
	assert.Nil(t, rep.ListenerUntil)
}

// F-81: a future window but a DEAD PID (the listen process is gone) → not
// listening. The AND of PID-alive and future-window guards the false positive of
// a stale ListenUntil left in the manifest.
func TestBuildOverview_ListenerDeadPID(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantOverviewSession(t, dataDir, "lsnov04", session.RoleEsc, "ESC-x", "/repo/x", "", "")
	mgr := session.NewManager(dataDir, time.Second)
	mf, err := mgr.LoadManifest("lsnov04")
	require.NoError(t, err)
	mf.PID = deadPID
	future := time.Now().UTC().Add(time.Hour)
	mf.ListenUntil = &future
	require.NoError(t, mgr.SaveManifest(mf))

	rep, err := buildOverview(mgr, overviewTestCfg(dataDir), "lsnov04")
	require.NoError(t, err)
	assert.False(t, rep.ListenerActive, "future window but dead PID → not listening")
}

func TestPrintOverviewHuman_ListenerActive(t *testing.T) {
	t.Parallel()
	until := time.Now().UTC().Add(30 * time.Minute)
	rep := overviewReport{
		Me:             overviewSelf{SessionID: "esc12345", AgentName: "ESC-x", Role: "esc", Stale: false},
		ListenerActive: true,
		ListenerPid:    4321,
		ListenerUntil:  &until,
		Inbox:          []overviewMsg{},
	}
	var b bytes.Buffer
	printOverviewHuman(&b, rep)
	out := b.String()
	assert.Contains(t, out, "listener: listening (PID 4321", "the listener line names the pid")
	assert.Contains(t, out, "expires in", "and the remaining window")
}

func TestPrintOverviewHuman_ListenerNotListening(t *testing.T) {
	t.Parallel()
	rep := overviewReport{
		Me:    overviewSelf{SessionID: "esc12345", AgentName: "ESC-x", Role: "esc", Stale: true},
		Inbox: []overviewMsg{},
	}
	var b bytes.Buffer
	printOverviewHuman(&b, rep)
	assert.Contains(t, b.String(), "listener: not listening")
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

// TestRunOverview_SessionIDFlag_ResolvesExplicitSession is the A-3 (F-86) check:
// with --session-id, overview reports THAT session directly, regardless of the
// cwd — the worktree/shared-scope case where the bare cwd lookup is wrong.
func TestRunOverview_SessionIDFlag_ResolvesExplicitSession(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	plantOverviewSession(t, dataDir, "ovsid001", session.RoleEsc, "ESC-x", "/repo/x", "", "working")

	var runErr error
	out := captureStdout(t, func() {
		runErr = runOverview([]string{"--session-id=ovsid001", "--json"})
	})
	require.NoError(t, runErr)

	var rep overviewReport
	require.NoError(t, json.Unmarshal([]byte(out), &rep))
	assert.Equal(t, "ovsid001", rep.Me.SessionID, "an explicit --session-id resolves me directly, not via the cwd")
	assert.Equal(t, session.RoleEsc, rep.Me.Role)
}

// TestRunOverview_SessionIDFlag_InvalidRejected: a malformed --session-id goes
// through the same SC-4 validation as every other id path and is rejected with
// an overview-prefixed error.
func TestRunOverview_SessionIDFlag_InvalidRejected(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	err := runOverview([]string{"--session-id=BAD!!"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overview:")
}

// TestBuildOverview_ListenerGeneration is the B-2 observability: a claimed
// listener surfaces its generation; PID!=0 → not reclaim-pending.
func TestBuildOverview_ListenerGeneration(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantOverviewSession(t, dataDir, "lsngen01", session.RoleEsc, "ESC-x", "/repo/x", "", "working")
	mgr := session.NewManager(dataDir, time.Second)
	_, err := mgr.ClaimListener("lsngen01")
	require.NoError(t, err)

	rep, err := buildOverview(mgr, overviewTestCfg(dataDir), "lsngen01")
	require.NoError(t, err)
	assert.Equal(t, 1, rep.ListenerGeneration)
	assert.False(t, rep.ListenerReclaimPending)
}

// TestBuildOverview_ListenerReclaimPending: after a reclaim (PID==0), overview
// reports reclaim-pending with the bumped generation.
func TestBuildOverview_ListenerReclaimPending(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantOverviewSession(t, dataDir, "lsngen02", session.RoleEsc, "ESC-x", "/repo/x", "", "")
	mgr := session.NewManager(dataDir, time.Second)
	_, err := mgr.ClaimListener("lsngen02")
	require.NoError(t, err)
	_, err = mgr.ReclaimListener("lsngen02")
	require.NoError(t, err)

	rep, err := buildOverview(mgr, overviewTestCfg(dataDir), "lsngen02")
	require.NoError(t, err)
	assert.Equal(t, 2, rep.ListenerGeneration)
	assert.True(t, rep.ListenerReclaimPending, "PID==0 after reclaim → reclaim-pending")
}

// TestPrintOverviewHuman_ListenerGenerationAndReclaim: the human line carries
// the generation when active, and the reclaim-pending hint otherwise.
func TestPrintOverviewHuman_ListenerGenerationAndReclaim(t *testing.T) {
	t.Parallel()
	until := time.Now().UTC().Add(time.Hour)
	var b bytes.Buffer
	printOverviewHuman(&b, overviewReport{
		Me:             overviewSelf{SessionID: "esc12345", AgentName: "ESC-x", Role: "esc"},
		ListenerActive: true, ListenerPid: 4321, ListenerUntil: &until, ListenerGeneration: 2,
		Inbox: []overviewMsg{},
	})
	assert.Contains(t, b.String(), "generation 2", "active listener line carries the generation")

	var b2 bytes.Buffer
	printOverviewHuman(&b2, overviewReport{
		Me:                     overviewSelf{SessionID: "esc12345", AgentName: "ESC-x", Role: "esc"},
		ListenerReclaimPending: true, ListenerGeneration: 3,
		Inbox: []overviewMsg{},
	})
	assert.Contains(t, b2.String(), "reclaim-pending")
	assert.Contains(t, b2.String(), "generation 3")
}
