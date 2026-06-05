package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
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

// plantManifestDetails writes a complete, valid manifest under mgr.DataDir with
// caller-controlled ProjectPath/Scope/AgentName/Role, so LookupByCWDDetails can
// be exercised on arbitrary scope/path layouts without going through Register
// (no lock, no collision check, no real filesystem paths — the lookup is lexical).
func plantManifestDetails(t *testing.T, mgr *Manager, id, projectPath, scope, agentName, role string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(mgr.DataDir, "sessions", id), 0o700))
	now := time.Now().UTC()
	mf := &Manifest{
		SessionID:     id,
		SchemaVersion: SchemaVersionV2,
		ProjectName:   filepath.Base(projectPath),
		ProjectPath:   projectPath,
		AgentName:     agentName,
		Role:          role,
		PID:           os.Getpid(),
		StartedAt:     now,
		LastHeartbeat: now,
		Status:        StatusActive,
		Capabilities:  []string{"query"},
		Scope:         scope,
	}
	require.NoError(t, mgr.SaveManifest(mf))
}

// TestLookupByCWDDetails_SingleAndNested: one session resolves from its exact
// path and from a nested subdir — no ambiguity, no siblings (same behaviour as
// LongestPrefixLookup, now with the richer result).
func TestLookupByCWDDetails_SingleAndNested(t *testing.T) {
	t.Parallel()
	mgr := NewManager(t.TempDir(), time.Second)
	plantManifestDetails(t, mgr, "lkupsing", "/repo/p1", "/repo/p1", "ESC-x", RoleEsc)

	for _, cwd := range []string{"/repo/p1", "/repo/p1/sub/nested"} {
		res, err := mgr.LookupByCWDDetails(cwd)
		require.NoError(t, err)
		assert.Equal(t, "lkupsing", res.SelectedID, "cwd %q", cwd)
		assert.False(t, res.HardAmbiguous)
		assert.Len(t, res.Candidates, 1)
		assert.Empty(t, res.ScopeSiblings)
	}
}

// TestLookupByCWDDetails_NestedLongestPrefixWins: p1 and p1/sub both match a deep
// cwd, but the longer prefix wins and it is NOT a hard ambiguity (different
// lengths). Mirrors TestLongestPrefixLookup's nested case.
func TestLookupByCWDDetails_NestedLongestPrefixWins(t *testing.T) {
	t.Parallel()
	mgr := NewManager(t.TempDir(), time.Second)
	plantManifestDetails(t, mgr, "lkupp1aa", "/repo/p1", "", "VAL-x", RoleVal)
	plantManifestDetails(t, mgr, "lkupp1sb", "/repo/p1/sub", "", "ESC-x", RoleEsc)

	res, err := mgr.LookupByCWDDetails("/repo/p1/sub/deeper")
	require.NoError(t, err)
	assert.Equal(t, "lkupp1sb", res.SelectedID, "longest prefix wins")
	assert.False(t, res.HardAmbiguous, "different prefix lengths are not a tie")
	assert.Len(t, res.Candidates, 1)
}

// TestLookupByCWDDetails_HardAmbiguity: two sessions with the SAME ProjectPath
// match a cwd at the same maximum length → HardAmbiguous, both contenders
// surfaced (LongestPrefixLookup would silently pick the first).
func TestLookupByCWDDetails_HardAmbiguity(t *testing.T) {
	t.Parallel()
	mgr := NewManager(t.TempDir(), time.Second)
	plantManifestDetails(t, mgr, "lkupamb1", "/repo/shared", "", "VAL-x", RoleVal)
	plantManifestDetails(t, mgr, "lkupamb2", "/repo/shared", "", "ESC-x", RoleEsc)

	res, err := mgr.LookupByCWDDetails("/repo/shared")
	require.NoError(t, err)
	assert.True(t, res.HardAmbiguous)
	assert.Len(t, res.Candidates, 2, "both equal-length matches are contenders")
	assert.NotEmpty(t, res.SelectedID, "a deterministic pick is still made")
}

