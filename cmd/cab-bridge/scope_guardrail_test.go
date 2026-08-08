package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// plantSessionFull writes a manifest with caller-controlled ProjectPath + Scope
// (overview_test's plantOverviewSession hardcodes ProjectPath), needed to drive
// the B-1 guardrail through a real command whose cwd lookup must match a planted
// session and surface a shared-scope sibling.
func plantSessionFull(t *testing.T, dataDir, id, role, agentName, scope, projectPath, state string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "sessions", id), 0o700))
	mgr := session.NewManager(dataDir, time.Second)
	now := time.Now().UTC()
	mf := &session.Manifest{
		SessionID:     id,
		SchemaVersion: session.SchemaVersionV2,
		ProjectName:   filepath.Base(projectPath),
		ProjectPath:   projectPath,
		AgentName:     agentName,
		Role:          role,
		PID:           os.Getpid(),
		StartedAt:     now,
		LastHeartbeat: now,
		Status:        session.StatusActive,
		Capabilities:  []string{"query"},
		Scope:         scope,
		State:         state,
	}
	require.NoError(t, mgr.SaveManifest(mf))
}

// sharedScopePair plants a VAL@rootDir + ESC@cwd sharing one scope and chdirs
// into the ESC's project so an id-free command resolves the ESC and sees the VAL
// as a shared-scope sibling. Returns the ESC and VAL ids. Uses t.Chdir (no
// t.Parallel) and reads the post-chdir cwd so ProjectPath matches exactly even
// when the temp dir is under a symlink (/var -> /private/var on macOS).
func sharedScopePair(t *testing.T, dataDir string) (escID, valID string) {
	t.Helper()
	wtDir := t.TempDir()
	rootDir := t.TempDir()
	t.Chdir(wtDir)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	const scope = "/shared/repo"
	plantSessionFull(t, dataDir, "escwt001", session.RoleEsc, "ESC-x", scope, cwd, "working")
	plantSessionFull(t, dataDir, "valrt001", session.RoleVal, "VAL-x", scope, rootDir, session.StateOrchestrating)
	return "escwt001", "valrt001"
}

// sharedScopePrefixPair is sharedScopePair with the cwd a SUBDIRECTORY of the
// session's projectPath, i.e. a prefix match. That is the case the guardrail
// still warns about after F-91: the caller might genuinely be someone else who
// never registered (the LL-14 stress-test scenario).
func sharedScopePrefixPair(t *testing.T, dataDir string) (escID, valID string) {
	t.Helper()
	wtDir := t.TempDir()
	rootDir := t.TempDir()
	sub := filepath.Join(wtDir, "subdir")
	require.NoError(t, os.MkdirAll(sub, 0o700))
	t.Chdir(sub)
	const scope = "/shared/repo"
	plantSessionFull(t, dataDir, "escwt001", session.RoleEsc, "ESC-x", scope, wtDir, "working")
	plantSessionFull(t, dataDir, "valrt001", session.RoleVal, "VAL-x", scope, rootDir, session.StateOrchestrating)
	return "escwt001", "valrt001"
}

// TestRunWhoami_ExactMatchInSharedScope_IsSilent is the F-91 fix: with the
// command issued from the session's OWN working directory, siblings elsewhere in
// the scope do not weaken the resolution, so there is nothing to warn about.
//
// Before the fix this printed a multi-line warning before every id-free command
// — in the setup v0.8 makes normal, since peers must share a scope for
// name-based recipients to resolve — and its remediation named --session-id,
// which the v0.8 loop commands reject. A dead end, on every command.
func TestRunWhoami_ExactMatchInSharedScope_IsSilent(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	sharedScopePair(t, dataDir) // cwd == projectPath

	stderr := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			require.NoError(t, runWhoami(nil))
		})
	})
	assert.NotContains(t, stderr, "warning", "an exact match in a shared scope must be silent")
	assert.NotContains(t, stderr, "--session-id", "and must not advise a flag the loop commands reject")
}

// TestRunOverview_SharedScope_WarnsStderrStdoutValidJSON is the B-1 vincolo #5:
// in a shared scope an id-free `overview --json` resolves the cwd session, warns
// on STDERR, and keeps STDOUT valid JSON (the warning must never pollute it).
func TestRunOverview_SharedScope_WarnsStderrStdoutValidJSON(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	escID, valID := sharedScopePrefixPair(t, dataDir)

	var runErr error
	var stderr string
	stdout := captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			runErr = runOverview([]string{"--json"}) // id-free
		})
	})
	require.NoError(t, runErr)

	var rep overviewReport
	require.NoError(t, json.Unmarshal([]byte(stdout), &rep), "stdout must stay valid JSON despite the stderr warning")
	assert.Equal(t, escID, rep.Me.SessionID, "the cwd resolves to the ESC")

	assert.Contains(t, stderr, "warning", "the shared-scope hazard warns on stderr")
	assert.Contains(t, stderr, valID, "the warning names the sibling")
	assert.Contains(t, stderr, "--session-id="+escID, "and an executable remediation")
}

