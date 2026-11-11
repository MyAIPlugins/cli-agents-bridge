//go:build windows

package security

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// F-154, the half that needs the platform the defect lives on.
//
// WHAT THE FIXTURE IS, AND WHAT IT IS NOT. The field defect is a transient
// ACCESS_DENIED on a name that another party is deleting (measured, PR #25). The
// kernel state behind it is not established, and a first fixture built on
// "os.Remove of a file somebody holds leaves the name pending" was REFUTED by the
// first run on a Windows Server 2025 runner: there the name is free the moment
// os.Remove returns (twin run 43017039092, the premise assertion below, which is
// why it is an assertion and not an assumption).
//
// The fixture here (deleteInFlight) does by hand what a delete does on its way:
// it opens the file with DELETE access, sets the delete disposition, and KEEPS the
// handle open. Until that handle closes, an open or a stat of the name is answered
// STATUS_DELETE_PENDING, which Win32 reports as ACCESS_DENIED, and closing the
// handle completes the delete. That is a real ACCESS_DENIED from the real API,
// deterministic, released at an exact attempt through the injected wait, and
// independent of how any Windows build implements DeleteFile. It proves what the
// retry does with such an answer; that this is the answer the field defect gives
// is what the cause measurement and the twin runs are for.
//
// Every test that depends on the premise ASSERTS it: if the state is not
// reproduced the test fails with that message, it does not skip. A skip here
// would be a green run that proved nothing.

// fileDispositionInfo mirrors FILE_DISPOSITION_INFO, which is one BOOLEAN: one
// byte, and SetFileInformationByHandle checks the size.
type fileDispositionInfo struct{ DeleteFile byte }

// deleteInFlight plants name in dir and puts it in the state of a delete that has
// not finished: delete disposition set, deleting handle still open. release closes
// that handle, which completes the delete; it is idempotent and also runs at
// cleanup. The premise is asserted before returning.
func deleteInFlight(t *testing.T, dir, name string) (path string, release func()) {
	t.Helper()
	path = plantFile(t, filepath.Join(dir, name), "GRANT")
	p, err := windows.UTF16PtrFromString(path)
	require.NoError(t, err)

	h, err := windows.CreateFile(p, windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	require.NoError(t, err, "CreateFile(DELETE) on the planted file")

	closed := false
	release = func() {
		if closed {
			return
		}
		closed = true
		if cerr := windows.CloseHandle(h); cerr != nil {
			t.Errorf("CloseHandle of the deleting handle: %v", cerr)
		}
	}
	t.Cleanup(release)

	info := fileDispositionInfo{DeleteFile: 1}
	require.NoError(t, windows.SetFileInformationByHandle(h, windows.FileDispositionInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))), "SetFileInformationByHandle(FileDispositionInfo)")

	probe, err := openNoFollowOnce(path)
	if err == nil {
		_ = probe.Close()
		t.Fatalf("premise not reproduced: the name %q opened while its delete was in flight instead of being denied", path)
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("premise not reproduced: the name %q answered %v while its delete was in flight, not ACCESS_DENIED", path, err)
	}
	return path, release
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
// defect itself: a reader arrives while a delete is in flight, is told "denied",
// and used to give up. Now it waits, and when the delete completes it receives the
// true answer: the file is gone.
func TestOpenNoFollow_DeniedWhileDeletePendingRidesOutToTheFileBeingGone(t *testing.T) {
	path, release := deleteInFlight(t, t.TempDir(), "desktop-grant.json")

	var s sleeps
	f, err := openNoFollowSleeping(path, func(d time.Duration) {
		s.sleep(d)
		if len(s.waits) == 2 {
			release()
		}
	})
	assert.Nil(t, f)
	require.Error(t, err)
	assert.True(t, os.IsNotExist(err), "the verdict must be the true one, a missing file: %v", err)
	assert.Equal(t, []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}, s.waits,
		"two waits, then the third attempt sees the delete completed")
}

// TestOpenNoFollow_DeniedForeverReportsTheRealErrorAfterTheBudget is the bound:
// a delete that never completes is waited on five times and then reported, with
// the Win32 cause still reachable. Nothing here pretends the read succeeded.
func TestOpenNoFollow_DeniedForeverReportsTheRealErrorAfterTheBudget(t *testing.T) {
	path, _ := deleteInFlight(t, t.TempDir(), "desktop-grant.json")

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
// the retry could have weakened. The name is denied, the delete completes during
// the wait, and somebody plants a symlink under the same name before the next
// attempt. Every attempt repeats the WHOLE Lstat + open + handle check, so the
// link is seen for what it is and refused: no handle is returned, so the file it
// points at cannot be read through this call.
func TestOpenNoFollow_DeniedThenReplacedByALinkDuringTheWaitIsRefused(t *testing.T) {
	dir := t.TempDir()
	secret := plantFile(t, filepath.Join(dir, "secret.json"), "SECRET")
	// The privilege is probed BEFORE the scenario starts: a skip decided in the
	// middle of a wait would leave the delete half done.
	symlinkOrSkip(t, secret, filepath.Join(dir, "probe-link.json"))
	path, release := deleteInFlight(t, dir, "desktop-grant.json")

	var s sleeps
	f, err := openNoFollowSleeping(path, func(d time.Duration) {
		s.sleep(d)
		if len(s.waits) == 2 {
			release()
			require.NoError(t, os.Symlink(secret, path), "the name freed by the completed delete must be free to take a link")
		}
	})
	assert.Nil(t, f, "no handle may come back for a name that became a link")
	assert.ErrorIs(t, err, ErrOwnershipMismatch)
	assert.Equal(t, []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}, s.waits)
}
