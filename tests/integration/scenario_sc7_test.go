package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSC7_SymlinkDataDirRejected proves FINDING-1 is wired end-to-end: a real
// subcommand invoked against a symlinked data dir aborts (TM-5). This verifies
// the boot check actually runs in the subcommand entry path, not just in a unit.
func TestSC7_SymlinkDataDirRejected(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	realDir := filepath.Join(tmp, "real")
	require.NoError(t, os.Mkdir(realDir, 0o700))
	linkDir := filepath.Join(tmp, "link")
	require.NoError(t, os.Symlink(realDir, linkDir))

	_, errOut, exit := run(t, []string{"peers"}, dataDirEnv(linkDir))
	assert.NotEqual(t, 0, exit, "a subcommand on a symlinked data dir must fail")
	assert.Contains(t, errOut, "symlink", "stderr must explain the symlink rejection")
}

// TestSC7_FirstRunCreatesDataDir confirms a non-existent data dir is treated as
// a first run (created 0700), NOT as an attack — the critical smoke/test
// invariant called out in the Sprint 5 brief.
func TestSC7_FirstRunCreatesDataDir(t *testing.T) {
	t.Parallel()

	base := filepath.Join(t.TempDir(), "fresh-not-yet-created")
	_, errOut, exit := run(t, []string{"peers"}, dataDirEnv(base))
	require.Equal(t, 0, exit, "first run must succeed and create the data dir: %s", errOut)

	info, err := os.Lstat(base)
	require.NoError(t, err, "data dir must be created on first run")
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "first-run data dir must be 0700")
}
