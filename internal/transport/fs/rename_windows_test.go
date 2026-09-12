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

// TestRenameAtomic_ASourceLockedByAnotherOpenIsRetriedUntilItIsReleased is the
// half of the retry the first version did not have, and it is the worse half.
//
// A critic's native probe moved the same transient lock from one file to the
// other and got opposite outcomes: held TARGET, success after 73ms; held
// SOURCE, sharing violation at 0ms. The source open sat outside the retry loop,
// so a lock there was fatal while a lock on the target was survivable.
//
// And the source is the file more likely to be locked, not less: at
// AtomicWriteBytes' call-site it is the temp file THIS process has just finished
// writing — precisely what an antivirus is reading at that moment.
//
// os.Open is the right holder here because it grants no FILE_SHARE_DELETE, which
// is exactly what a scanner or an editor looks like from our side.
func TestRenameAtomic_ASourceLockedByAnotherOpenIsRetriedUntilItIsReleased(t *testing.T) {
	dir := t.TempDir()
	staged := plant(t, dir, ".tmp.staged", "NEW")
	target := plant(t, dir, "manifest.json", "OLD")

	holder, err := os.Open(staged)
	require.NoError(t, err)
	released := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = holder.Close()
		close(released)
	}()

	require.NoError(t, renameAtomic(staged, target),
		"a transient lock on the SOURCE must be waited out like one on the target")
	<-released

	got, rerr := os.ReadFile(target)
	require.NoError(t, rerr)
	assert.Equal(t, "NEW", string(got))
}

// TestRenameAtomic_TheBudgetIsONEAndCoversBothStages is the oracle for the
// single budget, and it does not look at the clock at all.
//
// THE TEST BELOW IS NOT THIS ORACLE, which is why both exist. It asserts wall
// time with a second of slack, and a critic showed exactly what that buys:
// doubling the backoff constant — real pauses of 620ms instead of 310 — left it
// GREEN. It can tell "there is a retry" from "there is none"; it cannot tell one
// budget from two, which is the property its name promised. And it could not,
// even with a tighter bound: with the source locked forever the replace stage is
// never reached, so there is no second stage for a second budget to show up in.
//
// So the sleeper is driven from here instead. No time passes, nothing is timed,
// and the scenario consumes attempts in BOTH stages of one operation: the source
// is locked first, then released while the target is locked in its place. The
// property is then plainly countable — the two stages together must fit in ONE
// allowance of waits, not one allowance each.
//
// Stubbing a package variable is safe here because this test does not call
// t.Parallel(): Go runs the parallel tests of a package only after the
// sequential ones finish, and the cleanup restores the sleeper before then.
func TestRenameAtomic_TheBudgetIsONEAndCoversBothStages(t *testing.T) {
	dir := t.TempDir()
	staged := plant(t, dir, ".tmp.staged", "NEW")
	target := plant(t, dir, "manifest.json", "OLD")

	// os.Open grants no FILE_SHARE_DELETE, so it blocks whichever file it holds:
	// the source against the open stage, the target against the replace.
	srcHold, err := os.Open(staged)
	require.NoError(t, err)
	defer func() { _ = srcHold.Close() }()
	var tgtHold *os.File
	defer func() {
		if tgtHold != nil {
			_ = tgtHold.Close()
		}
	}()

	original := renameSleep
	t.Cleanup(func() { renameSleep = original })

	waits := 0
	var slept time.Duration
	renameSleep = func(d time.Duration) {
		waits++
		slept += d
		if waits == 2 {
			// Hand the lock over: from here on the open succeeds and the replace
			// is the one being refused.
			_ = srcHold.Close()
			var oerr error
			tgtHold, oerr = os.Open(target)
			require.NoError(t, oerr)
		}
	}

	trace, err := renameAtomicTraced(staged, target)

	require.Error(t, err, "both files are locked in turn, so the operation cannot succeed")
	assert.Positive(t, trace.openFails, "the source lock must have consumed attempts")
	assert.Positive(t, trace.replaceFails, "and the target lock must have consumed some of the SAME ones")
	assert.Equal(t, renameAttempts, trace.attempts,
		"the two stages together must fit in ONE allowance of %d attempts", renameAttempts)
	assert.Equal(t, renameAttempts-1, trace.waits,
		"and in ONE allowance of waits: %d attempts have %d gaps between them, and a second budget "+
			"would show up here as more", renameAttempts, renameAttempts-1)

	// AND THE DURATIONS IT ASKED FOR, summed, must be the backoff the constants
	// describe. This is what catches a backoff that quietly changes scale: the
	// critic's mutation multiplied the starting delay inside the function, so the
	// real pauses doubled while the constants — and therefore the comment that
	// quotes them — stayed put. Nothing timed anything: the expected figure is
	// derived here and the actual one is what the loop handed the sleeper.
	expected := time.Duration(0)
	for i, d := 1, renameFirstDelay; i < renameAttempts; i, d = i+1, d*2 {
		expected += d
	}
	assert.Equal(t, expected, slept,
		"the loop must ask for exactly the backoff its constants describe, or the number in the comment "+
			"is about a function that no longer exists")
}

