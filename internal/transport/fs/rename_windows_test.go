//go:build windows

package fs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// F-133's oracle, and it is DELIBERATE rather than statistical.
//
// The defect was found as a flake — one run in seven — and a test that chases a
// flake by repeating it N times is not a test, it is a bet with a p-value. What
// makes the failure certain instead of likely is holding the handle open across
// the replace, which is the state the flake was a glimpse of.

// holdLikeABridgeReader opens path the way security.openNoFollow does, and the
// share mode is the whole point: FILE_SHARE_DELETE is what lets somebody else
// supersede the file while we read it.
//
// It is opened here rather than through the security package because that
// primitive is unexported. The two are pinned together by
// TestOpenNoFollow_GrantsShareDeleteSoTheFileCanBeSuperseded over there — if the
// production reader ever stops granting share-delete, that test goes red, not
// this one. Neither test alone proves the pair; this is the division.
func holdLikeABridgeReader(t *testing.T, path string) *os.File {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	require.NoError(t, err)
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	require.NoError(t, err)
	f := os.NewFile(uintptr(h), path)
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func plant(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

// TestRenameAtomic_HoldingAFileDoesNotBlockARenameOntoIt is F-133 itself.
func TestRenameAtomic_HoldingAFileDoesNotBlockARenameOntoIt(t *testing.T) {
	dir := t.TempDir()
	target := plant(t, dir, "manifest.json", "OLD")
	staged := plant(t, dir, ".tmp.staged", "NEW")

	held := holdLikeABridgeReader(t, target)

	require.NoError(t, renameAtomic(staged, target),
		"replacing a file another bridge process is reading must succeed — this is the whole defect")

	// The half that makes the fix safe rather than merely convenient: the handle
	// opened before the replace keeps reading the PREVIOUS contents, entire.
	// A reader that saw a mixture of the two would be a worse bug than the one
	// being fixed, and quieter.
	old, rerr := io.ReadAll(held)
	require.NoError(t, rerr)
	assert.Equal(t, "OLD", string(old),
		"the handle opened before the replace must still read the old contents, whole")

	fresh, ferr := os.ReadFile(target)
	require.NoError(t, ferr)
	assert.Equal(t, "NEW", string(fresh), "and a fresh open must see the new ones")
}

// TestRenameAtomic_ThePlainOsRenameControlStillFails keeps the test above
// honest.
//
// A green test proves nothing until it has been seen to go red for the right
// reason. os.Rename is MoveFileEx, which cannot supersede a held file no matter
// what share mode the holder granted — so it still reproduces the defect on
// demand. If this ever passes, the platform or Go changed and the oracle above
// has quietly stopped having a subject.
func TestRenameAtomic_ThePlainOsRenameControlStillFails(t *testing.T) {
	dir := t.TempDir()
	target := plant(t, dir, "manifest.json", "OLD")
	staged := plant(t, dir, ".tmp.staged", "NEW")

	holdLikeABridgeReader(t, target)

	err := os.Rename(staged, target)
	require.Error(t, err, "MoveFileEx must still refuse: this is the defect, reproduced on purpose")
	assert.True(t,
		errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION),
		"and refuse with the Win32 error F-133 is about, not some other one: %v", err)
}

// TestRenameAtomic_TargetAbsentBehavesLikeOsRename covers the branch the oracle
// above does not: the COMMON path.
//
// Both call-sites rename onto a name that usually is not there — the first write
// of a manifest, and every move into processed/, where the destination never
// exists. A replace primitive that only worked when the target was present would
// fix a rare defect by breaking the ordinary path, which is the worst way to
// close a lot.
func TestRenameAtomic_TargetAbsentBehavesLikeOsRename(t *testing.T) {
	dir := t.TempDir()
	staged := plant(t, dir, ".tmp.staged", "NEW")
	target := filepath.Join(dir, "does-not-exist-yet.json")

	require.NoError(t, renameAtomic(staged, target))
	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "NEW", string(got))
	assert.NoFileExists(t, staged, "and the source is consumed, as a rename consumes it")
}

// TestRenameAtomic_TargetPresentWithNobodyHoldingIt is the other ordinary case:
// an overwrite with no reader in sight, which is what happens almost every time.
func TestRenameAtomic_TargetPresentWithNobodyHoldingIt(t *testing.T) {
	dir := t.TempDir()
	staged := plant(t, dir, ".tmp.staged", "NEW")
	target := plant(t, dir, "manifest.json", "OLD")

	require.NoError(t, renameAtomic(staged, target))
	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "NEW", string(got))
}

