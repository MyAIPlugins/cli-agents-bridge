package main

import (
	"bytes"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// TestJoin_PrintsEveryoneNotAPickedPeer is the F-92 shape: with three agents
// alive, a command that names ONE of them is wrong however it chooses. The list
// cannot be wrong that way — and "peer: none" with three peers alive is what
// sent an agent into passive waiting while its brief was already in flight.
func TestJoin_PrintsEveryoneNotAPickedPeer(t *testing.T) {
	dataDir := t.TempDir()
	const scope = "/repo/group"
	plantSessionFull(t, dataDir, "valgrp01", session.RoleVal, "VAL-g", scope, "/repo/group", session.StateOrchestrating)
	plantSessionFull(t, dataDir, "crigrp01", session.RoleEsc, "CRI-g", scope, "/repo/group/docs", "working")
	plantSessionFull(t, dataDir, "cr2grp01", session.RoleEsc, "CRI2-g", scope, "/repo/group/cmd", "working")

	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)
	peers, _, err := collectPeers(mgr, dataDir, cfg.StaleSeconds, 65536, true, "", scope)
	require.NoError(t, err)

	here := othersHere(peers, "valgrp01")
	require.Len(t, here, 2, "everyone else, not a chosen one")

	var names []string
	for _, p := range here {
		names = append(names, p.AgentName)
	}
	assert.Equal(t, []string{"CRI-g", "CRI2-g"}, names, "deterministic order")

	var out bytes.Buffer
	require.NoError(t, printJoinReport(&out, joinReport{
		SessionID: "valgrp01", AgentName: "VAL-g", Role: session.RoleVal, Action: "resumed", Here: here,
		Hint: "run next to receive work",
	}))
	assert.Contains(t, out.String(), "CRI-g")
	assert.Contains(t, out.String(), "CRI2-g")
	assert.Contains(t, out.String(), "here with you (2)")
}

func TestJoin_ReportsAnEmptyRoomHonestly(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, printJoinReport(&out, joinReport{
		SessionID: "solo0001", AgentName: "ESC-solo", Role: session.RoleEsc, Action: "registered-new",
		Here: []joinPeer{}, Hint: "run next to receive work",
	}))
	assert.Contains(t, out.String(), "nobody else is here yet")
}

// TestJoin_NameClashStopsAndAsks is F-90: joining under a different name in a
// directory that already holds a session of that role must NOT create a second
// one — that leaves two sessions on one project path and blocks every command
// resolving by directory afterwards.
func TestJoin_NameClashStopsAndAsks(t *testing.T) {
	dataDir := t.TempDir()
	proj := t.TempDir()
	const scope = "/repo/clash"
	plantSessionFull(t, dataDir, "escold01", session.RoleEsc, "ESC-old", scope, proj, "working")

	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)
	peers, _, err := collectPeers(mgr, dataDir, cfg.StaleSeconds, 65536, true, "", scope)
	require.NoError(t, err)

	occupant, clash := findNameClash(mgr, peers, session.RoleEsc, proj, "ESC-new")
	require.True(t, clash, "a different name on the same (role, projectPath) is the F-90 shape")
	assert.Equal(t, "ESC-old", occupant.AgentName)

	// Same name is a resume, not a clash.
	_, clash = findNameClash(mgr, peers, session.RoleEsc, proj, "ESC-old")
	assert.False(t, clash)

	// A different ROLE in the same directory is not a clash either.
	_, clash = findNameClash(mgr, peers, session.RoleVal, proj, "VAL-x")
	assert.False(t, clash)

	// Nor is the same name in a DIFFERENT directory.
	_, clash = findNameClash(mgr, peers, session.RoleEsc, filepath.Join(proj, "sub"), "ESC-new")
	assert.False(t, clash)
}

