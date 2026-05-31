package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeriveAgentName_Scenario covers the F-40 adaptive naming rule: inherit a
// peer's suffix, or fall back to <ROLE>-<scope-basename>.
func TestDeriveAgentName_Scenario(t *testing.T) {
	tests := []struct {
		name      string
		myRole    string
		scopeBase string
		peers     []peerSummary
		wantName  string
		wantBasis string
	}{
		{
			name:      "no peer falls back to scope basename",
			myRole:    "esc",
			scopeBase: "cli-agents-bridge",
			peers:     nil,
			wantName:  "ESC-cli-agents-bridge",
			wantBasis: "scope-basename",
		},
		{
			name:      "inherits peer VAL-cab suffix",
			myRole:    "esc",
			scopeBase: "cli-agents-bridge",
			peers:     []peerSummary{{SessionID: "aaa", Role: "val", AgentName: "VAL-cab"}},
			wantName:  "ESC-cab",
			wantBasis: "peer:aaa",
		},
		{
			name:      "inherits peer ESC-base suffix (val deriving)",
			myRole:    "val",
			scopeBase: "proj",
			peers:     []peerSummary{{SessionID: "bbb", Role: "esc", AgentName: "ESC-proj"}},
			wantName:  "VAL-proj",
			wantBasis: "peer:bbb",
		},
		{
			name:      "non-pattern peer name falls back to basename",
			myRole:    "esc",
			scopeBase: "proj",
			peers:     []peerSummary{{SessionID: "ccc", Role: "val", AgentName: "orchestrator"}},
			wantName:  "ESC-proj",
			wantBasis: "scope-basename",
		},
		{
			name:      "peer prefix with empty suffix falls back",
			myRole:    "esc",
			scopeBase: "proj",
			peers:     []peerSummary{{SessionID: "ddd", Role: "val", AgentName: "VAL-"}},
			wantName:  "ESC-proj",
			wantBasis: "scope-basename",
		},
		{
			name:      "degenerate scope basename becomes session",
			myRole:    "esc",
			scopeBase: ".",
			peers:     nil,
			wantName:  "ESC-session",
			wantBasis: "scope-basename",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, basis := deriveAgentName(tt.myRole, tt.scopeBase, tt.peers)
			assert.Equal(t, tt.wantName, name, "name")
			assert.Equal(t, tt.wantBasis, basis, "basis")
		})
	}
}

// TestSelectPeer_Scenario covers peer selection: complementary role preferred,
// same role excluded, most-recent among ties.
func TestSelectPeer_Scenario(t *testing.T) {
	t0 := time.Date(2026, 5, 31, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)

	t.Run("no peers", func(t *testing.T) {
		_, ok := selectPeer("esc", nil)
		assert.False(t, ok)
	})
	t.Run("same role only is excluded", func(t *testing.T) {
		_, ok := selectPeer("val", []peerSummary{{SessionID: "x", Role: "val", AgentName: "VAL-y"}})
		assert.False(t, ok)
	})
	t.Run("complementary role chosen", func(t *testing.T) {
		p, ok := selectPeer("esc", []peerSummary{
			{SessionID: "obs", Role: "observer", AgentName: "OBSERVER-z", LastHeartbeat: t1},
			{SessionID: "val", Role: "val", AgentName: "VAL-z", LastHeartbeat: t0},
		})
		require.True(t, ok)
		assert.Equal(t, "val", p.Role, "complement beats a more-recent non-complement")
	})
	t.Run("most recent among complements", func(t *testing.T) {
		p, ok := selectPeer("esc", []peerSummary{
			{SessionID: "old", Role: "val", AgentName: "VAL-old", LastHeartbeat: t0},
			{SessionID: "new", Role: "val", AgentName: "VAL-new", LastHeartbeat: t1},
		})
		require.True(t, ok)
		assert.Equal(t, "new", p.SessionID)
	})
}

