package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// plantScopedSession writes a fresh-heartbeat manifest with an explicit scope
// (and optional team) so the collectPeers scope filter can be driven directly,
// without os.Chdir — the cwd->scope derivation itself is covered by scope_test.
func plantScopedSession(t *testing.T, dataDir, id, scope, team string) {
	t.Helper()
	sessionDir := filepath.Join(dataDir, "sessions", id)
	require.NoError(t, os.MkdirAll(sessionDir, 0o700))
	mgr := session.NewManager(dataDir, time.Second)
	now := time.Now().UTC()
	mf := &session.Manifest{
		SessionID:     id,
		SchemaVersion: session.SchemaVersionV2,
		ProjectName:   "proj-" + id,
		ProjectPath:   filepath.Join(dataDir, "proj-"+id),
		AgentName:     "agent-" + id,
		Role:          session.RoleEsc,
		PID:           os.Getpid(),
		StartedAt:     now,
		LastHeartbeat: now,
		Status:        session.StatusActive,
		Capabilities:  []string{"query"},
		Scope:         scope,
		TeamID:        team,
	}
	require.NoError(t, mgr.SaveManifest(mf))
}

func ids(peers []peerSummary) []string {
	out := make([]string, 0, len(peers))
	for _, p := range peers {
		out = append(out, p.SessionID)
	}
	return out
}

// TestCollectPeers_DefaultScopeFilter_HidesOtherScopes is the F-17 core: with a
// non-empty scopeFilter, only matching-scope sessions are returned and the
// hidden-by-scope count reports how many --all-scopes would reveal.
func TestCollectPeers_DefaultScopeFilter_HidesOtherScopes(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantScopedSession(t, dataDir, "valaaaaa", "/root/A", "")
	plantScopedSession(t, dataDir, "escaaaaa", "/root/A", "")
	plantScopedSession(t, dataDir, "escbbbbb", "/root/B", "")

	peers, hidden, err := collectPeers(session.NewManager(dataDir, time.Second), dataDir, 300, 65536, true, "", "/root/A")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"valaaaaa", "escaaaaa"}, ids(peers), "only scope /root/A sessions returned")
	assert.Equal(t, 1, hidden, "the /root/B session is counted as hidden-by-scope")
}

// TestCollectPeers_AllScopes_NoFilter: an empty scopeFilter returns every
// session and never reports anything hidden.
func TestCollectPeers_AllScopes_NoFilter(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantScopedSession(t, dataDir, "valaaaaa", "/root/A", "")
	plantScopedSession(t, dataDir, "escbbbbb", "/root/B", "")

	peers, hidden, err := collectPeers(session.NewManager(dataDir, time.Second), dataDir, 300, 65536, true, "", "")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"valaaaaa", "escbbbbb"}, ids(peers))
	assert.Equal(t, 0, hidden)
}

// TestCollectPeers_LegacyEmptyScope_HiddenByDefault_ShownWithAll: a legacy /
// pre-F-17 session has scope=="" so it never matches a scope filter — hidden by
// default, visible only with --all-scopes (empty scopeFilter).
func TestCollectPeers_LegacyEmptyScope_HiddenByDefault_ShownWithAll(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantScopedSession(t, dataDir, "scopedaa", "/root/A", "")
	plantScopedSession(t, dataDir, "legacyaa", "", "") // pre-F-17: no scope

	mgr := session.NewManager(dataDir, time.Second)

	filtered, hidden, err := collectPeers(mgr, dataDir, 300, 65536, true, "", "/root/A")
	require.NoError(t, err)
	assert.Equal(t, []string{"scopedaa"}, ids(filtered), "legacy empty-scope session hidden by default")
	assert.Equal(t, 1, hidden)

	all, hiddenAll, err := collectPeers(mgr, dataDir, 300, 65536, true, "", "")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"scopedaa", "legacyaa"}, ids(all), "--all-scopes reveals the legacy session")
	assert.Equal(t, 0, hiddenAll)
}

// captureStdoutStderr redirects both streams for the duration of fn and returns
// what each captured. The scope filter writes its hint to stderr while the table
// /JSON goes to stdout, so a test must inspect both.
func captureStdoutStderr(t *testing.T, fn func()) (string, string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	require.NoError(t, err)
	rErr, wErr, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout, os.Stderr = wOut, wErr
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()

	fn()
	require.NoError(t, wOut.Close())
	require.NoError(t, wErr.Close())
	outData, err := io.ReadAll(rOut)
	require.NoError(t, err)
	errData, err := io.ReadAll(rErr)
	require.NoError(t, err)
	return string(outData), string(errData)
}