// The message is the feature here, so it is asserted like one. A fresh agent
// meeting this error on its FIRST command has to be able to pick the right road
// from the text alone: the previous wording offered resuming and --force-new as
// equals, and the wrong one is not recoverable in the same breath.
func TestJoin_NameClashRecommendsResumingAndSaysWhatItKnows(t *testing.T) {
	t.Parallel()
	old := time.Now().UTC().Add(-2 * time.Hour)
	dead := peerSummary{SessionID: "escold01", AgentName: "ESC-bridge", Role: session.RoleEsc, LastHeartbeat: old, Stale: true}
	live := peerSummary{SessionID: "escold01", AgentName: "ESC-bridge", Role: session.RoleEsc, LastHeartbeat: time.Now().UTC(), Stale: false}

	t.Run("a_derived_name_says_the_rule_changed_and_recommends_resuming", func(t *testing.T) {
		msg := nameClashError(dead, session.RoleEsc, "ESC-esc-v08", true).Error()
		assert.Contains(t, msg, "Continue as it:  cab-bridge join --role=esc --agent-name=ESC-bridge",
			"the road that works must be spelled out, ready to run")
		assert.Contains(t, msg, "SAME agent, not a second one", "the likely cause, stated as likely")
		assert.Contains(t, msg, "no sign of life", "liveness is a fact, and here it points at 'you, from before'")
		assert.Contains(t, msg, "Only if you are a genuinely different agent",
			"--force-new is the exception, and reads like one")
		assert.Contains(t, msg, "ambiguous", "with its cost attached")
		assert.Less(t, strings.Index(msg, "Continue as it"), strings.Index(msg, "--force-new"),
			"the recommended road comes FIRST — the order is half the message")
		assert.Contains(t, msg, "an esc session", "English article, since an agent reads this sentence")
	})

	t.Run("a_live_occupant_does_not_claim_to_know_whose_it_is", func(t *testing.T) {
		msg := nameClashError(live, session.RoleEsc, "ESC-esc-v08", true).Error()
		assert.Contains(t, msg, "ALIVE", "a live session is a different situation and says so")
		assert.Contains(t, msg, "another agent is working in this directory right now",
			"with a live occupant the message must NOT assert that it is you")
		assert.NotContains(t, msg, "almost certainly yours")
	})

	t.Run("an_explicit_name_is_not_explained_as_a_schema_change", func(t *testing.T) {
		msg := nameClashError(dead, session.RoleEsc, "ESC-mine", false).Error()
		assert.Contains(t, msg, `You asked to join as "ESC-mine"`)
		assert.NotContains(t, msg, "Today's rule", "nothing was derived, so there is no rule to blame")
	})
}

func TestRunJoin_RequiresRole(t *testing.T) {
	t.Setenv("CAB_DATA_DIR", t.TempDir())
	err := runJoin(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--role")
}

// TestRunJoin_EndToEndIsIdempotent: joining twice from the same directory is a
// resume, never a second session — the whole point of replacing register.
func TestRunJoin_EndToEndIsIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	proj := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	first := captureStdout(t, func() {
		require.NoError(t, runJoin([]string{"--role=esc", "--agent-name=ESC-j", "--project-path=" + proj}))
	})
	assert.Contains(t, first, "registered-new")

	second := captureStdout(t, func() {
		require.NoError(t, runJoin([]string{"--role=esc", "--agent-name=ESC-j", "--project-path=" + proj}))
	})
	assert.Contains(t, second, "resumed", "the second join must resume, not create")

	entries, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "exactly one session on this project path")

	// And the clash guard fires end-to-end on a different name.
	err = runJoin([]string{"--role=esc", "--agent-name=ESC-other", "--project-path=" + proj})
	require.Error(t, err, "a different name must stop and ask")
	assert.Contains(t, err.Error(), "ESC-j", "the error names the existing session")
	assert.Contains(t, err.Error(), "--force-new", "and the deliberate way out")
	assert.True(t, strings.Contains(err.Error(), "join --role=esc"), "with a runnable command")
}

