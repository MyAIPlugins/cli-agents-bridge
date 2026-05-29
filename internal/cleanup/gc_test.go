package cleanup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// plantSession writes a manifest with an explicit PID and lastHeartbeat into
// dataDir/sessions/<id>/, bypassing Register so a test can pin the exact
// (PID, heartbeat) combination GCOrphans keys on. The session dir is created
// first because AtomicWriteJSON (via SaveManifest) requires an existing parent.
func plantSession(t *testing.T, dataDir, id string, pid int, heartbeat time.Time) {
	t.Helper()
	sessionDir := filepath.Join(dataDir, "sessions", id)
	require.NoError(t, os.MkdirAll(sessionDir, 0o700))
	mgr := session.NewManager(dataDir, time.Second)
	mf := &session.Manifest{
		SessionID:     id,
		SchemaVersion: session.SchemaVersionV2,
		ProjectName:   "proj-" + id,
		ProjectPath:   filepath.Join(dataDir, "proj-"+id), // any non-empty abs path satisfies Validate
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

const deadPID = 999999 // see lock_test TestIsProcessAlive: unlikely to exist

// TestGCOrphans_RemovesOnlyCertainOrphans exercises the DOUBLE condition
// (LL-10) across three sessions swept in a single pass:
//   - orphan: dead PID + stale heartbeat  -> removed
//   - fresh:  dead PID + recent heartbeat -> kept (just registered, BUG-A window)
//   - live:   live PID + stale heartbeat  -> kept (owner in listen via AdoptPID)
func TestGCOrphans_RemovesOnlyCertainOrphans(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	base := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)

	plantSession(t, dataDir, "orphan01", deadPID, base.Add(-48*time.Hour))
	plantSession(t, dataDir, "fresh001", deadPID, base.Add(-1*time.Hour))
	plantSession(t, dataDir, "alive001", os.Getpid(), base.Add(-48*time.Hour))

	removed, err := GCOrphans(dataDir, 24, func() time.Time { return base })
	require.NoError(t, err)

	require.Len(t, removed, 1, "only the certain orphan must be swept")
	assert.Equal(t, "orphan01", removed[0].SessionID)
	assert.Equal(t, deadPID, removed[0].PID)
	assert.Equal(t, 48*time.Hour, removed[0].IdleAge)

	assertGone(t, dataDir, "orphan01")
	assertPresent(t, dataDir, "fresh001") // recent heartbeat: not yet abandoned
	assertPresent(t, dataDir, "alive001") // live PID: never touched
}

// TestGCOrphans_DisabledIsNoOp covers gcHours<=0: even with a textbook orphan
// present, nothing is removed (defensive echo of the AutoGCHours>0 caller gate).
func TestGCOrphans_DisabledIsNoOp(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	base := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	plantSession(t, dataDir, "orphan01", deadPID, base.Add(-48*time.Hour))

	removed, err := GCOrphans(dataDir, 0, func() time.Time { return base })
	require.NoError(t, err)
	assert.Empty(t, removed)
	assertPresent(t, dataDir, "orphan01")
}

// TestGCOrphans_MissingSessionsRootIsClean covers a first-run data dir with no
// sessions/ yet: GCOrphans returns empty (non-nil) without error.
func TestGCOrphans_MissingSessionsRootIsClean(t *testing.T) {
	t.Parallel()

	removed, err := GCOrphans(t.TempDir(), 24, nil)
	require.NoError(t, err)
	assert.NotNil(t, removed)
	assert.Empty(t, removed)
}

// TestGCOrphans_ArchivesInboxAndProcessedBeforeDelete verifies the pre-delete
// archive is reused (no silent data loss) for a gc'd orphan. AUDIT-1: both the
// unread inbox message AND the processed message must land in archive/ under
// their respective subdirs — not just processed/.
func TestGCOrphans_ArchivesInboxAndProcessedBeforeDelete(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	base := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	plantSession(t, dataDir, "orphan01", deadPID, base.Add(-48*time.Hour))

	inboxDir := filepath.Join(dataDir, "sessions", "orphan01", "inbox")
	processedDir := filepath.Join(dataDir, "sessions", "orphan01", "processed")
	require.NoError(t, os.MkdirAll(inboxDir, 0o700))
	require.NoError(t, os.MkdirAll(processedDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(inboxDir, "msg-aaaaaaaaaaaa.json"), []byte(`{"unread":true}`), 0o600))
	require.NoError(t, os.WriteFile(
		filepath.Join(processedDir, "20260527T100000.000Z-msg-bbbbbbbbbbbb.json"),
		[]byte(`{"ok":true}`), 0o600))

	removed, err := GCOrphans(dataDir, 24, func() time.Time { return base })
	require.NoError(t, err)
	require.Len(t, removed, 1)

	assertGone(t, dataDir, "orphan01")

	base2 := filepath.Join(dataDir, "archive", "2026-05-29", "orphan01")
	assertArchived(t, filepath.Join(base2, "inbox"), "msg-aaaaaaaaaaaa")       // AUDIT-1: unread inbox preserved
	assertArchived(t, filepath.Join(base2, "processed"), "msg-bbbbbbbbbbbb")
}

func assertArchived(t *testing.T, archSubdir, wantName string) {
	t.Helper()
	entries, err := os.ReadDir(archSubdir)
	require.NoError(t, err, "archive subdir %q must exist after pre-delete move", archSubdir)
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].Name(), wantName,
		"archived filename preserves original message ID for audit")
}

func assertGone(t *testing.T, dataDir, id string) {
	t.Helper()
	_, err := os.Stat(filepath.Join(dataDir, "sessions", id))
	assert.True(t, os.IsNotExist(err), "session %q must be removed", id)
}

func assertPresent(t *testing.T, dataDir, id string) {
	t.Helper()
	_, err := os.Stat(filepath.Join(dataDir, "sessions", id))
	assert.NoError(t, err, "session %q must survive", id)
}
