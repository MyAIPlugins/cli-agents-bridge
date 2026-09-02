package security

// The platform-independent half. Everything that asserts Unix SEMANTICS — a
// uid, a umask, a mode, a FIFO, refusing a symlink — lives in perms_unix_test.go
// behind a build tag, so that these keep running on Windows instead of the whole
// file disappearing there. SC-4 validation is the bulk of it and is portable.
import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateSessionID covers SC-4 (path traversal prevention).
// Regex: ^[a-z0-9]{6,32}$
func TestValidateSessionID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		id      string
		wantErr bool
	}{
		// Valid: lower-bound, upper-bound, alphanumeric
		{"valid 6 chars random", "abc123", false},
		{"valid 6 chars all digits", "123456", false},
		{"valid 6 chars all letters", "abcdef", false},
		{"valid 32 chars upper bound", "abcdef0123456789abcdef0123456789", false},
		{"valid 22 chars friendly-name-like", "cliagentsbridgevalmain", false},

		// Invalid: boundary
		{"too short 5 chars", "abc12", true},
		{"too long 33 chars", "abcdef0123456789abcdef0123456789x", true},
		{"empty string", "", true},

		// Invalid: charset
		{"uppercase", "ABCdef", true},
		{"with dash", "abc-12", true},
		{"with underscore", "abc_12", true},
		{"with dot", "abc.12", true},
		{"with space", "abc 12", true},
		{"with unicode", "abc12à", true},

		// Invalid: path traversal attempts (the critical TM-2 cases)
		{"path traversal classic", "../../etc/passwd", true},
		{"slash separator", "abc/12", true},
		{"backslash separator", `abc\12`, true},
		{"absolute path", "/etc/passwd", true},
		{"null byte injection", "abc12\x00", true},
		{"newline injection", "abc12\n", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSessionID(tc.id)
			if tc.wantErr {
				assert.ErrorIs(t, err, ErrInvalidSessionID, "id=%q should be rejected", tc.id)
			} else {
				assert.NoError(t, err, "id=%q should be accepted", tc.id)
			}
		})
	}
}

// TestValidateTeamID covers the F-5 team label hygiene.
// Regex: ^[a-z0-9][a-z0-9_-]{0,31}$ (1-32 chars, leading alphanumeric, then
// lowercase alphanumeric / '-' / '_'). The empty string is NOT tested as valid
// here: the caller skips ValidateTeamID entirely when --team is empty, so an
// empty value never reaches this function.
func TestValidateTeamID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		id      string
		wantErr bool
	}{
		// Valid
		{"single char", "a", false},
		{"single digit", "1", false},
		{"alpha", "alpha", false},
		{"with dash", "team-1", false},
		{"with underscore", "team_1", false},
		{"digit lead then mix", "0ab-c_d", false},
		{"32 chars upper bound", "a234567890123456789012345678901b", false},

		// Invalid: boundary / charset
		{"empty string", "", true},
		{"33 chars too long", "a2345678901234567890123456789012x", true},
		{"uppercase", "Team", true},
		{"leading dash", "-x", true},
		{"leading underscore", "_x", true},
		{"with space", "team 1", true},
		{"with dot", "team.1", true},
		{"with slash", "team/1", true},
		{"path traversal", "../x", true},
		{"unicode", "tëam", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTeamID(tc.id)
			if tc.wantErr {
				assert.ErrorIs(t, err, ErrInvalidTeamID, "id=%q should be rejected", tc.id)
			} else {
				assert.NoError(t, err, "id=%q should be accepted", tc.id)
			}
		})
	}
}

// TestCheckOwnership covers SC-3 (ownership verification).
// Happy path: a file created by the current process is owned by us → ok, on
// either platform. The MISMATCH path needs a file owned by somebody else, which
// is a Unix construction here — see perms_unix_test.go.
func TestCheckOwnership(t *testing.T) {
	t.Parallel()

	t.Run("happy: own tempfile", func(t *testing.T) {
		tmp := filepath.Join(t.TempDir(), "owned.txt")
		require.NoError(t, os.WriteFile(tmp, []byte("test"), 0o600))

		err := CheckOwnership(tmp)
		assert.NoError(t, err)
	})

	t.Run("non-existent path", func(t *testing.T) {
		err := CheckOwnership(filepath.Join(t.TempDir(), "does-not-exist"))
		require.Error(t, err)
		assert.True(t, errors.Is(err, os.ErrNotExist))
	})
}

// TestEnforceDirPerms covers the platform-independent half of the contract: the
// path must exist and must be a directory. Whether a MODE is then applied is a
// Unix question — the tightening cases are in perms_unix_test.go, and on Windows
// the mode half is a documented no-op.
func TestEnforceDirPerms(t *testing.T) {
	t.Parallel()

	t.Run("non-existent dir errors", func(t *testing.T) {
		err := EnforceDirPerms(filepath.Join(t.TempDir(), "ghost"), 0o700)
		require.Error(t, err)
	})

	t.Run("file instead of dir errors", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "not-a-dir.txt")
		require.NoError(t, os.WriteFile(f, []byte("x"), 0o600))

		err := EnforceDirPerms(f, 0o700)
		require.Error(t, err)
	})
}

// ReadOwnedFile on a file we DO own: read normally.
func TestReadOwnedFile_ReadsOurOwnFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "mine.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"ok":true}`), 0o600))

	data, err := ReadOwnedFile(path)
	require.NoError(t, err)
	assert.Equal(t, `{"ok":true}`, string(data))
}

// A missing file reports the underlying os error, not an ownership verdict: the
// caller distinguishes "not there" from "not yours", and several of them treat
// ErrNotExist as an ordinary race (a message archived between listing and read).
func TestReadOwnedFile_MissingFileIsNotAnOwnershipError(t *testing.T) {
	t.Parallel()
	_, err := ReadOwnedFile(filepath.Join(t.TempDir(), "ghost.json"))
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.NotErrorIs(t, err, ErrOwnershipMismatch)
}
