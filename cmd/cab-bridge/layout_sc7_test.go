package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
)

// SC-7 on the tree this generation writes (SEC gate round 3): <base>/layout-2 gets
// the full integrity check, children included, before anything under it is opened.
// The cases are the ones bootstrapBaseDir documents, now pinned on the new
// directory through the function every command calls first.

func layoutCase(t *testing.T) (base, state string) {
	t.Helper()
	base = t.TempDir()
	t.Setenv("CAB_DATA_DIR", base)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	return base, filepath.Join(base, config.LayoutDir)
}

func TestSC7_Layout2_FirstRunCreatesItWithTheFixedChildren(t *testing.T) {
	_, state := layoutCase(t)
	cfg, err := loadConfigOrFail()
	require.NoError(t, err)
	assert.Equal(t, state, cfg.DataDir)
	info, err := os.Lstat(state)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assertPOSIXPerm(t, info.Mode().Perm(), 0o700, "layout-2 on first run")
}

func TestSC7_Layout2_LoosePermsAreTightened(t *testing.T) {
	_, state := layoutCase(t)
	require.NoError(t, os.Mkdir(state, 0o700))
	require.NoError(t, os.Chmod(state, 0o777))
	_, err := loadConfigOrFail()
	require.NoError(t, err, "loose perms repair, they do not stop the command")
	info, err := os.Lstat(state)
	require.NoError(t, err)
	assertPOSIXPerm(t, info.Mode().Perm(), 0o700, "layout-2 after the repair")
}

// A link at layout-2 is refused, and nothing behind it is opened: a session with
// a manifest sits in the target, a command names it by id, and the command fails
// on the integrity check before it can read that manifest.
func TestSC7_Layout2_ALinkIsFatalAndNothingBehindItIsOpened(t *testing.T) {
	base, state := layoutCase(t)
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	plantOverviewSession(t, elsewhere, "linkedsess", "esc", "ESC-linked", "/repo/x", "", "working")
	before := snapshotBase(t, elsewhere)
	mustSymlink(t, elsewhere, state)

	_, err := loadConfigOrFail()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")

	t.Setenv("CAB_SESSION_ID", "linkedsess")
	var cmdErr error
	out := captureStdout(t, func() { cmdErr = runWhoami(nil) })
	require.Error(t, cmdErr, "a command resolves its config first, and stops there")
	assert.Contains(t, cmdErr.Error(), "symlink")
	assert.Empty(t, out, "the manifest behind the link was never read: nothing about it was printed")
	assert.Equal(t, before, snapshotBase(t, elsewhere), "and nothing behind the link was changed")
	assert.NoFileExists(t, filepath.Join(base, "config.json"))
}

func TestSC7_Layout2_AFileInItsPlaceIsFatal(t *testing.T) {
	_, state := layoutCase(t)
	require.NoError(t, os.WriteFile(state, []byte("not a directory"), 0o600))
	_, err := loadConfigOrFail()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

// The two fixed children are part of the check: a link planted at layout-2/sessions
// would redirect every session this binary writes.
func TestSC7_Layout2_ALinkedChildIsFatal(t *testing.T) {
	_, state := layoutCase(t)
	require.NoError(t, os.Mkdir(state, 0o700))
	elsewhere := t.TempDir()
	mustSymlink(t, elsewhere, filepath.Join(state, "sessions"))
	_, err := loadConfigOrFail()
	require.Error(t, err)
	assert.Empty(t, mustReadDirNames(t, elsewhere), "nothing was written through the link")
}

// A foreign owner cannot be created without root, so it is provoked on a directory
// that already belongs to another user. The function under test is the one
// loadConfigOrFail runs on layout-2 (bootstrapDataDir); it must refuse before it
// creates, tightens or opens anything. The premise is asserted, not assumed: if
// the directory happens to be ours, or we are root, the case does not apply.
func TestSC7_AForeignOwnerIsFatalAndNothingIsTightened(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("owner comparison by uid: Unix, not root (Windows compares SIDs; root skips the check by design)")
	}
	foreign := string(filepath.Separator)
	info, err := os.Lstat(foreign)
	require.NoError(t, err)
	if !ownedByAnotherUser(t, info) {
		t.Skip("the root directory is ours on this machine: no foreign directory to provoke")
	}
	before := info.Mode()
	err = bootstrapDataDir(foreign)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to operate")
	after, err := os.Lstat(foreign)
	require.NoError(t, err)
	assert.Equal(t, before, after.Mode(), "a directory that is not ours is never chmod'ed")
}

func mustReadDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