// TestLookupByCWDDetails_SharedScopeSiblings: VAL@root + ESC@worktree share one
// Scope with different ProjectPaths. From the worktree cwd the ESC is selected
// and the VAL is the shared-scope sibling; from the root it is symmetric.
func TestLookupByCWDDetails_SharedScopeSiblings(t *testing.T) {
	t.Parallel()
	mgr := NewManager(t.TempDir(), time.Second)
	scope := "/repo/main"
	plantManifestDetails(t, mgr, "lkupval0", "/repo/main", scope, "VAL-x", RoleVal)
	plantManifestDetails(t, mgr, "lkupesc0", "/repo/main-wt", scope, "ESC-x", RoleEsc)

	res, err := mgr.LookupByCWDDetails("/repo/main-wt/internal")
	require.NoError(t, err)
	assert.Equal(t, "lkupesc0", res.SelectedID)
	assert.False(t, res.HardAmbiguous)
	require.Len(t, res.ScopeSiblings, 1)
	assert.Equal(t, "lkupval0", res.ScopeSiblings[0].ID)

	res2, err := mgr.LookupByCWDDetails("/repo/main")
	require.NoError(t, err)
	assert.Equal(t, "lkupval0", res2.SelectedID)
	require.Len(t, res2.ScopeSiblings, 1)
	assert.Equal(t, "lkupesc0", res2.ScopeSiblings[0].ID)
}

// TestLookupByCWDDetails_EmptyScopeNoSiblings: with no scope (legacy/v1), there
// is no shared-scope hazard even with multiple distinct-path sessions.
func TestLookupByCWDDetails_EmptyScopeNoSiblings(t *testing.T) {
	t.Parallel()
	mgr := NewManager(t.TempDir(), time.Second)
	plantManifestDetails(t, mgr, "lkupemp1", "/repo/a", "", "VAL-x", RoleVal)
	plantManifestDetails(t, mgr, "lkupemp2", "/repo/b", "", "ESC-x", RoleEsc)

	res, err := mgr.LookupByCWDDetails("/repo/a")
	require.NoError(t, err)
	assert.Equal(t, "lkupemp1", res.SelectedID)
	assert.Empty(t, res.ScopeSiblings, "empty scope → no shared-scope siblings")
}

// TestLookupByCWDDetails_NoMatch: a cwd matching nothing → ErrNoSessionForCwd,
// like LongestPrefixLookup.
func TestLookupByCWDDetails_NoMatch(t *testing.T) {
	t.Parallel()
	mgr := NewManager(t.TempDir(), time.Second)
	plantManifestDetails(t, mgr, "lkupnom1", "/repo/a", "/repo/a", "VAL-x", RoleVal)
	_, err := mgr.LookupByCWDDetails("/totally/unrelated")
	assert.ErrorIs(t, err, ErrNoSessionForCwd)
}

func TestLongestPrefixLookup_NoSessionsDirYet(t *testing.T) {
	t.Parallel()

	// DataDir exists but no sessions/ subdir created yet
	mgr := NewManager(t.TempDir(), time.Second)
	_, err := mgr.LongestPrefixLookup("/any/path")
	assert.ErrorIs(t, err, ErrNoSessionForCwd)
}

// TestLongestPrefixLookup_ReturnsDirNameNotManifestField is the NEW-1
// regression: when a session's directory name diverges from the sessionId
// field inside its manifest (manual rename / hand-crafted import), the lookup
// must return the directory name (the real, single-component identity) and
// never the attacker-influenceable manifest field.
func TestLongestPrefixLookup_ReturnsDirNameNotManifestField(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	mgr := NewManager(dataDir, time.Second)
	projDir := t.TempDir()

	dirName := "realdir1"
	sessionDir := filepath.Join(dataDir, "sessions", dirName)
	require.NoError(t, os.MkdirAll(sessionDir, 0o700))
	mf := &Manifest{
		SessionID:     "spoofedi",
		SchemaVersion: SchemaVersionV2,
		ProjectName:   filepath.Base(projDir),
		ProjectPath:   projDir,
		Role:          RoleVal,
		Status:        StatusActive,
	}
	data, err := json.MarshalIndent(mf, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, "manifest.json"), data, 0o600))

	got, err := mgr.LongestPrefixLookup(projDir)
	require.NoError(t, err)
	assert.Equal(t, dirName, got, "lookup must return the directory name, not the manifest sessionId field")
	assert.NotEqual(t, "spoofedi", got, "the manifest sessionId field must never be returned")
}

