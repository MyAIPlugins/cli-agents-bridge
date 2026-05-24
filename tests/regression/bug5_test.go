package regression

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

// TestBUG5_LongestPrefixMatchWins reproduces BUG-5 (Patil
// get-session-id.sh:25-36 exits at the first matching projectPath in glob
// order without tracking prefix length — non-deterministic result when a
// nested cwd matches both /p1/ and /p1/sub/).
//
// cli-agents-bridge fix: Manager.LongestPrefixLookup tracks the best match
// length across the full scan and returns the longest prefix.
//
// Test scenario from PLAN §7.2 row BUG-5:
//   - register /p1/      → sessionID_p1
//   - register /p1/sub/  → sessionID_p1sub
//   - invoke from /p1/sub/nested/  → must resolve to sessionID_p1sub
//     (the longer prefix), not sessionID_p1 (the shorter parent).
func TestBUG5_LongestPrefixMatchWins(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	mgr := session.NewManager(dataDir, time.Second)

	root := t.TempDir()
	pathP1 := filepath.Join(root, "p1")
	pathP1Sub := filepath.Join(pathP1, "sub")
	pathNested := filepath.Join(pathP1Sub, "nested")
	require.NoError(t, os.MkdirAll(pathNested, 0o755))

	mfP1, relP1, err := mgr.Register(context.Background(), session.RegisterOpts{
		ProjectPath: pathP1,
		Role:        session.RoleVal,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = relP1() })

	mfP1Sub, relP1Sub, err := mgr.Register(context.Background(), session.RegisterOpts{
		ProjectPath: pathP1Sub,
		Role:        session.RoleEsc,
		ForceNew:    true, // /p1/sub is inside /p1 — without ForceNew this would
		//                  legitimately collide via the BUG-6 protection
		//                  (LongestPrefixLookup(pathP1Sub) finds pathP1 as
		//                  ancestor manifest). ForceNew bypasses to set up
		//                  the BUG-5 test stage as intended.
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = relP1Sub() })

	got, err := mgr.LongestPrefixLookup(pathNested)
	require.NoError(t, err)
	assert.Equal(t, mfP1Sub.SessionID, got,
		"BUG-5 regression: cwd %q must resolve to longest-prefix session %s (path %s), not %s (path %s)",
		pathNested, mfP1Sub.SessionID, pathP1Sub, mfP1.SessionID, pathP1)
}
