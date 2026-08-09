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

// TestBuildOverview_MePeerAndUnreadInbox is the F-42 core: me + the
// complementary peer in my scope + the UNREAD inbox (processed/ excluded).
func TestBuildOverview_MePeerAndUnreadInbox(t *testing.T) {
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

	require.Len(t, rep.Inbox, 2, "only inbox/ messages, never processed/")
	gotIDs := map[string]bool{}
	for _, m := range rep.Inbox {
		gotIDs[m.MsgID] = true
	}
	assert.True(t, gotIDs["msg-aaaaaaaaaaaa"])
	assert.True(t, gotIDs["msg-bbbbbbbbbbbb"])
	assert.False(t, gotIDs["msg-cccccccccccc"], "processed message must not appear")
}

// The Riga-2 regression: under the mailbox model a delivered message stays in
// inbox/, so "everything in inbox/" and "everything still waiting for you" are
// different sets — and overview was reporting the first while answering the
// second. A message already handed over by `next` is not work waiting, and an
// ack was never work at all.
func TestBuildOverview_InboxIsUnreadNotEveryFile(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantOverviewSession(t, dataDir, "unrdov1", session.RoleEsc, "ESC-u", "/repo/u", "", "working")
	plantMsg(t, dataDir, "unrdov1", "inbox", "msg-aaaaaaaaaaaa", "valu0001", "VAL-u", message.TypeQuery, "already read")
	plantMsg(t, dataDir, "unrdov1", "inbox", "msg-bbbbbbbbbbbb", "valu0001", "VAL-u", message.TypeQuery, "still waiting")
	plantMsg(t, dataDir, "unrdov1", "inbox", "msg-cccccccccccc", "valu0001", "VAL-u", message.TypeAck, "receipt")

	mgr := session.NewManager(dataDir, time.Second)
	_, err := mgr.CommitWakeCursor("unrdov1", []string{"msg-aaaaaaaaaaaa"}, time.Now().UTC(), nil, nil)
	require.NoError(t, err)

	rep, err := buildOverview(mgr, overviewTestCfg(dataDir), "unrdov1")
	require.NoError(t, err)
	require.Len(t, rep.Inbox, 1, "three files in inbox/, one of them actually waiting")
	assert.Equal(t, "msg-bbbbbbbbbbbb", rep.Inbox[0].MsgID)

	// The files are all still there: this is a change of question, not a sweep.
	entries, err := os.ReadDir(filepath.Join(dataDir, "sessions", "unrdov1", "inbox"))
	require.NoError(t, err)
	assert.Len(t, entries, 3, "overview reads, it never archives")

	// And the same three files answer the same way through the peers column.
	assert.Equal(t, 1, countUnread(mgr, "unrdov1", filepath.Join(dataDir, "sessions", "unrdov1"), 65536),
		"one number, one meaning, wherever it is shown")
}

// The zero case says "nothing unread", never "empty": inbox/ may hold read
// messages nobody has archived, and calling that empty is the same kind of
// false statement in the other direction.
func TestPrintOverviewHuman_ZeroUnreadDoesNotClaimAnEmptyInbox(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	printOverviewHuman(&b, overviewReport{
		Me:    overviewSelf{SessionID: "esc12345", AgentName: "ESC-x", Role: "esc"},
		Inbox: []overviewMsg{},
	})
	assert.Contains(t, b.String(), "inbox: nothing unread")
	assert.NotContains(t, b.String(), "empty")
	assert.NotContains(t, b.String(), "pending")
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
	assert.Contains(t, out, "inbox: 1 unread")
	assert.Contains(t, out, "msg-aaaaaaaaaaaa from VAL-x  type query")
}

// plantListener writes an ownership record by hand, so a test can describe an
// owner this process cannot be (a dead PID) or a generation it did not reach.
func plantListener(t *testing.T, dataDir, id string, pid, generation int) {
	t.Helper()
	rec := map[string]any{
		"listenerGeneration": generation,
		"listenerToken":      "0123456789abcdef",
		"listenerPID":        pid,
		"listenerClaimedAt":  time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.Marshal(rec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "sessions", id, "listener.json"), data, 0o600))
}

// A live owner in the ownership record → listening, with the OWNER's pid and the
// moment the wait started. The pid matters: overview prints a `kill` line next to
// it, so it has to name the process that actually holds the wait.
func TestBuildOverview_ListenerActive(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantOverviewSession(t, dataDir, "lsnov01", session.RoleEsc, "ESC-x", "/repo/x", "", "working")
	mgr := session.NewManager(dataDir, time.Second)
	owner, err := mgr.ClaimListener("lsnov01") // PID = os.Getpid() (live)
	require.NoError(t, err)

	rep, err := buildOverview(mgr, overviewTestCfg(dataDir), "lsnov01")
	require.NoError(t, err)
	assert.True(t, rep.ListenerActive, "a live owner → listening")
	assert.Equal(t, os.Getpid(), rep.ListenerPid)
	assert.Equal(t, owner.Generation, rep.ListenerGeneration)
	require.NotNil(t, rep.ListenerSince, "the wait has a start, and it is the claim")
	assert.WithinDuration(t, owner.ClaimedAt, *rep.ListenerSince, time.Second)
}