// TestRenameAtomic_ASourceLockedForeverFailsWithinOneBudget is the SMOKE that
// sits next to the oracle above: it uses the real sleeper and a real lock, so it
// shows the thing works end to end rather than only under a stub.
//
// Its bound is deliberately loose and it is NOT the budget oracle — see above
// for why a wall-clock assertion cannot be one.
func TestRenameAtomic_ASourceLockedForeverFailsWithinOneBudget(t *testing.T) {
	dir := t.TempDir()
	staged := plant(t, dir, ".tmp.staged", "NEW")
	target := plant(t, dir, "manifest.json", "OLD")

	holder, err := os.Open(staged)
	require.NoError(t, err)
	defer func() { _ = holder.Close() }()

	started := time.Now()
	err = renameAtomic(staged, target)
	elapsed := time.Since(started)

	require.Error(t, err, "a lock that never lifts must be reported, not waited on forever")
	assert.Contains(t, err.Error(), "attempts", "and the error must say how long it tried: %v", err)

	// The budget is COMPUTED from the two constants, never written down twice.
	// Writing "630ms" here is what the production comment did, and it was wrong:
	// six attempts have five gaps between them, so the sixth delay is never
	// spent. A literal in the test would have agreed with the mistaken comment
	// instead of catching it.
	budget := time.Duration(0)
	for i, d := 1, renameFirstDelay; i < renameAttempts; i, d = i+1, d*2 {
		budget += d
	}
	assert.Greater(t, elapsed, budget/2,
		"it must actually have used the budget (%s) rather than giving up at once", budget)
	assert.Less(t, elapsed, budget+time.Second,
		"and ONE budget, not one per stage: %s against a budget of %s", elapsed, budget)

	// Nothing was consumed and nothing was half-done: the refusal is safe.
	assert.FileExists(t, staged, "the source must survive a refused replace")
	got, rerr := os.ReadFile(target)
	require.NoError(t, rerr)
	assert.Equal(t, "OLD", string(got), "and the target must be untouched")
}

// TestMoveToProcessed_RetriesWhileTheSourceIsBrieflyLocked exists because the
// OTHER MoveToProcessed test does not discriminate the fix.
//
// Mutation testing showed it: with renameAtomic reduced to os.Rename, the
// held-target test and the AtomicWriteBytes test both go red, while
// MoveToProcessed_SucceedsWhileAReaderHoldsTheSource stays GREEN — os.Rename is
// enough when the source grants share-delete and the destination does not exist.
// It asserts something true and nothing new.
//
// This one does: a source locked WITHOUT share-delete defeats os.Rename and the
// unretried open alike, and is survived only by the loop.
func TestMoveToProcessed_RetriesWhileTheSourceIsBrieflyLocked(t *testing.T) {
	dir := t.TempDir()
	inbox := filepath.Join(dir, "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))
	msg := plant(t, inbox, "msg-abc.json", "BODY")

	holder, err := os.Open(msg)
	require.NoError(t, err)
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = holder.Close()
	}()

	require.NoError(t, MoveToProcessed(msg, filepath.Join(dir, "processed")))
	assert.NoFileExists(t, msg, "the message must have left the inbox")
}

