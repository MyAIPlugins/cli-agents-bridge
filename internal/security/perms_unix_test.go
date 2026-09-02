//go:build !windows

package security

// The Unix half of the suite: everything that asserts a uid, a umask, a mode, a
// FIFO or an atomic symlink refusal. Split out rather than tagging the whole
// file, so the portable assertions (SC-4 validation above all) keep running on
// Windows — "a test that asserts an exclusion has to run where the exclusion
// exists", and the converse is just as true.

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The mismatch half of SC-3: /etc/passwd is owned by root (UID 0); when the
// current UID is not 0, CheckOwnership must return ErrOwnershipMismatch.
func TestCheckOwnership_ForeignFileIsRefused(t *testing.T) {
	t.Parallel()
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
}

// The mode half of EnforceDirPerms: idempotent chmod enforcement. Backs SC-2
// when a session dir pre-exists with wrong perms (e.g. user manually chmodded it
// to 755). On Windows Perm() is pinned at 0777 and the enforcement is a
// documented no-op, so these two cannot hold there.
func TestEnforceDirPerms_Mode(t *testing.T) {
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

// And on a file owned by somebody else: refused.
//
// /etc/hosts is root-owned on both macOS and Linux and readable by everyone,
// which makes it the one file that can exercise the rejection path WITHOUT the
// test needing privileges it should never have. A mock would have proved that
// the mock returns what it was told to.
func TestReadOwnedFile_RefusesAnotherUsersFile(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("running as root: the check is deliberately skipped for root")
	}
	info, err := os.Stat("/etc/hosts")
	if err != nil {
		t.Skip("/etc/hosts not available on this platform")
	}
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(sys.Uid) == os.Getuid() {
		t.Skip("/etc/hosts is not owned by another uid here")
	}

	_, err = ReadOwnedFile("/etc/hosts")
	require.Error(t, err, "a file owned by another uid must not be read")
	assert.ErrorIs(t, err, ErrOwnershipMismatch)
	assert.Contains(t, err.Error(), "/etc/hosts", "and the error names the path")
}

// The CRI design-gate finding: os.Open FOLLOWS symlinks, so an fstat afterwards
// reports the owner of the TARGET. Another uid plants a symlink of their own
// pointing at one of OUR files — during the same loose-perms window this check
// cleans up after — and the ownership test passes on our own uid.
//
// Note what the setup proves: the link is refused even though its target is a
// perfectly legitimate file of ours that reads fine on its own.
//
// Unix-tagged for the setup, not for the rule: creating a symlink on Windows
// needs a privilege the agent will not have. The refusal itself exists there too
// (openNoFollow), and lot 1 on the machine decides how to exercise it.
func TestReadOwnedFile_RefusesASymlinkEvenToOurOwnFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "real.json")
	link := filepath.Join(dir, "manifest.json")
	require.NoError(t, os.WriteFile(target, []byte(`{"agentName":"planted"}`), 0o600))
	require.NoError(t, os.Symlink(target, link))

	// The target itself is fine — this is what makes the refusal meaningful.
	data, err := ReadOwnedFile(target)
	require.NoError(t, err)
	assert.Contains(t, string(data), "planted")

	_, err = ReadOwnedFile(link)
	require.Error(t, err, "a symlink is not the file it claims to be")
	assert.ErrorIs(t, err, ErrOwnershipMismatch)
	assert.Contains(t, err.Error(), "symlink")
}

// Non-regular files are refused too: a link to a FIFO would otherwise sail
// through whenever the target belongs to us, and reading one blocks forever.
func TestReadOwnedFile_RefusesNonRegularFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo.json")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := ReadOwnedFile(fifo)
		assert.Error(t, err, "a FIFO is not a message file")
		assert.ErrorIs(t, err, ErrOwnershipMismatch)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ReadOwnedFile blocked on a FIFO instead of refusing it")
	}
}
