package main

import (
	"bytes"
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

const deadPID = 999999 // unlikely to exist; see internal/session TestIsProcessAlive

// plantSession writes a manifest with an explicit PID + heartbeat under
// dataDir/sessions/<id>/ so the auto-gc path can be driven deterministically.
func plantSession(t *testing.T, dataDir, id string, pid int, heartbeat time.Time) {
	t.Helper()
	sessionDir := filepath.Join(dataDir, "sessions", id)
	require.NoError(t, os.MkdirAll(sessionDir, 0o700))
	mgr := session.NewManager(dataDir, time.Second)
	mf := &session.Manifest{
		SessionID:     id,
		SchemaVersion: session.SchemaVersionV2,
		ProjectName:   "proj-" + id,
		ProjectPath:   filepath.Join(dataDir, "proj-"+id),
		AgentName:     "agent-" + id,
		Role:          session.RoleEsc,
		PID:           pid,
		StartedAt:     heartbeat,
		LastHeartbeat: heartbeat,
		Status:        session.StatusActive,
		Capabilities:  []string{"query"},
	}
	require.NoError(t, mgr.SaveManifest(mf))
}

func sessionExists(dataDir, id string) bool {
	_, err := os.Stat(filepath.Join(dataDir, "sessions", id))
	return err == nil
}

// TestRunAutoGC_RemovesOrphanAndLogs covers the cmd-side glue: a dead-PID +
// stale-heartbeat session is swept and an explicit line is logged, while a
// live-PID session survives untouched.
func TestRunAutoGC_RemovesOrphanAndLogs(t *testing.T) {
	dataDir := t.TempDir()
	old := time.Now().UTC().Add(-48 * time.Hour)
	plantSession(t, dataDir, "orphan01", deadPID, old)
	plantSession(t, dataDir, "alive001", os.Getpid(), old)

	var buf bytes.Buffer
	removed := runAutoGC(config.Config{DataDir: dataDir, AutoGCHours: 24}, &buf)

	require.Len(t, removed, 1)
	assert.Equal(t, "orphan01", removed[0].SessionID)

	assert.False(t, sessionExists(dataDir, "orphan01"), "orphan must be swept")
	assert.True(t, sessionExists(dataDir, "alive001"), "live session must survive")

	log := buf.String()
	assert.Contains(t, log, "auto-gc removed orphan session orphan01")
	assert.Contains(t, log, "pid 999999 dead")
}

// TestRunAutoGC_DisabledIsNoOp covers AutoGCHours<=0: nil result, nothing
// removed, nothing logged.
func TestRunAutoGC_DisabledIsNoOp(t *testing.T) {
	dataDir := t.TempDir()
	plantSession(t, dataDir, "orphan01", deadPID, time.Now().UTC().Add(-48*time.Hour))

	var buf bytes.Buffer
	removed := runAutoGC(config.Config{DataDir: dataDir, AutoGCHours: 0}, &buf)

	assert.Nil(t, removed)
	assert.True(t, sessionExists(dataDir, "orphan01"), "disabled gc must not remove anything")
	assert.Empty(t, buf.String())
}

// TestRunRegister_AutoGCSweepsOrphanThenCreates is the end-to-end check: with
// AutoGCHours on and an orphan present, `register` removes the orphan and then
// creates exactly one fresh session.
func TestRunRegister_AutoGCSweepsOrphanThenCreates(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "24")

	plantSession(t, dataDir, "orphan01", deadPID, time.Now().UTC().Add(-48*time.Hour))

	projDir := t.TempDir()
	var runErr error
	out := captureStdout(t, func() {
		runErr = runRegister([]string{"--role=esc", "--project-path=" + projDir, "--json=false"})
	})
	require.NoError(t, runErr)

	assert.False(t, sessionExists(dataDir, "orphan01"), "orphan must be swept by register's auto-gc")

	entries, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
	require.NoError(t, err)
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	require.Len(t, dirs, 1, "exactly one fresh session must remain")
	assert.NotEqual(t, "orphan01", dirs[0])
	// --json=false prints just the new session ID; it must match the surviving dir.
	assert.Equal(t, dirs[0], firstLine(out))
}

// TestRunRegister_PopulatesScope is the F-17 register wiring: registering from a
// subfolder of a .git project must store that project root as the manifest
// scope (resolveScope walks up from the --project-path). A marker-less path
// would instead store itself (the cwd fallback), also asserted.
func TestRunRegister_PopulatesScope(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o700))
	sub := filepath.Join(root, "docs")
	require.NoError(t, os.MkdirAll(sub, 0o700))

	mgr := session.NewManager(dataDir, time.Second)

	var runErr error
	out := captureStdout(t, func() {
		runErr = runRegister([]string{"--role=esc", "--project-path=" + sub, "--json=false"})
	})
	require.NoError(t, runErr)
	mf, err := mgr.LoadManifest(firstLine(out))
	require.NoError(t, err)
	// Scope is the canonical (symlink-resolved) project root (F-41); t.TempDir is
	// under a symlinked /tmp|/var on macOS, so resolve the expected value too.
	wantRoot, evErr := filepath.EvalSymlinks(root)
	require.NoError(t, evErr)
	assert.Equal(t, wantRoot, mf.Scope, "scope must be the (canonical) .git project root, derived from the subfolder")

	// A marker-less project path stores itself as scope (cwd fallback).
	bare := t.TempDir()
	var runErr2 error
	out2 := captureStdout(t, func() {
		runErr2 = runRegister([]string{"--role=esc", "--project-path=" + bare, "--json=false"})
	})
	require.NoError(t, runErr2)
	mf2, err := mgr.LoadManifest(firstLine(out2))
	require.NoError(t, err)
	wantBare, evErr2 := filepath.EvalSymlinks(bare)
	require.NoError(t, evErr2)
	assert.Equal(t, wantBare, mf2.Scope, "no marker -> scope is the (canonical) project path itself")
}

// TestRunRegister_SessionIDFlag_Rejected is the A-5 check: register has no use
// for --session-id (the id is derived from agent-name/role/scope), so passing
// it returns an actionable error naming --resume — not the cryptic stdlib
// "flag provided but not defined: -session-id". The check fires before any FS
// access, so no CAB_DATA_DIR setup is needed.
func TestRunRegister_SessionIDFlag_Rejected(t *testing.T) {
	t.Parallel()
	err := runRegister([]string{"--session-id=abc123", "--role=esc"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "register: --session-id is not supported here")
	assert.Contains(t, err.Error(), "--resume", "the message must teach the correct alternative")
}

// captureStdout redirects os.Stdout for the duration of fn and returns what was
// written, so register's manifest/ID output does not pollute test logs.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()
	require.NoError(t, w.Close())
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(data)
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}