// TestRenameAtomic_AMissingSourceStillAnswersNotExist guards the error SHAPE.
//
// Callers all over the tree ask errors.Is(err, fs.ErrNotExist) about what comes
// back from these two call-sites. A primitive that returned a bare Win32 errno
// instead of os.Rename's *os.LinkError would answer a different question, and
// the branches that depend on it would silently stop firing.
func TestRenameAtomic_AMissingSourceStillAnswersNotExist(t *testing.T) {
	dir := t.TempDir()
	err := renameAtomic(filepath.Join(dir, "not-here"), filepath.Join(dir, "target"))
	require.Error(t, err)
	assert.ErrorIs(t, err, fs.ErrNotExist, "a missing source must still read as not-exist: %v", err)

	var le *os.LinkError
	assert.True(t, errors.As(err, &le), "and keep os.Rename's error shape: %T", err)
}

// TestRenameAtomic_CrossVolumeIsRefusedWithoutRetrying covers the classifier.
//
// A different volume is a CONFIGURATION problem, permanent by nature: retrying
// it six times would turn an instant verdict into a 630ms one and teach nobody
// anything. The timing is the assertion — it is the only way to tell "refused"
// from "refused after exhausting the retries".
func TestRenameAtomic_CrossVolumeIsRefusedWithoutRetrying(t *testing.T) {
	other := crossVolumeDir(t)
	dir := t.TempDir()
	staged := plant(t, dir, ".tmp.staged", "NEW")

	started := time.Now()
	err := renameAtomic(staged, filepath.Join(other, "target.json"))
	elapsed := time.Since(started)

	require.Error(t, err)
	assert.Less(t, elapsed, 300*time.Millisecond,
		"a cross-volume rename is permanent and must not be retried; it took %s, and the full retry budget is 630ms", elapsed)
}

// crossVolumeDir finds a directory on a DIFFERENT volume, or skips by name.
//
// A skip that says which volume it looked for is the difference between "this
// passed" and "this was never attempted" — the only difference a reader of the
// output can act on.
func crossVolumeDir(t *testing.T) string {
	t.Helper()
	here := filepath.VolumeName(t.TempDir())
	for _, drive := range []string{"D:", "E:", "G:"} {
		if drive == here {
			continue
		}
		dir := filepath.Join(drive+`\`, "cab-bridge-crossvolume-test")
		if err := os.MkdirAll(dir, 0o700); err == nil {
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			return dir
		}
	}
	t.Skipf("no writable second volume next to %s; the cross-volume branch is NOT verified here", here)
	return ""
}

// TestAtomicWriteBytes_SucceedsWhileAReaderHoldsTheTarget is the same property
// one level up, through the API the bridge actually calls.
//
// The primitive can be right and the call-site still wired to os.Rename — that
// is precisely the shape of the defect this lot closes, so the wiring gets its
// own test rather than being taken on trust from a diff.
func TestAtomicWriteBytes_SucceedsWhileAReaderHoldsTheTarget(t *testing.T) {
	dir := t.TempDir()
	target := plant(t, dir, "manifest.json", "OLD")

	held := holdLikeABridgeReader(t, target)

	require.NoError(t, AtomicWriteBytes(target, []byte("NEW"), 0o600),
		"AtomicWriteBytes must go through the replace primitive, not os.Rename")

	old, rerr := io.ReadAll(held)
	require.NoError(t, rerr)
	assert.Equal(t, "OLD", string(old))

	fresh, ferr := os.ReadFile(target)
	require.NoError(t, ferr)
	assert.Equal(t, "NEW", string(fresh))
}

// TestMoveToProcessed_SucceedsWhileAReaderHoldsTheSource is the second
// call-site, and it exercises the branch the first one cannot: here the SOURCE
// is what somebody is reading, and the destination never exists.
func TestMoveToProcessed_SucceedsWhileAReaderHoldsTheSource(t *testing.T) {
	dir := t.TempDir()
	inbox := filepath.Join(dir, "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))
	msg := plant(t, inbox, "msg-abc.json", "BODY")

	holdLikeABridgeReader(t, msg)

	require.NoError(t, MoveToProcessed(msg, filepath.Join(dir, "processed")))
	assert.NoFileExists(t, msg, "the message must have left the inbox")
}
