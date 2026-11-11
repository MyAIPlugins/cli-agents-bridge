//go:build windows

package security

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// F-154, the half that needs the platform the defect lives on.
//
// The state under test is Windows' delete-pending: a file removed while a handle
// to it is open keeps its NAME, and every open-by-name answers ACCESS_DENIED
// until that handle closes. The tests build it with the same primitives
// production uses (openNoFollowOnce for the holder, os.Remove for the delete),
// and the wait is injected, so the handle is released at an exact attempt and no
// test sleeps for real.
//
// Every test below that depends on the premise ASSERTS it (deleteWhileHeld): if
// the state is not reproduced the test fails with that message, it does not
// skip. A skip here would be a green run that proved nothing.

// deleteWhileHeld plants name in dir, opens it the way a production reader does,
// removes it while held, and checks that the name is now denied. The holder is
// closed at cleanup if the test did not close it itself.
func deleteWhileHeld(t *testing.T, dir, name string) (string, *os.File) {
	t.Helper()
	path := plantFile(t, filepath.Join(dir, name), "GRANT")

	holder, err := openNoFollowOnce(path)
	require.NoError(t, err, "the holder must open")
	t.Cleanup(func() { _ = holder.Close() })
	require.NoError(t, os.Remove(path), "a file held with share-delete must be removable (F-133)")

	probe, err := openNoFollowOnce(path)
	if err == nil {
		_ = probe.Close()
		t.Fatalf("premise not reproduced: the removed-but-held name %q opened instead of being denied", path)
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("premise not reproduced: the removed-but-held name %q answered %v, not ACCESS_DENIED", path, err)
	}
	return path, holder
}

// symlinkOrSkip creates link -> target. A host that does not grant
// SeCreateSymbolicLinkPrivilege skips with the reason, the same way
// sharedelete_windows_test.go does; the VAL reads the verbose log of the final
// run for a PASS of these tests by name, because a skip proves nothing.
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skipf("symlink fixture needs SeCreateSymbolicLinkPrivilege, which this host does not grant; "+
				"the refusal is NOT verified here: %v", err)
		}
		require.NoError(t, err)
	}
}

// TestOpenNoFollow_DeniedWhileDeletePendingRidesOutToTheFileBeingGone is the
// defect itself: a reader arrives while the delete is pending, is told "denied",
// and used to give up. Now it waits, and when the holder lets go it receives the
// true answer: the file is gone.
func TestOpenNoFollow_DeniedWhileDeletePendingRidesOutToTheFileBeingGone(t *testing.T) {
	path, holder := deleteWhileHeld(t, t.TempDir(), "desktop-grant.json")

	var s sleeps
	f, err := openNoFollowSleeping(path, func(d time.Duration) {
		s.sleep(d)
		if len(s.waits) == 2 {
			require.NoError(t, holder.Close(), "releasing the last handle completes the delete")
		}
	})
	assert.Nil(t, f)
	require.Error(t, err)
	assert.True(t, os.IsNotExist(err), "the verdict must be the true one, a missing file: %v", err)
	assert.Equal(t, []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}, s.waits,
		"two waits, then the third attempt sees the name released")
}

// TestOpenNoFollow_DeniedForeverReportsTheRealErrorAfterTheBudget is the bound:
// a holder that never lets go is waited on five times and then reported, with
// the Win32 cause still reachable. Nothing here pretends the read succeeded.
func TestOpenNoFollow_DeniedForeverReportsTheRealErrorAfterTheBudget(t *testing.T) {
	path, _ := deleteWhileHeld(t, t.TempDir(), "desktop-grant.json")

	var s sleeps
	f, err := openNoFollowSleeping(path, s.sleep)
	assert.Nil(t, f)
	require.Error(t, err)
	assert.ErrorIs(t, err, windows.ERROR_ACCESS_DENIED, "the real cause, not a generic one")
	assert.Contains(t, err.Error(), "6 attempts")
	assert.Equal(t, []time.Duration{
		5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond,
	}, s.waits)
}

// TestOpenNoFollow_ASymlinkIsRefusedWithoutWaiting: the retry must not become a
// way to wait on something that is refused for good. A link is an
// ErrOwnershipMismatch on the first attempt and the loop never sleeps.
func TestOpenNoFollow_ASymlinkIsRefusedWithoutWaiting(t *testing.T) {
	dir := t.TempDir()
	target := plantFile(t, filepath.Join(dir, "real.json"), "REAL")
	link := filepath.Join(dir, "link.json")
	symlinkOrSkip(t, target, link)

	var s sleeps
	f, err := openNoFollowSleeping(link, s.sleep)
	assert.Nil(t, f)
	assert.ErrorIs(t, err, ErrOwnershipMismatch)
	assert.Empty(t, s.waits, "a refusal that cannot change must cost no waiting")
}

// TestOpenNoFollow_DeniedThenReplacedByALinkDuringTheWaitIsRefused is the case
// the retry could have weakened. The name is denied, the holder lets go during
// the wait, and somebody plants a symlink under the same name before the next
// attempt. Every attempt repeats the WHOLE Lstat + open + handle check, so the
// link is seen for what it is and refused: no handle is returned, so the file it
// points at cannot be read through this call.
func TestOpenNoFollow_DeniedThenReplacedByALinkDuringTheWaitIsRefused(t *testing.T) {
	dir := t.TempDir()
	secret := plantFile(t, filepath.Join(dir, "secret.json"), "SECRET")
	// The privilege is probed BEFORE the scenario starts: a skip decided in the
	// middle of a wait would leave the holder half-released.
	symlinkOrSkip(t, secret, filepath.Join(dir, "probe-link.json"))
	path, holder := deleteWhileHeld(t, dir, "desktop-grant.json")

	var s sleeps
	f, err := openNoFollowSleeping(path, func(d time.Duration) {
		s.sleep(d)
		if len(s.waits) == 2 {
			require.NoError(t, holder.Close())
			require.NoError(t, os.Symlink(secret, path), "the released name must be free to take a link")
		}
	})
	assert.Nil(t, f, "no handle may come back for a name that became a link")
	assert.ErrorIs(t, err, ErrOwnershipMismatch)
	assert.Equal(t, []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}, s.waits)
}