// The regression of the self-inflicted eviction cycle: a waiter that EXITS must
// not change what overview says about the one that replaced it.
//
// Sequence (the one that bit the VAL): owner A is waiting, owner B claims and
// evicts it, A then exits and runs its teardown. Before the fix, A's deferred
// SetWaitingSince(nil) cleared the marker B had just published, so overview told
// a listening session it was not listening — and the agent, following its own
// rule to re-arm when not listening, evicted its healthy waiter.
//
// Here A's exit is the honest in-process equivalent: everything a departing
// `next` still does. The assertion is that it is nothing.
func TestBuildOverview_DepartingWaiterDoesNotUnsetTheLiveOne(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantOverviewSession(t, dataDir, "lsnov05", session.RoleEsc, "ESC-x", "/repo/x", "", "working")
	mgr := session.NewManager(dataDir, time.Second)

	ownerA, err := mgr.StartWait("lsnov05")
	require.NoError(t, err)
	ownerB, err := mgr.StartWait("lsnov05") // B evicts A
	require.NoError(t, err)
	require.False(t, mgr.IsListenerCurrent("lsnov05", ownerA.Token), "A is evicted")
	require.True(t, mgr.IsListenerCurrent("lsnov05", ownerB.Token), "B owns the wait")

	rep, err := buildOverview(mgr, overviewTestCfg(dataDir), "lsnov05")
	require.NoError(t, err)
	assert.True(t, rep.ListenerActive, "B is waiting, and A leaving cannot say otherwise")
	assert.Equal(t, ownerB.Generation, rep.ListenerGeneration)
}

// A session that never waited has no ownership record at all → not listening,
// and no error on the observability path.
func TestBuildOverview_ListenerAbsent(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantOverviewSession(t, dataDir, "lsnov03", session.RoleEsc, "ESC-x", "/repo/x", "", "") // never claimed

	mgr := session.NewManager(dataDir, time.Second)
	rep, err := buildOverview(mgr, overviewTestCfg(dataDir), "lsnov03")
	require.NoError(t, err)
	assert.False(t, rep.ListenerActive, "no ownership record → not listening")
	assert.False(t, rep.ListenerReclaimPending)
	assert.Nil(t, rep.ListenerSince)
}

// The owner's process is gone (crash, kill) → not listening, EVEN THOUGH the
// manifest PID is this live test process. That combination is the whole point of
// reading the ownership record: the manifest PID belongs to whoever adopted the
// session last, which is not the same claim as "somebody is waiting right now".
func TestBuildOverview_ListenerOwnerProcessDead(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantOverviewSession(t, dataDir, "lsnov04", session.RoleEsc, "ESC-x", "/repo/x", "", "") // manifest PID = live
	plantListener(t, dataDir, "lsnov04", deadPID, 7)

	mgr := session.NewManager(dataDir, time.Second)
	rep, err := buildOverview(mgr, overviewTestCfg(dataDir), "lsnov04")
	require.NoError(t, err)
	assert.False(t, rep.ListenerActive, "dead owner → not listening, live manifest PID notwithstanding")
	assert.Equal(t, 7, rep.ListenerGeneration, "the generation is still observable")
}

var nowForOverviewTest = time.Now().UTC()

func TestPrintOverviewHuman_ListenerActive(t *testing.T) {
	t.Parallel()
	rep := overviewReport{
		Me:             overviewSelf{SessionID: "esc12345", AgentName: "ESC-x", Role: "esc", Stale: false},
		ListenerActive: true, ListenerSince: &nowForOverviewTest,
		ListenerPid: 4321,
		Inbox:       []overviewMsg{},
	}
	var b bytes.Buffer
	printOverviewHuman(&b, rep)
	out := b.String()
	assert.Contains(t, out, "listener: listening (PID 4321", "the listener line names the pid")
	assert.NotContains(t, out, "expires in", "there is no window left to expire (§2.2 rev. cdb21dc)")
	assert.Contains(t, out, "waiting since", "what is observable now is when the wait began")
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
	assert.Contains(t, out, "inbox: nothing unread")
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
	assert.False(t, rep.ListenerActive, "revoked is not listening — PID 0 is nobody's process")
	assert.Nil(t, rep.ListenerSince)
}

// TestPrintOverviewHuman_ListenerGenerationAndReclaim: the human line carries
// the generation when active, and the reclaim-pending hint otherwise.
func TestPrintOverviewHuman_ListenerGenerationAndReclaim(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	printOverviewHuman(&b, overviewReport{
		Me:             overviewSelf{SessionID: "esc12345", AgentName: "ESC-x", Role: "esc"},
		ListenerActive: true, ListenerPid: 4321, ListenerGeneration: 2,
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

// TestPrintOverviewHuman_ShowsHowToStopJustThisWaiter: the PID was always
// printed and neither operator thought to use it when it mattered — one
// `pkill -f "cab-bridge next"` killed four waiters at once. The scalpel has to
// be written next to the number, or the hammer wins.
func TestPrintOverviewHuman_ShowsHowToStopJustThisWaiter(t *testing.T) {
	t.Parallel()
	since := time.Now().UTC()
	var b bytes.Buffer
	printOverviewHuman(&b, overviewReport{
		Me:             overviewSelf{SessionID: "esc12345", AgentName: "ESC-x", Role: "esc"},
		ListenerActive: true, ListenerPid: 4321, ListenerSince: &since,
	})
	out := b.String()
	assert.Contains(t, out, "kill 4321", "the exact surgical command, next to the pid it needs")
	assert.Contains(t, out, "just this one", "and it must be obvious it targets ONE waiter")
}
