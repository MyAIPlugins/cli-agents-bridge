package fs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RenameAtomic is what the archive step of the session retirement moves messages with
// (F-171). Two properties of it are relied on and written in its comment, so they are
// asserted here on every platform: it moves, and it replaces a target that exists.
func TestRenameAtomic_MovesAFileAndReplacesAnExistingTarget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.json")
	dst := filepath.Join(dir, "dst.json")
	require.NoError(t, os.WriteFile(src, []byte("new"), 0o600))
	require.NoError(t, os.WriteFile(dst, []byte("old"), 0o600))

	require.NoError(t, RenameAtomic(src, dst))

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "new", string(got), "the target was replaced, not kept and not refused")
	_, serr := os.Stat(src)
	assert.ErrorIs(t, serr, os.ErrNotExist, "and the source is gone")
}

func TestRenameAtomic_AMissingSourceIsAnErrorThatSaysSo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	err := RenameAtomic(filepath.Join(dir, "nope.json"), filepath.Join(dir, "dst.json"))

	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrNotExist, "callers across the tree ask errors.Is(err, fs.ErrNotExist)")
}