// TestRenameAtomic_CrossVolumeIsRefusedWithoutRetrying covers the classifier.
//
// A different volume is a CONFIGURATION problem, permanent by nature: retrying
// it six times would spend the whole backoff allowance to reach the same verdict
// and teach nobody anything. The timing is one assertion — the only way to tell "refused" from
// "refused after exhausting the retries".
//
// But timing alone left the test green for ANY quick failure: a bad path, a
// permission, a broken fixture would all have read as "the cross-volume branch
// is handled". So it also names the error and checks what the refusal left
// behind.
func TestRenameAtomic_CrossVolumeIsRefusedWithoutRetrying(t *testing.T) {
	other := crossVolumeDir(t)
	dir := t.TempDir()
	staged := plant(t, dir, ".tmp.staged", "NEW")

	started := time.Now()
	err := renameAtomic(staged, filepath.Join(other, "target.json"))
	elapsed := time.Since(started)

	require.Error(t, err)
	assert.Less(t, elapsed, 300*time.Millisecond,
		"a cross-volume rename is permanent and must not be retried; it took %s, which must stay well under "+
			"the time a full round of retries would take", elapsed)

	// ERROR_NOT_SAME_DEVICE, by name. This is also the executable footnote to
	// F-137: it is the constant Windows actually returns, and the one
	// errors.Is(err, syscall.EXDEV) does NOT recognise — which is why the EXDEV
	// branches in atomic.go and process.go are dead on this platform. Naming the
	// real value here records that without touching those files.
	assert.ErrorIs(t, err, windows.ERROR_NOT_SAME_DEVICE,
		"and it must fail for the reason the branch is about, not merely fail: %v", err)

	// A refusal that consumed the source would be worse than the defect: the
	// message would be gone and the target never written.
	assert.FileExists(t, staged, "the source must survive a refused cross-volume replace")
	got, rerr := os.ReadFile(staged)
	require.NoError(t, rerr)
	assert.Equal(t, "NEW", string(got), "and survive intact, not truncated")

	// The other half of the non-consumption contract: a refusal must not have
	// created a partial destination either. "Nothing moved" is two claims, and
	// only one of them was being checked.
	assert.NoFileExists(t, filepath.Join(other, "target.json"),
		"and the destination must not exist at all after a refused replace")
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
		// MkdirTemp, NOT MkdirAll with a fixed name, and the whole difference is
		// the RemoveAll underneath. MkdirAll SUCCEEDS on a directory that already
		// exists — it is not an error — so a fixed name outside t.TempDir()
		// ADOPTS whatever is already there and then deletes it, contents
		// included. That is the shape that cost thirteen archived sessions in
		// August: a destructive command aimed by a path somebody trusted instead
		// of by an isolation somebody created.
		//
		// And the collision to worry about is not a stranger's directory, it is
		// OURS. This project runs its suite in several worktrees at once, so two
		// runs would meet on one fixed name on the same volume and one would
		// RemoveAll while the other was still working inside it — surfacing as an
		// intermittent, inexplicable red, which is precisely what this lot exists
		// to remove.
		//
		// A unique name per run means the cleanup can only reach what this run
		// made. "The name is specific enough" is the reasoning, not the defence.
		dir, err := os.MkdirTemp(drive+`\`, "cab-bridge-xvol-")
		if err == nil {
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
//
// AND IT DOES NOT DISCRIMINATE THE FIX — said here so nobody reads it as the
// F-133 oracle it looks like. Mutation testing proved it: with renameAtomic
// reduced to os.Rename this test stays GREEN, because os.Rename is enough when
// the source granted share-delete and the target does not exist. What it pins is
// real and worth keeping — the common path must not break — but the
// discriminating sibling is
// TestMoveToProcessed_RetriesWhileTheSourceIsBrieflyLocked.
func TestMoveToProcessed_SucceedsWhileAReaderHoldsTheSource(t *testing.T) {
	dir := t.TempDir()
	inbox := filepath.Join(dir, "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))
	msg := plant(t, inbox, "msg-abc.json", "BODY")

	holdLikeABridgeReader(t, msg)

	require.NoError(t, MoveToProcessed(msg, filepath.Join(dir, "processed")))
	assert.NoFileExists(t, msg, "the message must have left the inbox")
}

// TestMoveToProcessed_CrossVolumeIsNamedAsSuch is F-137's oracle.
//
// The branch it covers was DEAD on Windows: the call-site asked
// errors.Is(err, syscall.EXDEV), and a real cross-volume move returns
// ERROR_NOT_SAME_DEVICE, which is a different constant that does not match it.
// So the explicit "this is a config bug, not a transient failure" message never
// arrived here — while the comment above the branch announced that it did.
//
// MoveToProcessed is the reachable call-site of the pair: its destination comes
// from the caller, so a directory on another volume is a configuration somebody
// can actually produce. AtomicWriteBytes cannot reach it — its temp file is
// created in the target's own directory — which is why there is no twin of this
// test for it, and why that is said out loud there rather than left as a gap.
func TestMoveToProcessed_CrossVolumeIsNamedAsSuch(t *testing.T) {
	other := crossVolumeDir(t)
	dir := t.TempDir()
	inbox := filepath.Join(dir, "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))
	msg := plant(t, inbox, "msg-abc.json", "BODY")

	err := MoveToProcessed(msg, other)
	require.Error(t, err, "a move onto another volume cannot be atomic and must fail")

	// The MESSAGE, because the whole point of the branch is which sentence the
	// reader gets: the generic one sends them looking for a transient fault.
	assert.Contains(t, err.Error(), "cross-device",
		"it must be named as a cross-device failure, not reported generically: %v", err)
	assert.Contains(t, err.Error(), "config bug",
		"and told apart from a transient error, which is the distinction the branch exists to draw: %v", err)

	// And nothing was consumed: a refused move must leave the message where it
	// was, or the branch would be worse than the generic error it replaces.
	assert.FileExists(t, msg, "the message must survive a refused cross-device move")
	got, rerr := os.ReadFile(msg)
	require.NoError(t, rerr)
	assert.Equal(t, "BODY", string(got), "and survive intact")
}