// TestJoin_ReplaysOpenAsksAcrossLives is the F-34 cross-life gap (CRI2 P1-4).
//
// Within one life the guard holds — collectOpenAsks only counts NOTIFIED. But
// the cursor only ever grew: a page of `next` lost to a compact left its asks
// NOTIFIED forever, no later `next` re-showed them, and the next reply to that
// sender CLOSED them without this life having ever seen them.
func TestJoin_ReplaysOpenAsksAcrossLives(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)

	const sid = "jrepl001"
	plantOverviewSession(t, dataDir, sid, session.RoleEsc, "ESC-r", "/repo/r", "", "working")

	now := time.Now().UTC()
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "the brief", now)
	plantInboxAt(t, dataDir, sid, "msg-bbbbbbbbbbbb", "valxxx01", message.TypeNotify, "an update", now)
	plantInboxAt(t, dataDir, sid, "msg-cccccccccccc", "valxxx01", message.TypeResponse, "an answer", now)
	_, err := mgr.CommitWakeCursor(sid, []string{"msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb", "msg-cccccccccccc"}, now, nil, nil)
	require.NoError(t, err)

	n, err := replayOpenAsks(mgr, cfg, sid)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the ask is replayed")

	cursor, _, err := mgr.ReadWakeCursor(sid)
	require.NoError(t, err)
	assert.False(t, cursor.IsNotified("msg-aaaaaaaaaaaa"), "the ask goes back to UNREAD")
	assert.True(t, cursor.IsNotified("msg-bbbbbbbbbbbb"), "a tell is one-shot: never re-woken")
	assert.True(t, cursor.IsNotified("msg-cccccccccccc"), "a response is one-shot too")
}

// TestForgetNotified_IsIdempotentAndBounded: replaying twice changes nothing
// more, and unknown ids are simply absent.
func TestForgetNotified_IsIdempotentAndBounded(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)

	const sid = "jrepl002"
	plantOverviewSession(t, dataDir, sid, session.RoleEsc, "ESC-r2", "/repo/r2", "", "working")
	now := time.Now().UTC()
	_, err := mgr.CommitWakeCursor(sid, []string{"msg-aaaaaaaaaaaa"}, now, nil, nil)
	require.NoError(t, err)

	require.NoError(t, mgr.ForgetNotified(sid, []string{"msg-aaaaaaaaaaaa"}))
	require.NoError(t, mgr.ForgetNotified(sid, []string{"msg-aaaaaaaaaaaa"}))
	require.NoError(t, mgr.ForgetNotified(sid, nil))
	require.NoError(t, mgr.ForgetNotified(sid, []string{"msg-neverexisted"}))

	cursor, _, err := mgr.ReadWakeCursor(sid)
	require.NoError(t, err)
	assert.Empty(t, cursor.Notified)
}

// TestJoin_NameTakenElsewhereNamesAReachablePlace: an error that names a place
// must name one the reader can go to.
//
// It used to print ProjectName, which is filepath.Base — so "run this from
// cridir" pointed at something that is not a directory, and a repo can hold
// several with that name. Same dead-end class this command had just closed by
// dropping the reference to `bootstrap`.
func TestJoin_NameTakenElsewhereNamesAReachablePlace(t *testing.T) {
	dataDir := t.TempDir()
	base := t.TempDir()
	occupantDir := filepath.Join(base, "cridir")
	mineDir := filepath.Join(base, "valdir")
	require.NoError(t, os.MkdirAll(occupantDir, 0o700))
	require.NoError(t, os.MkdirAll(mineDir, 0o700))

	const scope = "/repo/shared"
	plantSessionFull(t, dataDir, "occup001", session.RoleVal, "VAL-x", scope, occupantDir, session.StateOrchestrating)

	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)
	peers, _, err := collectPeers(mgr, dataDir, cfg.StaleSeconds, 65536, true, "", scope)
	require.NoError(t, err)

	occupant, path, clash := findNameElsewhere(mgr, peers, session.RoleVal, mineDir, "VAL-x")
	require.True(t, clash)
	assert.Equal(t, "occup001", occupant.SessionID)
	assert.Equal(t, occupantDir, path, "the full path, not the basename")
	assert.True(t, filepath.IsAbs(path), "an agent must be able to cd into what the error names")

	// Same directory is a resume, not a clash.
	_, _, clash = findNameElsewhere(mgr, peers, session.RoleVal, occupantDir, "VAL-x")
	assert.False(t, clash)
}
