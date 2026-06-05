package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// TestRunOverview_SharedScope_WarnsStderrStdoutValidJSON is the B-1 vincolo #5:
// in a shared scope an id-free `overview --json` resolves the cwd session, warns
// on STDERR, and keeps STDOUT valid JSON (the warning must never pollute it).
func TestRunOverview_SharedScope_WarnsStderrStdoutValidJSON(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	escID, valID := sharedScopePair(t, dataDir)

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

// TestRunReceiveAny_SharedScope_WarnsStderrStdoutValidJSON: vincolo #5 for the
// other JSON-emitting chokepoint. `receive --any --emit=json` warns on stderr
// during resolution, then (with a short deadline and an empty inbox) exits 0 with
// a {"status":"timeout"} JSON payload on stdout — still valid JSON.
func TestRunReceiveAny_SharedScope_WarnsStderrStdoutValidJSON(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	_, valID := sharedScopePair(t, dataDir)

	var runErr error
	var stderr string
	stdout := captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			runErr = runReceive([]string{"--any", "--max-deadline=1", "--emit=json"})
		})
	})
	require.NoError(t, runErr, "an empty --any window exits 0 (F-24/F-36)")

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &payload), "stdout must stay valid JSON despite the warning")
	assert.Equal(t, "timeout", payload["status"])

	assert.Contains(t, stderr, "warning", "the shared-scope hazard warns on stderr")
	assert.Contains(t, stderr, valID, "the warning names the sibling")
}

// TestRunListen_SharedScope_NDJSONCleanWarnsStderr is the P3-2 listen leg of
// vincolo #5: `listen --wait-one --emit=json` resolves through the guardrail
// (warning on stderr), then on an empty short window exits 0 with a JSON timeout
// payload on stdout — the warning must not pollute the NDJSON stream.
func TestRunListen_SharedScope_NDJSONCleanWarnsStderr(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	_, valID := sharedScopePair(t, dataDir)

	var runErr error
	var stderr string
	stdout := captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			runErr = runListen([]string{"--wait-one", "--emit=json", "--until-deadline=1s", "--no-auto-ack"})
		})
	})
	require.NoError(t, runErr, "an empty --wait-one window exits 0 with a timeout payload (F-24)")

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &payload), "stdout must be a valid JSON timeout payload, not polluted by the warning")
	assert.Equal(t, "timeout", payload["status"])

	assert.Contains(t, stderr, "warning", "the shared-scope hazard warns on stderr")
	assert.Contains(t, stderr, valID, "the warning names the sibling")
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
	_, valID := sharedScopePair(t, dataDir)

	var runErr error
	_ = captureStdout(t, func() {
		_ = captureStderr(t, func() {
			runErr = runWhoami(nil) // id-free
		})
	})
	require.Error(t, runErr, "strict mode rejects the shared-scope hazard instead of silently picking")
	assert.Contains(t, runErr.Error(), valID, "the error names the sibling")
}