// TestBootstrap_TwoFreshAgentsConverge is the F-40 acceptance test: two fresh
// agents, in sequence, with NO config (no agent-name, no team, no data dir
// beyond the test sandbox), end up with a matching VAL-x / ESC-x pair on disk.
//
// It reproduces the REAL deployment topology: DISJOINT cwds sharing one git root
// (a val at the repo root, an esc in a subdir). That gives them the SAME scope
// (so they discover each other for adaptive naming) but DIFFERENT projectPaths
// (so register's per-project BUG-6 collision never fires). A shared
// --project-path would collide in-process — the test runner PID is live, unlike
// the real register-then-die lifecycle — which is a test artifact, not a code
// fault; the separate-process smoke confirms the real path works.
func TestBootstrap_TwoFreshAgentsConverge(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	// A git root shared by both, with the esc one level down — the real VAL@root
	// / ESC@subdir layout. The .git marker makes FindProjectRoot return `root` as
	// the scope for both cwds (lexical walk-up, no symlink resolution).
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o700))
	sub := filepath.Join(root, "docs")
	require.NoError(t, os.Mkdir(sub, 0o700))
	scopeBase := filepath.Base(root)

	// val first: lone bootstrap, no peer -> VAL-<base>, sets orchestrating.
	require.NoError(t, runBootstrap([]string{"--role=val", "--project-path=" + root, "--no-listen"}))
	// esc second from the subdir: same scope -> sees VAL-<base> -> derives ESC-<base>.
	require.NoError(t, runBootstrap([]string{"--role=esc", "--project-path=" + sub, "--no-listen"}))

	mgr := session.NewManager(dataDir, time.Second)
	byRole := map[string]*session.Manifest{}
	ids, err := listSessionIDs(t, dataDir)
	require.NoError(t, err)
	require.Len(t, ids, 2, "exactly two sessions registered")
	for _, id := range ids {
		mf, lerr := mgr.LoadManifest(id)
		require.NoError(t, lerr)
		byRole[mf.Role] = mf
	}

	valMf := byRole["val"]
	escMf := byRole["esc"]
	require.NotNil(t, valMf, "a val session exists")
	require.NotNil(t, escMf, "an esc session exists")
	assert.Equal(t, "VAL-"+scopeBase, valMf.AgentName)
	assert.Equal(t, "ESC-"+scopeBase, escMf.AgentName)
	assert.Equal(t, session.StateOrchestrating, valMf.State, "val is set orchestrating")
	// Both resolve to the git root -> shared scope -> they see each other.
	assert.Equal(t, root, valMf.Scope)
	assert.Equal(t, valMf.Scope, escMf.Scope)
}

// TestBootstrap_ResumeIsIdempotent verifies a second identical bootstrap resumes
// the same session id rather than creating a duplicate (F-27 reuse).
func TestBootstrap_ResumeIsIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	proj := t.TempDir()

	require.NoError(t, runBootstrap([]string{"--role=esc", "--project-path=" + proj, "--no-listen"}))
	ids1, err := listSessionIDs(t, dataDir)
	require.NoError(t, err)
	require.Len(t, ids1, 1)

	// Reproduce register-then-die: bootstrap/register is one-shot, so in
	// production its PID is dead by the time a second bootstrap runs — which is
	// exactly what lets --resume reclaim the session. In-process the test runner
	// PID stays live, so we stamp the manifest with a dead PID (the same technique
	// reconnect_test.writeManifestAt uses). The separate-process smoke needs no
	// such stamp; this is the faithful unit-level stand-in.
	mgr := session.NewManager(dataDir, time.Second)
	mf, err := mgr.LoadManifest(ids1[0])
	require.NoError(t, err)
	mf.PID = deadPID
	require.NoError(t, mgr.SaveManifest(mf))

	// Second bootstrap, same identity, dead owner -> resume, not a new session.
	require.NoError(t, runBootstrap([]string{"--role=esc", "--project-path=" + proj, "--no-listen"}))
	ids2, err := listSessionIDs(t, dataDir)
	require.NoError(t, err)
	assert.Equal(t, ids1, ids2, "same session id resumed, no duplicate")
}

// TestBootstrap_RoleRequired rejects a missing --role.
func TestBootstrap_RoleRequired(t *testing.T) {
	t.Setenv("CAB_DATA_DIR", t.TempDir())
	err := runBootstrap([]string{"--project-path=" + t.TempDir()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--role is required")
}

// listSessionIDs returns the session directory names under dataDir/sessions.
func listSessionIDs(t *testing.T, dataDir string) ([]string, error) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}
