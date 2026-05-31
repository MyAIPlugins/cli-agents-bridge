package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reconnectDeadPID is a PID very unlikely to exist, so IsProcessAlive reports
// false — used to simulate an abandoned (post-compact/post-register-death)
// session whose owning process is gone.
const reconnectDeadPID = 2147480000

// abandon simulates the real post-compact / post-register-death state: a dead
// manifest PID. Required because Register writes os.Getpid() (the LIVE test
// process), which reconnect would otherwise treat as a live, non-resumable
// session. Liveness is the manifest PID (a running listen keeps it alive), not
// the lock.
func abandon(t *testing.T, mgr *Manager, id string) {
	t.Helper()
	mf, err := mgr.LoadManifest(id)
	require.NoError(t, err)
	mf.PID = reconnectDeadPID
	require.NoError(t, mgr.SaveManifest(mf))
}

// registerReusable registers a session, releases its lock, and marks it
// abandoned (dead PID) so a later resume can take it. With a non-zero age the
// heartbeat is backdated via the injected clock. ForceNew lets multiple
// same-identity sessions be planted for the multi-match test.
func registerReusable(t *testing.T, mgr *Manager, projDir, agent, role, scope string, age time.Duration) string {
	t.Helper()
	saved := mgr.Now
	base := time.Now().UTC()
	mgr.Now = func() time.Time { return base.Add(age) }
	mf, rel, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir, AgentName: agent, Role: role, Scope: scope, ForceNew: true,
	})
	require.NoError(t, err)
	require.NoError(t, rel())
	mgr.Now = saved
	abandon(t, mgr, mf.SessionID)
	return mf.SessionID
}

func TestRegister_Resume_ResumesOwnStale(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	projDir := t.TempDir()
	mgr := NewManager(dataDir, time.Second)

	mf1, rel1, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir, AgentName: "ESC-x", Role: RoleEsc, Scope: "/proj/root",
	})
	require.NoError(t, err)
	require.NoError(t, rel1())     // release so the session is reusable
	abandon(t, mgr, mf1.SessionID) // owning process gone (post-compact)

	// Plant an inbox message to prove preservation across the resume.
	inbox := filepath.Join(dataDir, "sessions", mf1.SessionID, "inbox")
	require.NoError(t, os.WriteFile(filepath.Join(inbox, "keep.json"), []byte("{}"), 0o600))

	mf2, rel2, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir, AgentName: "ESC-x", Role: RoleEsc, Scope: "/proj/root", Resume: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel2() })

	assert.Equal(t, mf1.SessionID, mf2.SessionID, "resume must reuse the same sessionId")
	assert.FileExists(t, filepath.Join(inbox, "keep.json"), "inbox preserved across resume")
	assert.Equal(t, os.Getpid(), mf2.PID, "resume adopts the current PID")
}

// TestRegister_Resume_DoesNotStealLive is the CRITICAL F-27 regression: a
// matching session whose lock is held by a live FOREIGN process (pid 1, NOT our
// own re-entrant pid) must NOT be resumed — Register returns ErrIdentityLive and
// the live session's manifest is left untouched.
func TestRegister_Resume_DoesNotStealLive(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	projDir := t.TempDir()
	mgr := NewManager(dataDir, time.Second)

	mf1, rel1, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir, AgentName: "ESC-x", Role: RoleEsc, Scope: "/proj/root",
	})
	require.NoError(t, err)
	require.NoError(t, rel1())

	// Simulate a LIVE owner: set the MANIFEST PID to a foreign, known-alive PID
	// (1 = init/launchd, always exists, NOT os.Getpid()). A running listen keeps
	// the manifest PID alive via AdoptPID; the lock is not a liveness signal.
	mf1.PID = 1
	require.NoError(t, mgr.SaveManifest(mf1))

	_, _, err = mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir, AgentName: "ESC-x", Role: RoleEsc, Scope: "/proj/root", Resume: true,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIdentityLive, "a live identity match must not be stolen")

	reloaded, err := mgr.LoadManifest(mf1.SessionID)
	require.NoError(t, err)
	assert.Equal(t, 1, reloaded.PID, "live session manifest PID must be unchanged (not adopted)")
}

func TestRegister_Resume_NoMatch_RegistersNew(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	projDir := t.TempDir()
	mgr := NewManager(dataDir, time.Second)

	mf, rel, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir, AgentName: "ESC-fresh", Role: RoleEsc, Scope: "/proj/root", Resume: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel() })
	assert.NotEmpty(t, mf.SessionID, "no identity match -> a fresh session is registered")
}

func TestRegister_Resume_MultiMatch_ResumesMostRecent(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	projDir := t.TempDir()
	mgr := NewManager(dataDir, time.Second)

	older := registerReusable(t, mgr, projDir, "ESC-x", RoleEsc, "/proj/root", -2*time.Hour)
	newer := registerReusable(t, mgr, projDir, "ESC-x", RoleEsc, "/proj/root", -1*time.Hour)

	mf, rel, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir, AgentName: "ESC-x", Role: RoleEsc, Scope: "/proj/root", Resume: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel() })

	assert.Equal(t, newer, mf.SessionID, "multi-match must resume the most recent session")
	assert.NotEqual(t, older, mf.SessionID)
}

// TestRegister_Resume_LegacyNoScope_MatchesAndBackfills: a pre-F-17 session
// (empty scope) is matched by agent-name + projectPath prefix, and the resume
// backfills the derived F-17 scope into it.
func TestRegister_Resume_LegacyNoScope_MatchesAndBackfills(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	projDir := t.TempDir()
	mgr := NewManager(dataDir, time.Second)

	mf1, rel1, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir, AgentName: "ESC-x", Role: RoleEsc, // no Scope (legacy)
	})
	require.NoError(t, err)
	require.NoError(t, rel1())
	require.Empty(t, mf1.Scope)
	abandon(t, mgr, mf1.SessionID)

	subDir := filepath.Join(projDir, "sub")
	require.NoError(t, os.MkdirAll(subDir, 0o700))

	mf2, rel2, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: subDir, AgentName: "ESC-x", Role: RoleEsc, Scope: projDir, Resume: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel2() })

	assert.Equal(t, mf1.SessionID, mf2.SessionID, "legacy session matched by agent-name + projectPath prefix")
	assert.Equal(t, projDir, mf2.Scope, "resume backfills the F-17 scope into the legacy session")
}

// TestRegister_Resume_DifferentRole_NoMatch: role is part of the identity, so a
// session with a different role is not resumed (a fresh one is created). VAL and
// ESC use different project dirs but share a scope, as they really do.
func TestRegister_Resume_DifferentRole_NoMatch(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	mgr := NewManager(dataDir, time.Second)

	val, rel1, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: t.TempDir(), AgentName: "AGENT", Role: RoleVal, Scope: "/proj/root",
	})
	require.NoError(t, err)
	require.NoError(t, rel1())

	// Same agent-name + scope, different role and project dir -> must NOT resume
	// the VAL; a fresh ESC session is registered instead.
	mf, rel2, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: t.TempDir(), AgentName: "AGENT", Role: RoleEsc, Scope: "/proj/root", Resume: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel2() })
	assert.NotEqual(t, val.SessionID, mf.SessionID, "different role must not be resumed")
}
