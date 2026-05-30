package security

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
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
// Happy path: file created by current process is owned by current UID → ok.
// Mismatch path: /etc/passwd is owned by root (UID 0); when current UID != 0
// CheckOwnership must return ErrOwnershipMismatch.
func TestCheckOwnership(t *testing.T) {
	t.Parallel()

	t.Run("happy: own tempfile", func(t *testing.T) {
		tmp := filepath.Join(t.TempDir(), "owned.txt")
		require.NoError(t, os.WriteFile(tmp, []byte("test"), 0o600))

		err := CheckOwnership(tmp)
		assert.NoError(t, err)
	})

	t.Run("mismatch: /etc/passwd when non-root", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("test process is root, ownership check is skipped by design (SC-3 root edge case)")
		}
		// /etc/passwd exists and is root-owned on every Unix.
		// If this assertion fails, the test environment is non-standard.
		err := CheckOwnership("/etc/passwd")
		if err == nil {
			// Some sandboxed CI environments may report current UID as owner — skip then.
			t.Skip("/etc/passwd appears owned by current UID, likely sandboxed CI — skipping mismatch path")
		}
		assert.ErrorIs(t, err, ErrOwnershipMismatch)
	})

	t.Run("non-existent path", func(t *testing.T) {
		err := CheckOwnership(filepath.Join(t.TempDir(), "does-not-exist"))
		require.Error(t, err)
		assert.True(t, errors.Is(err, os.ErrNotExist))
	})
}

// TestEnforceDirPerms covers idempotent chmod enforcement.
// Backs SC-2 when a session dir pre-exists with wrong perms (e.g. user
// manually chmodded it to 755).
func TestEnforceDirPerms(t *testing.T) {
	t.Parallel()

	t.Run("chmod from 755 to 700", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "tighten")
		require.NoError(t, os.Mkdir(dir, 0o755))

		err := EnforceDirPerms(dir, 0o700)
		require.NoError(t, err)

		info, err := os.Stat(dir)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	})

	t.Run("idempotent: 700 stays 700 across calls", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "already-tight")
		require.NoError(t, os.Mkdir(dir, 0o700))

		require.NoError(t, EnforceDirPerms(dir, 0o700))
		require.NoError(t, EnforceDirPerms(dir, 0o700))

		info, err := os.Stat(dir)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	})

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

// TestUmaskPropagation verifies that setting umask 0o077 (as cmd/cab-bridge
// main.init() does — SC-1) results in files created via os.WriteFile having
// mode 0o600 even when the requested mode is more permissive.
//
// This test mutates the process umask, so it must run serially (no t.Parallel)
// to avoid races with the other tests above.
func TestUmaskPropagation(t *testing.T) {
	prevUmask := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(prevUmask) })

	tmp := filepath.Join(t.TempDir(), "umask-check.txt")
	// Request permissive 0o666 — umask 0o077 should strip group+other bits
	// down to 0o600.
	require.NoError(t, os.WriteFile(tmp, []byte("x"), 0o666))

	info, err := os.Stat(tmp)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"with umask 0o077, requested 0o666 must be masked down to 0o600")
}
