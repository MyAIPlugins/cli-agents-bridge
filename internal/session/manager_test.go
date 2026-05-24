package session

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateSessionID_MatchesSC4Regex(t *testing.T) {
	t.Parallel()

	re := regexp.MustCompile(`^[a-z0-9]{6,32}$`)
	for i := 0; i < 100; i++ {
		id, err := generateSessionID()
		require.NoError(t, err)
		assert.True(t, re.MatchString(id), "id %q must satisfy SC-4 regex", id)
		assert.Len(t, id, 8, "expect 8 hex chars (4 byte entropy)")
	}
}

func TestRegister_HappyPath(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	projDir := t.TempDir()

	mgr := NewManager(dataDir, time.Second)
	mf, release, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir,
		AgentName:   "VAL-test",
		Role:        RoleVal,
	})
	require.NoError(t, err)
	require.NotNil(t, release)
	t.Cleanup(func() { _ = release() })

	assert.NotEmpty(t, mf.SessionID)
	assert.Equal(t, SchemaVersionV2, mf.SchemaVersion)
	assert.Equal(t, filepath.Base(projDir), mf.ProjectName)
	assert.Equal(t, projDir, mf.ProjectPath)
	assert.Equal(t, "VAL-test", mf.AgentName)
	assert.Equal(t, RoleVal, mf.Role)
	assert.Equal(t, os.Getpid(), mf.PID)
	assert.Equal(t, StatusActive, mf.Status)

	// Session dir + manifest must exist on disk with correct perms
	sessionDir := filepath.Join(dataDir, "sessions", mf.SessionID)
	info, err := os.Stat(sessionDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	for _, sub := range []string{"inbox", "outbox"} {
		subInfo, err := os.Stat(filepath.Join(sessionDir, sub))
		require.NoError(t, err)
		assert.True(t, subInfo.IsDir())
		assert.Equal(t, os.FileMode(0o700), subInfo.Mode().Perm())
	}

	manifestPath := filepath.Join(sessionDir, "manifest.json")
	mfInfo, err := os.Stat(manifestPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), mfInfo.Mode().Perm())

	// LoadManifest roundtrips
	loaded, err := mgr.LoadManifest(mf.SessionID)
	require.NoError(t, err)
	assert.Equal(t, mf.SessionID, loaded.SessionID)
}

func TestRegister_DefaultsApplied(t *testing.T) {
	t.Parallel()

	mgr := NewManager(t.TempDir(), time.Second)
	projDir := t.TempDir()

	mf, release, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir,
		// AgentName, Role, Capabilities left empty → defaults
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = release() })

	assert.Equal(t, filepath.Base(projDir), mf.AgentName, "agentName default = projectName basename")
	assert.Equal(t, RoleNeutral, mf.Role, "role default = neutral")
	assert.NotEmpty(t, mf.Capabilities)
}

func TestRegister_EmptyProjectPath(t *testing.T) {
	t.Parallel()

	mgr := NewManager(t.TempDir(), time.Second)
	_, _, err := mgr.Register(context.Background(), RegisterOpts{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ProjectPath required")
}

func TestLongestPrefixLookup(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	mgr := NewManager(dataDir, time.Second)

	// Stage 3 sessions with nested project paths
	root := t.TempDir()
	p1 := filepath.Join(root, "p1")
	p1sub := filepath.Join(p1, "sub")
	require.NoError(t, os.MkdirAll(p1sub, 0o755))

	registerFor := func(pp string) string {
		mf, rel, err := mgr.Register(context.Background(), RegisterOpts{
			ProjectPath: pp,
			Role:        RoleVal,
			ForceNew:    true, // overlapping paths; in real life each pp would be different
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = rel() })
		return mf.SessionID
	}

	idP1 := registerFor(p1)
	idP1sub := registerFor(p1sub)

	cases := []struct {
		name string
		cwd  string
		want string
	}{
		{"exact match p1", p1, idP1},
		{"exact match p1/sub", p1sub, idP1sub},
		{"nested in p1/sub (BUG-5 scenario)", filepath.Join(p1sub, "nested", "deeper"), idP1sub},
		{"directly in p1 (no subdir)", filepath.Join(p1, "other-file"), idP1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, os.MkdirAll(filepath.Dir(tc.cwd), 0o755))
			got, err := mgr.LongestPrefixLookup(tc.cwd)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got, "cwd %q must resolve to longest matching projectPath", tc.cwd)
		})
	}
}

