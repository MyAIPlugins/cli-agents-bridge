package fs

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMoveToProcessed_HappyPath(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	inbox := filepath.Join(base, "inbox")
	processed := filepath.Join(base, "processed")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	src := filepath.Join(inbox, "msg-aaaaaaaaaaaa.json")
	require.NoError(t, os.WriteFile(src, []byte(`{"ok":true}`), 0o600))

	require.NoError(t, MoveToProcessed(src, processed))

	// Source must be gone (rename, not copy)
	_, err := os.Stat(src)
	assert.True(t, os.IsNotExist(err), "MoveToProcessed must remove the source")

	// processed/ must contain exactly one file matching the naming convention
	entries, err := os.ReadDir(processed)
	require.NoError(t, err)
	require.Len(t, entries, 1)

	namePattern := regexp.MustCompile(`^\d{8}T\d{6}\.\d{9}Z-msg-aaaaaaaaaaaa\.json$`)
	assert.True(t, namePattern.MatchString(entries[0].Name()),
		"processed filename must match <RFC3339-stamp>-<orig> shape; got %q", entries[0].Name())

	// Content preserved
	data, err := os.ReadFile(filepath.Join(processed, entries[0].Name()))
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, string(data))
}

func TestMoveToProcessed_CreatesProcessedDirOnDemand(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	inbox := filepath.Join(base, "inbox")
	processed := filepath.Join(base, "processed") // does not exist yet
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	src := filepath.Join(inbox, "msg-bbbbbbbbbbbb.json")
	require.NoError(t, os.WriteFile(src, []byte("x"), 0o600))

	require.NoError(t, MoveToProcessed(src, processed))

	info, err := os.Stat(processed)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(),
		"processed/ must be created with 0o700 (SC-2)")
}

func TestMoveToProcessed_PreservesOrderViaTimestampPrefix(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	inbox := filepath.Join(base, "inbox")
	processed := filepath.Join(base, "processed")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	for i, id := range []string{"msg-aaaaaaaaaaa1", "msg-aaaaaaaaaaa2", "msg-aaaaaaaaaaa3"} {
		src := filepath.Join(inbox, id+".json")
		require.NoError(t, os.WriteFile(src, []byte("x"), 0o600))
		require.NoError(t, MoveToProcessed(src, processed))
		_ = i
	}

	entries, err := os.ReadDir(processed)
	require.NoError(t, err)
	require.Len(t, entries, 3)

	// ReadDir returns entries in arbitrary order, but a stable Sort by name
	// MUST give chronological order thanks to the RFC3339 timestamp prefix.
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	// names already sorted by Go's ReadDir contract (alphabetical) — assert
	// the third entry mentions msg-...3 (latest message wrote last).
	assert.Contains(t, names[2], "msg-aaaaaaaaaaa3",
		"timestamp prefix must give chronological lexical order")
}

func TestMoveToProcessed_NonExistentSrc(t *testing.T) {
	t.Parallel()

	processed := filepath.Join(t.TempDir(), "processed")
	err := MoveToProcessed(filepath.Join(t.TempDir(), "ghost.json"), processed)
	require.Error(t, err)
	assert.True(t, os.IsNotExist(err) || os.IsNotExist(errUnwrap(err)),
		"non-existent source must surface a not-exist error")
}

// errUnwrap is a tiny helper because errors.Is unwraps through %w but plain
// os.IsNotExist does not always cross %w. Simplifies the assertion above.
func errUnwrap(err error) error {
	type unwrapper interface{ Unwrap() error }
	if u, ok := err.(unwrapper); ok {
		return u.Unwrap()
	}
	return err
}