// TestRunPeers_ScopeFiltering exercises the full wiring: runPeers derives the
// cwd's scope (t.Chdir into a real .git project), filters to it by default with
// the hidden-count hint, shows everything with --all-scopes, and lets --team
// bypass the scope filter (H3).
func TestRunPeers_ScopeFiltering(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o700))
	t.Chdir(root)

	// Derive the scope exactly as runPeers will (robust to the macOS
	// /var -> /private/var getwd symlink resolution), then plant against it.
	cwd, err := os.Getwd()
	require.NoError(t, err)
	wantScope := resolveScope(cwd)
	require.NotEmpty(t, wantScope)

	plantScopedSession(t, dataDir, "inscope1", wantScope, "")
	plantScopedSession(t, dataDir, "outscope1", wantScope+"-elsewhere", "")
	plantScopedSession(t, dataDir, "teamed01", "/some/other/root", "shared")

	t.Run("default filters to cwd scope and hints", func(t *testing.T) {
		stdout, stderr := captureStdoutStderr(t, func() {
			require.NoError(t, runPeers([]string{"--json"}))
		})
		assert.Contains(t, stdout, "inscope1")
		assert.NotContains(t, stdout, "outscope1")
		assert.NotContains(t, stdout, "teamed01")
		assert.Contains(t, stderr, "hidden — use --all-scopes")
	})

	t.Run("--all-scopes shows everything, no hint", func(t *testing.T) {
		stdout, stderr := captureStdoutStderr(t, func() {
			require.NoError(t, runPeers([]string{"--json", "--all-scopes"}))
		})
		assert.Contains(t, stdout, "inscope1")
		assert.Contains(t, stdout, "outscope1")
		assert.Contains(t, stdout, "teamed01")
		assert.NotContains(t, stderr, "hidden")
	})

	t.Run("--team bypasses scope filter (H3)", func(t *testing.T) {
		stdout, _ := captureStdoutStderr(t, func() {
			require.NoError(t, runPeers([]string{"--json", "--team=shared"}))
		})
		assert.Contains(t, stdout, "teamed01", "team filter is cross-scope by design")
		assert.NotContains(t, stdout, "inscope1")
	})
}

// --- F-112: peers says who is actually listening ----------------------------

// TestPeers_ListeningAndOverviewCannotDisagree is the F-112 regression, and it
// is written as a COMPARISON on purpose.
//
// The finding was not "peers is wrong": it was that peers and overview answered
// the same question independently, so they could disagree in the same instant —
// overview said `listener: not listening` while peers showed `ok`, about the same
// session, one minute after the process died. Pinning peers alone would leave the
// two free to drift apart again; pinning them AGAINST EACH OTHER is what the
// single source (ListenerOwner.Listening) buys.
func TestPeers_ListeningAndOverviewCannotDisagree(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)

	const scope = "/repo/live"
	// A fresh heartbeat on all three: this is the F-112 window, where the
	// heartbeat is frozen at the instant the one-shot exited and STALE still
	// reads `ok` — correctly, because the session is not abandoned.
	plantSessionFull(t, dataDir, "lsnalive", session.RoleEsc, "ESC-alive", scope, scope, "working")
	plantSessionFull(t, dataDir, "lsndead0", session.RoleEsc, "ESC-dead", scope, scope, "working")
	plantSessionFull(t, dataDir, "lsnnever", session.RoleVal, "VAL-never", scope, scope, session.StateOrchestrating)
	plantListener(t, dataDir, "lsnalive", os.Getpid(), 3)
	plantListener(t, dataDir, "lsndead0", deadPID, 3)
	// lsnnever: no listener record at all.

	peers, _, err := collectPeers(mgr, dataDir, cfg.StaleSeconds, 65536, true, "", scope)
	require.NoError(t, err)
	got := map[string]peerSummary{}
	for _, p := range peers {
		got[p.SessionID] = p
	}

	require.NotNil(t, got["lsnalive"].Listening)
	assert.True(t, *got["lsnalive"].Listening)
	assert.Equal(t, os.Getpid(), got["lsnalive"].PID, "the PID is the one holding the wait, the one a kill would reach")

	require.NotNil(t, got["lsndead0"].Listening)
	assert.False(t, *got["lsndead0"].Listening, "the record is there and the process is not")
	assert.Equal(t, 0, got["lsndead0"].PID, "no process to signal, so no number to offer")
	assert.False(t, got["lsndead0"].Stale, "and STALE stays ok: a fresh heartbeat means not abandoned, which is a different fact")

	assert.Nil(t, got["lsnnever"].Listening, "never listened is not the same as not listening now")

	// The comparison. Same instant, same fact, both commands.
	for _, sid := range []string{"lsnalive", "lsndead0", "lsnnever"} {
		rep, berr := buildOverview(mgr, cfg, sid)
		require.NoError(t, berr)
		peerSaysListening := got[sid].Listening != nil && *got[sid].Listening
		assert.Equal(t, rep.ListenerActive, peerSaysListening,
			"peers and overview must answer identically for %s", sid)
	}
}

func TestListeningCol(t *testing.T) {
	t.Parallel()
	yes, no := true, false
	assert.Equal(t, "yes", listeningCol(&yes))
	assert.Equal(t, "no", listeningCol(&no))
	assert.Equal(t, "-", listeningCol(nil), "never listened reads as absence, not as a fault")
	assert.Equal(t, "-", listenerPIDCol(0), "a zero printed as 0 is a number somebody would try to kill")
	assert.Equal(t, "4321", listenerPIDCol(4321))
}