// TestRunOverview_ExplicitSessionID_NoWarning is vincolo #6: passing
// --session-id bypasses the guardrail entirely — no lookup, no warning — even in
// a shared scope.
func TestRunOverview_ExplicitSessionID_NoWarning(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	escID, _ := sharedScopePair(t, dataDir)

	var runErr error
	var stderr string
	_ = captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			runErr = runOverview([]string{"--session-id=" + escID, "--json"})
		})
	})
	require.NoError(t, runErr)
	assert.NotContains(t, stderr, "warning", "an explicit --session-id suppresses the guardrail warning")
}

// TestRunWhoami_StrictSharedScope_RejectsEndToEnd is the P3-2 strict leg: with
// CAB_BRIDGE_STRICT_SESSION_LOOKUP=1, an id-free command in a shared scope is
// REJECTED end-to-end (the warning is promoted to an error) — no silent pick.
// Complements the pure-predicate strict test in common_test.go.
func TestRunWhoami_StrictSharedScope_RejectsEndToEnd(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	t.Setenv("CAB_BRIDGE_STRICT_SESSION_LOOKUP", "1")
	_, valID := sharedScopePrefixPair(t, dataDir)

	var runErr error
	_ = captureStdout(t, func() {
		_ = captureStderr(t, func() {
			runErr = runWhoami(nil) // id-free
		})
	})
	require.Error(t, runErr, "strict mode rejects the shared-scope hazard instead of silently picking")
	assert.Contains(t, runErr.Error(), valID, "the error names the sibling")
}

// --- CAB_SESSION_ID in input (1c voce B) ------------------------------------

// TestResolveCurrentSession_EnvPrecedence pins the ladder
// --session-id > CAB_SESSION_ID > lookup-by-cwd, and the fail-closed behaviour
// that makes the middle rung trustworthy.
func TestResolveCurrentSession_EnvPrecedence(t *testing.T) {
	setup := func(t *testing.T) (*session.Manager, string, string) {
		t.Helper()
		dataDir := t.TempDir()
		t.Setenv("CAB_DATA_DIR", dataDir)
		t.Setenv("CAB_AUTO_GC_HOURS", "0")
		escID, valID := sharedScopePair(t, dataDir) // cwd == ESC's projectPath
		cfg := config.DefaultConfig()
		cfg.DataDir = dataDir
		return newSessionManager(cfg), escID, valID
	}

	t.Run("env_wins_over_cwd", func(t *testing.T) {
		mgr, escID, valID := setup(t)
		t.Setenv("CAB_SESSION_ID", valID)
		got, err := resolveCurrentSession(mgr, "next", "")
		require.NoError(t, err)
		assert.Equal(t, valID, got, "the environment decides, not the directory")
		assert.NotEqual(t, escID, got)
	})

	t.Run("flag_wins_over_env", func(t *testing.T) {
		mgr, escID, valID := setup(t)
		t.Setenv("CAB_SESSION_ID", valID)
		got, err := resolveCurrentSession(mgr, "read", escID)
		require.NoError(t, err)
		assert.Equal(t, escID, got, "an explicit flag outranks the environment")
	})

	t.Run("cwd_used_when_env_unset", func(t *testing.T) {
		mgr, escID, _ := setup(t)
		got, err := resolveCurrentSession(mgr, "next", "")
		require.NoError(t, err)
		assert.Equal(t, escID, got)
	})

	t.Run("malformed_env_is_an_error_not_a_fallback", func(t *testing.T) {
		mgr, _, _ := setup(t)
		t.Setenv("CAB_SESSION_ID", "NOT A VALID ID")
		_, err := resolveCurrentSession(mgr, "next", "")
		require.Error(t, err, "a bad value must never be ignored in favour of the cwd")
		assert.Contains(t, err.Error(), "CAB_SESSION_ID")
	})

	t.Run("nonexistent_env_session_is_an_error", func(t *testing.T) {
		mgr, _, _ := setup(t)
		t.Setenv("CAB_SESSION_ID", "deadbe01")
		_, err := resolveCurrentSession(mgr, "next", "")
		require.Error(t, err, "a stale export must be reported, not silently replaced by the cwd")
		assert.Contains(t, err.Error(), "does not exist")
	})

	t.Run("empty_env_is_treated_as_unset", func(t *testing.T) {
		mgr, escID, _ := setup(t)
		t.Setenv("CAB_SESSION_ID", "   ")
		got, err := resolveCurrentSession(mgr, "next", "")
		require.NoError(t, err)
		assert.Equal(t, escID, got)
	})
}
