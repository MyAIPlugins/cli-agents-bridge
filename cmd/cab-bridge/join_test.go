package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	peers, _, err := collectPeers(mgr, dataDir, cfg.StaleSeconds, true, "", scope)
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
	peers, _, err := collectPeers(mgr, dataDir, cfg.StaleSeconds, true, "", scope)
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
