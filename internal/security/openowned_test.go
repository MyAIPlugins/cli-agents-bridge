package security

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// OpenOwned is openOwned handed to a caller that acts on the descriptor (a
// lock). The portable half: what it opens, and what it refuses by shape.
func TestOpenOwned_OpensARegularFileOfOurs(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(path, []byte("hello"), 0o600))

	f, err := OpenOwned(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	data, err := io.ReadAll(f)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data), "the caller gets the open descriptor, readable")
}

func TestOpenOwned_MissingFileIsNotExist(t *testing.T) {
	t.Parallel()
	_, err := OpenOwned(filepath.Join(t.TempDir(), "nope"))
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrNotExist, "callers tell 'missing' from 'refused' with errors.Is")
	assert.NotErrorIs(t, err, ErrOwnershipMismatch)
}

func TestOpenOwned_RefusesADirectory(t *testing.T) {
	t.Parallel()
	_, err := OpenOwned(t.TempDir())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrOwnershipMismatch)
}