func TestLongestPrefixLookup_NoMatch(t *testing.T) {
	t.Parallel()

	mgr := NewManager(t.TempDir(), time.Second)
	_, err := mgr.LongestPrefixLookup("/totally/unrelated/path")
	assert.ErrorIs(t, err, ErrNoSessionForCwd)
}

func TestLongestPrefixLookup_NoSessionsDirYet(t *testing.T) {
	t.Parallel()

	// DataDir exists but no sessions/ subdir created yet
	mgr := NewManager(t.TempDir(), time.Second)
	_, err := mgr.LongestPrefixLookup("/any/path")
	assert.ErrorIs(t, err, ErrNoSessionForCwd)
}

func TestStartHeartbeat_UpdatesManifestPeriodically(t *testing.T) {
	t.Parallel()

	mgr := NewManager(t.TempDir(), 30*time.Millisecond) // fast tick for test
	projDir := t.TempDir()

	mf, release, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir,
		Role:        RoleVal,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = release() })

	initialHeartbeat := mf.LastHeartbeat

	ctx, cancel := context.WithCancel(context.Background())
	done := mgr.StartHeartbeat(ctx, mf.SessionID)

	// Wait for at least 3 ticks (~100ms)
	time.Sleep(120 * time.Millisecond)
	cancel()
	<-done

	updated, err := mgr.LoadManifest(mf.SessionID)
	require.NoError(t, err)
	assert.True(t, updated.LastHeartbeat.After(initialHeartbeat),
		"lastHeartbeat must advance after heartbeat goroutine ticks (initial=%v, updated=%v)",
		initialHeartbeat, updated.LastHeartbeat)
}

func TestStartHeartbeat_StopsOnContextCancel(t *testing.T) {
	t.Parallel()

	mgr := NewManager(t.TempDir(), 20*time.Millisecond)
	projDir := t.TempDir()

	mf, release, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir,
		Role:        RoleVal,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = release() })

	ctx, cancel := context.WithCancel(context.Background())
	done := mgr.StartHeartbeat(ctx, mf.SessionID)

	cancel()
	select {
	case <-done:
		// expected
	case <-time.After(200 * time.Millisecond):
		t.Fatal("heartbeat goroutine did not exit within 200ms of ctx cancel")
	}
}

func TestRegister_SameProjectPathTwice_FailsWithoutForceNew(t *testing.T) {
	t.Parallel()

	mgr := NewManager(t.TempDir(), time.Second)
	projDir := t.TempDir()

	mf1, rel1, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir,
		Role:        RoleVal,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel1() })

	// Second register on the same projectPath without ForceNew must fail
	_, _, err = mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir,
		Role:        RoleVal,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSessionExistsForProject)
	assert.Contains(t, err.Error(), mf1.SessionID)
}

func TestRegister_SameProjectPathTwice_AllowsForceNew(t *testing.T) {
	t.Parallel()

	mgr := NewManager(t.TempDir(), time.Second)
	projDir := t.TempDir()

	mf1, rel1, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir,
		Role:        RoleVal,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel1() })

	mf2, rel2, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir,
		Role:        RoleEsc,
		ForceNew:    true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel2() })

	assert.NotEqual(t, mf1.SessionID, mf2.SessionID, "ForceNew must generate fresh session ID")
}

func TestLoadManifest_V1DefaultsApplied(t *testing.T) {
	t.Parallel()

	mgr := NewManager(t.TempDir(), time.Second)

	// Plant a v1 manifest manually (Patil-shape)
	sessionID := "abc123"
	sessionDir := filepath.Join(mgr.DataDir, "sessions", sessionID)
	require.NoError(t, os.MkdirAll(sessionDir, 0o700))

	v1JSON := `{
		"sessionId": "abc123",
		"schemaVersion": 1,
		"projectName": "legacy",
		"projectPath": "/tmp/legacy",
		"startedAt": "2026-01-01T00:00:00Z",
		"lastHeartbeat": "2026-01-01T00:00:00Z",
		"status": "active"
	}`
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "manifest.json"), []byte(v1JSON), 0o600))

	got, err := mgr.LoadManifest(sessionID)
	require.NoError(t, err)
	assert.Equal(t, RoleNeutral, got.Role, "v1 read must default role to neutral")
	assert.Equal(t, "legacy", got.AgentName, "v1 read must default agentName to projectName")
	assert.Equal(t, 0, got.PID, "v1 read must default PID to 0")
}
