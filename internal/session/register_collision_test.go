package session

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// treeContents maps every path under root (relative) to its content, with the
// directories marked, so two snapshots can be compared for equality.
func treeContents(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			out[rel] = "<dir>"
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		out[rel] = string(data)
		return nil
	})
	require.NoError(t, err)
	return out
}

// F-160 C1: Register used to MkdirAll the session directory and RemoveAll it on
// a failed lock. With an id colliding with an existing session whose lock is
// held by a live process, that deleted somebody else's session. The directory
// must be created exclusively, and a collision must touch nothing.
func TestRegister_AnIDCollisionNeverTouchesTheExistingSession(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	mgr := NewManager(dataDir, time.Second)
	const collidingID = "aaaaaaaa"
	mgr.genID = func() (string, error) { return collidingID, nil }

	existing := filepath.Join(dataDir, "sessions", collidingID)
	require.NoError(t, os.MkdirAll(filepath.Join(existing, "inbox"), 0o700))
	// A lock held by a live foreign process (our parent): on c0649ad this made
	// AcquireLock fail with ErrLockHeld and the rollback RemoveAll the directory.
	require.NoError(t, os.WriteFile(filepath.Join(existing, "lock"), []byte(fmt.Sprintf("%d\n", os.Getppid())), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(existing, "manifest.json"), []byte(`{"sessionId":"aaaaaaaa"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(existing, "inbox", "m.json"), []byte(`{"kept":true}`), 0o600))
	before := treeContents(t, existing)

	_, release, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: t.TempDir(),
		AgentName:   "VAL-collision",
		Role:        RoleVal,
	})
	if release != nil {
		_ = release()
	}

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists", "the error must say why nothing happened")
	assert.Equal(t, before, treeContents(t, existing), "the existing session must be byte-identical after the collision")
}

// The sessions/ parent is created on demand, and a clean register still works
// through the exclusive Mkdir (the happy path of every other test, with the
// seam in place).
func TestRegister_CreatesTheSessionsParentAndTheSessionDirectory(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	mgr := NewManager(dataDir, time.Second)
	mgr.genID = func() (string, error) { return "bbbbbbbb", nil }

	mf, release, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: t.TempDir(),
		AgentName:   "VAL-fresh",
		Role:        RoleVal,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = release() })

	assert.Equal(t, "bbbbbbbb", mf.SessionID)
	info, err := os.Stat(filepath.Join(dataDir, "sessions", "bbbbbbbb"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}
