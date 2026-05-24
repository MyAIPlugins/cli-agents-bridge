package cleanup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// fixedClock returns a Now func that always reports t. Used to make
// retention sweep deterministic.
func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// registerSession writes a fresh session manifest into dataDir and returns
// the session ID. Optional age overrides the heartbeat to test stale paths.
func registerSession(t *testing.T, dataDir string, age time.Duration) (string, *session.Manager) {
	t.Helper()
	mgr := session.NewManager(dataDir, time.Second)
	now := time.Now().UTC()
	mgr.Now = func() time.Time { return now.Add(-age) }

	projDir := t.TempDir()
	mf, release, err := mgr.Register(context.Background(), session.RegisterOpts{
		ProjectPath: projDir,
		Role:        session.RoleVal,
		ForceNew:    true,
	})
	require.NoError(t, err)
	require.NoError(t, release()) // release lock so cleanup can wipe the dir
	return mf.SessionID, mgr
}

func TestRun_MySession_RemovesOnlyOwnSession(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	ownID, _ := registerSession(t, dataDir, 0)
	otherID, _ := registerSession(t, dataDir, 0)

	res, err := Run(context.Background(), Options{
		DataDir:       dataDir,
		Scope:         ScopeMySession,
		OwnSessionID:  ownID,
		StaleSeconds:  300,
		RetentionDays: 7,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{ownID}, res.SessionsRemoved)

	// Own session dir must be gone
	_, err = os.Stat(filepath.Join(dataDir, "sessions", ownID))
	assert.True(t, os.IsNotExist(err), "own session must be removed")

	// Other session must remain (BUG-4 regression: cross-project safety)
	_, err = os.Stat(filepath.Join(dataDir, "sessions", otherID))
	assert.NoError(t, err, "BUG-4: cleanup my-session must NOT touch other project's session")
}

func TestRun_MySession_MissingOwnSessionID_Errors(t *testing.T) {
	t.Parallel()

	_, err := Run(context.Background(), Options{
		DataDir: t.TempDir(),
		Scope:   ScopeMySession,
	})
	assert.ErrorIs(t, err, ErrOwnSessionRequired)
}

func TestRun_Global_RemovesOnlyStaleSessions(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	freshID, _ := registerSession(t, dataDir, 10*time.Second)
	staleID, _ := registerSession(t, dataDir, 600*time.Second)

	res, err := Run(context.Background(), Options{
		DataDir:       dataDir,
		Scope:         ScopeGlobal,
		StaleSeconds:  300, // BUG-8: same field cleanup + list-peers read
		RetentionDays: 7,
	})
	require.NoError(t, err)
	assert.Contains(t, res.SessionsRemoved, staleID)
	assert.NotContains(t, res.SessionsRemoved, freshID)

	_, err = os.Stat(filepath.Join(dataDir, "sessions", freshID))
	assert.NoError(t, err, "fresh session must survive")

	_, err = os.Stat(filepath.Join(dataDir, "sessions", staleID))
	assert.True(t, os.IsNotExist(err), "stale session must be removed")
}

func TestRun_UnknownScope(t *testing.T) {
	t.Parallel()

	_, err := Run(context.Background(), Options{
		DataDir: t.TempDir(),
		Scope:   "everything",
	})
	assert.ErrorIs(t, err, ErrUnknownScope)
}

func TestRun_ArchivesProcessedBeforeDelete(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	sid, _ := registerSession(t, dataDir, 0)

	// Plant a processed/ artifact (simulate post-poll consumption)
	processedDir := filepath.Join(dataDir, "sessions", sid, "processed")
	require.NoError(t, os.MkdirAll(processedDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(processedDir, "20260524T180000.000Z-msg-aaaaaaaaaaaa.json"),
		[]byte(`{"ok":true}`), 0o600))

	now := time.Date(2026, 5, 24, 18, 0, 0, 0, time.UTC)
	res, err := Run(context.Background(), Options{
		DataDir:       dataDir,
		Scope:         ScopeMySession,
		OwnSessionID:  sid,
		StaleSeconds:  300,
		RetentionDays: 7,
		Now:           fixedClock(now),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{sid}, res.SessionsRemoved)

	// Archive must exist at archive/2026-05-24/<sid>/
	archDir := filepath.Join(dataDir, "archive", "2026-05-24", sid)
	entries, err := os.ReadDir(archDir)
	require.NoError(t, err, "archive dir must exist after pre-delete move")
	require.Len(t, entries, 1, "archived file count must match processed/ contents")
	assert.Contains(t, entries[0].Name(), "msg-aaaaaaaaaaaa",
		"archived filename preserves original message ID for audit")
}

func TestRun_RetentionSweepPurgesOldArchives(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	archRoot := filepath.Join(dataDir, "archive")

	now := time.Date(2026, 5, 24, 18, 0, 0, 0, time.UTC)
	retentionDays := 7
	// dirs: 2026-05-01 (older than 7d ago) + 2026-05-20 (within 7d)
	require.NoError(t, os.MkdirAll(filepath.Join(archRoot, "2026-05-01", "sid1"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(archRoot, "2026-05-20", "sid2"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(archRoot, "2026-05-01", "sid1", "x.json"), []byte("x"), 0o600))

	// Use a degenerate global sweep (no sessions) so the call exercises the retention path.
	res, err := Run(context.Background(), Options{
		DataDir:       dataDir,
		Scope:         ScopeGlobal,
		StaleSeconds:  300,
		RetentionDays: retentionDays,
		Now:           fixedClock(now),
	})
	require.NoError(t, err)
	assert.Contains(t, res.ArchivesPurged, "2026-05-01", "old archive must be purged")
	assert.NotContains(t, res.ArchivesPurged, "2026-05-20", "in-window archive must survive")

	_, err = os.Stat(filepath.Join(archRoot, "2026-05-01"))
	assert.True(t, os.IsNotExist(err), "old archive dir must be removed from disk")
}