// TestAdoptPID_WritesCurrentPID is the Sprint 6 BUG-A unit: AdoptPID must
// overwrite the manifest PID with the calling (long-running listen) process's
// PID, so that BUG-6 collision detection and stale detection see a live owner
// instead of the dead ephemeral PID written by the one-shot register command.
func TestAdoptPID_WritesCurrentPID(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	mgr := NewManager(dataDir, time.Second)
	projDir := t.TempDir()

	mf, rel, err := mgr.Register(context.Background(), RegisterOpts{ProjectPath: projDir, ForceNew: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel() })

	// Simulate the ephemeral register PID having been overwritten by a stale
	// value on disk (as if the register process had died).
	mf.PID = 1
	require.NoError(t, mgr.SaveManifest(mf))

	require.NoError(t, mgr.AdoptPID(mf.SessionID))

	reloaded, err := mgr.LoadManifest(mf.SessionID)
	require.NoError(t, err)
	assert.Equal(t, os.Getpid(), reloaded.PID, "AdoptPID must write the current process PID")
	assert.False(t, reloaded.LastHeartbeat.IsZero(), "AdoptPID must also refresh the heartbeat")
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

// TestSetLastConsumed_UpdatesManifest is the F-12 unit: SetLastConsumed must
// persist the message ID into the manifest's lastConsumedMsgId field.
func TestSetLastConsumed_UpdatesManifest(t *testing.T) {
	t.Parallel()

	mgr := NewManager(t.TempDir(), time.Second)
	projDir := t.TempDir()
	mf, rel, err := mgr.Register(context.Background(), RegisterOpts{ProjectPath: projDir, Role: RoleEsc})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel() })

	assert.Empty(t, mf.LastConsumedMsgID, "fresh session has no consumed message")
	require.NoError(t, mgr.SetLastConsumed(mf.SessionID, "msg-aaaaaaaaaaaa"))

	loaded, err := mgr.LoadManifest(mf.SessionID)
	require.NoError(t, err)
	assert.Equal(t, "msg-aaaaaaaaaaaa", loaded.LastConsumedMsgID)
}

// TestManifestRMW_ConcurrentHeartbeatAndConsume_NoLostUpdate exercises the
// F-12 §3.5 race: the heartbeat goroutine and SetLastConsumed both
// load-modify-save the SAME manifest concurrently. Without manifestMu a
// heartbeat write that loaded an older copy would clobber a freshly-set
// lastConsumedMsgId (lost update). The mutex serializes the read-modify-write,
// so the LAST id written must always survive. Run under -race to also catch any
// memory race in the Manager itself.
func TestManifestRMW_ConcurrentHeartbeatAndConsume_NoLostUpdate(t *testing.T) {
	t.Parallel()

	mgr := NewManager(t.TempDir(), 5*time.Millisecond) // aggressive heartbeat tick
	projDir := t.TempDir()
	mf, rel, err := mgr.Register(context.Background(), RegisterOpts{ProjectPath: projDir, Role: RoleEsc})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel() })

	ctx, cancel := context.WithCancel(context.Background())
	done := mgr.StartHeartbeat(ctx, mf.SessionID)

	const n = 50
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if e := mgr.SetLastConsumed(mf.SessionID, fmt.Sprintf("msg-%012d", i)); e != nil {
				select {
				case errCh <- e:
				default:
				}
				return
			}
		}
	}()
	wg.Wait()
	cancel()
	<-done

	select {
	case e := <-errCh:
		require.NoError(t, e, "SetLastConsumed must not error under concurrency")
	default:
	}

	loaded, err := mgr.LoadManifest(mf.SessionID)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("msg-%012d", n-1), loaded.LastConsumedMsgID,
		"last consumed id must survive concurrent heartbeat writes (no lost update)")
	assert.False(t, loaded.LastHeartbeat.IsZero(), "heartbeat must also have run")
}
