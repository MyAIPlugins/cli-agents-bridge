//go:build windows

package fs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
)

// F-133 — atomic replace on Windows, and why it cannot be os.Rename.
//
// THE DEFECT. Every write this project performs ends in a rename onto a path
// some other bridge process may be reading at that instant: savemanifest,
// MoveToProcessed, every AtomicWriteBytes. On Windows those renames failed with
// "Access is denied" roughly one run in seven — a second `next` dying in
// `adopt: savemanifest`, a message never moved to processed/. Rare enough to
// look like the antivirus, which is what everybody assumed for nine days.
//
// WHAT IT IS NOT. It was never the antivirus, and it is not solved by opening
// our readers with FILE_SHARE_DELETE alone — the ratified design said it was,
// marked [D], and the measurement says otherwise:
//
//	reader os.Open            + os.Rename       Access is denied
//	reader os.Open            + this primitive  sharing violation
//	reader WITH share-delete  + os.Rename       Access is denied   ← the [D] that was wrong
//	reader WITH share-delete  + this primitive  succeeds
//
// os.Rename is MoveFileEx(MOVEFILE_REPLACE_EXISTING), which does not use POSIX
// semantics: it cannot supersede a target that still has an open handle,
// because the NAME stays occupied by a file in delete-pending state until the
// last handle closes. os.Remove is DeleteFileW and Go asks it for no POSIX flag
// (os/file_windows.go in Go 1.27.1), yet it succeeds on the very same held file,
// which is how the two halves were told apart. Measured on a Windows Server 2025
// runner (F-154, run 43017039092, and lab row L1 of run 43020768866): a file
// deleted while a share-delete reader holds it loses its NAME at once, so what
// os.Remove does there is POSIX in effect, and the OS provides it, not Go. Other
// Windows builds were not measured.
// The refusal F-154 is about is a different thing: a name that is being deleted
// answering "Access is denied" for a few milliseconds while the delete runs.
//
// SO IT TAKES BOTH HALVES, and they are not interchangeable:
//
//	share-delete in security.openNoFollow   makes our readers supersedable
//	FILE_RENAME_POSIX_SEMANTICS here        frees the name without waiting for them
//
// Neither works alone. The first is a precondition, not a fix.
//
// AND THE RETRY IS A THIRD THING, covering what neither can. POSIX semantics
// only work on handles that granted share-delete, so a file held by somebody
// ELSE — an antivirus mid-scan, an editor, `type` — still blocks the operation.
// Those handles are transient by nature, so the same operation is repeated,
// briefly. It is NOT a fallback: nothing degrades to a copy, the operation
// retried is the same atomic one. If it never succeeds the original error comes
// back, enriched with how long we tried. Two mechanisms, two different causes;
// neither is redundant and removing either brings back a share of the defect.
//
// THE RETRY COVERS BOTH FILES, and the first version of it did not — a critic's
// native probe is what showed the asymmetry, with the same transient lock moved
// from one file to the other:
//
//	somebody else holds the SOURCE, released after 50ms → sharing violation at 0ms
//	somebody else holds the TARGET, same transient       → success after 73ms
//
// The source open sat outside the loop, so a lock there was fatal and a lock on
// the target was survivable. And the source is the worse case of the two: at
// atomic.go's call-site it is the temp file THIS process just finished writing,
// which is exactly what an antivirus is scanning at that moment.
//
// THE LOOP IS NOT HERE. It is security.RetryLoop, the one loop of the repository, and
// this file keeps what is specific to the replace: WHICH errors are worth asking again
// about (isTransientReplaceError), the stage an error came from, the two diagnostics
// and the message. The budget below is the one in internal/security/denied_retry.go,
// where it sits next to the budget of reads and removals with the origin of each
// written down; these two names are only how this package refers to it, and a number
// written here instead would be a second budget that a test of the normal path never
// notices.
const (
	renameAttempts   = security.RenameAttempts
	renameFirstDelay = security.RenameFirstDelay
)

// fileRenameInfo mirrors the fixed part of FILE_RENAME_INFO. The FileName array
// is variable-length and follows in the buffer, so only the header is declared.
//
// It exists to DERIVE the header size rather than to be written through. The
// first version hardcoded 20, which bakes in a 64-bit pointer and the padding
// that follows Flags because of it; offsetof removes that assumption from the
// source.
//
// WHAT IS AND IS NOT ATTESTED HERE. The x64 layout was checked against the real
// SDK header with a native probe (sizeof 24, align 8, fields at 8/16/20) — the
// online documentation renders an ambiguous structure with the union field
// duplicated, so it is not a source. **amd64 is the only ABI anybody has run
// this on.** Removing a hardcoded assumption is not the same as verifying the
// other case, and this comment does not claim the second.
type fileRenameInfo struct {
	Flags         uint32
	RootDirectory windows.Handle
	// FileNameLength is the LAST fixed field; FileName starts immediately after.
	FileNameLength uint32
}

// renameInfoHeaderLen is where FileName begins — derived, not counted.
const renameInfoHeaderLen = unsafe.Offsetof(fileRenameInfo{}.FileNameLength) +
	unsafe.Sizeof(fileRenameInfo{}.FileNameLength)

// renameStage says which half of the operation produced an error, because the
// two are not classified the same way: only the replace can tell us the info
// class was refused.
type renameStage int

const (
	stageOpen renameStage = iota
	stageReplace
)

// renameAtomic replaces newpath with oldpath. See the block above for why this
// is not os.Rename.
//
// The error shape is os.Rename's — *os.LinkError wrapping the platform error —
// because callers across the tree ask errors.Is(err, fs.ErrNotExist) and read
// the message, and a drop-in replacement that answers a different question is
// not a drop-in replacement.
func renameAtomic(oldpath, newpath string) error {
	_, err := renameAtomicTraced(oldpath, newpath)
	return err
}

// renameSleep is time.Sleep, and it is a variable so a test can DRIVE the retry
// loop instead of waiting on it.
//
// The budget test used to assert wall-clock time with a second of slack, and a
// critic showed what that bought: doubling the backoff constant left it green.
// It could tell "there is a retry" from "there is none", which is not the
// property its name promised. Taking the clock out of the oracle entirely is the
// same move F-138 made on the heartbeat test the same afternoon — from measuring
// time to observing the property.
var renameSleep = time.Sleep

// renameTrace is what the operation DID, for a test that needs to observe the
// budget rather than time it. Production reads none of it.
type renameTrace struct {
	attempts     int
	waits        int
	openFails    int
	replaceFails int
}

func renameAtomicTraced(oldpath, newpath string) (renameTrace, error) {
	info, err := renameInfo(newpath)
	if err != nil {
		return renameTrace{}, renameLinkError(oldpath, newpath, err)
	}
	return renameRetrying(oldpath, newpath, func() (renameStage, error) {
		return tryReplace(oldpath, info)
	})
}

// renameLinkError gives an error the shape of os.Rename's: *os.LinkError, with the
// platform error underneath so errors.Is and errors.As keep working through it.
func renameLinkError(oldpath, newpath string, err error) error {
	return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: err}
}

// renameRetrying is the whole of what this file decides about repeating: it hands ONE
// attempt (open the source, replace the target) to the common loop with this operation's
// budget and predicate, then says what the outcome means. The attempt arrives as an
// argument and not as a call to tryReplace so that a test can inject the stage and the
// error of every attempt and run THIS body, with its two diagnostic branches, without a
// Windows syscall (rename_retry_windows_test.go).
//
// The elapsed time is measured here and not by the loop because the message of the
// exhausted budget reports it (what the syscalls and the scheduler really added to the
// backoff), and a loop that wrapped the error would take that text away from this file.
func renameRetrying(oldpath, newpath string, attempt func() (renameStage, error)) (renameTrace, error) {
	var trace renameTrace
	var stage renameStage
	started := time.Now()

	wait := func(d time.Duration) {
		trace.waits++
		renameSleep(d)
	}
	last, exhausted := security.RetryLoop(renameAttempts, renameFirstDelay, wait, isTransientReplaceError, func() error {
		trace.attempts++
		var err error
		stage, err = attempt()
		switch {
		case err == nil: // nothing to count
		case stage == stageOpen:
			trace.openFails++
		default:
			trace.replaceFails++
		}
		return err
	})

	switch {
	case last == nil:
		return trace, nil

	case exhausted:
		// WHAT THIS DOES NOT KNOW, and the previous version of this sentence claimed
		// to: it said another process was holding one of the files open. A critic's
		// probe produced this exact error with NOBODY else involved — a read-only
		// attribute on the target, reported as ACCESS_DENIED after the full budget,
		// under a message accusing a process that did not exist.
		//
		// It is the same defect as the INVALID_PARAMETER message below, which had
		// already been corrected: a cause DEDUCED from an error code. Fixing one and
		// leaving its sibling is the branch next door in the narrowest possible form.
		//
		// So the possibilities are listed, none is asserted, and the platform errno
		// is carried through for whoever can tell them apart.
		return trace, renameLinkError(oldpath, newpath, fmt.Errorf("still refused after %d attempts over %s, and the cause is NOT established here: "+
			"another process may be holding one of the two files open without sharing deletion "+
			"(antivirus, editor, an open shell), or it may be permissions or a file attribute — "+
			"a read-only target produces this same error with nobody else involved: %w",
			renameAttempts, time.Since(started).Round(time.Millisecond), last))

	case stage == stageReplace && errors.Is(last, windows.ERROR_INVALID_PARAMETER):
		// The info class was refused. WHAT THAT MEANS IS NOT KNOWN HERE, and
		// an earlier version of this message claimed it was: it said "this
		// Windows does not support it", which is a diagnosis, not an
		// observation. The same error also comes back from a filesystem that
		// has no POSIX rename — an SMB share, FAT — so that sentence would
		// have accused a perfectly up-to-date machine of being old. The
		// stdlib makes the same distinction (at_windows.go).
		//
		// So the message states the REQUIREMENT and leaves the cause open.
		// It is still refused rather than retried or worked around: falling
		// back to MoveFileEx would reintroduce this very defect, with the
		// aggravation of code claiming to have fixed it.
		//
		// Only at the replace stage: the same code from the open is an ordinary
		// error and takes the default branch.
		return trace, renameLinkError(oldpath, newpath, fmt.Errorf("atomic replace refused by SetFileInformationByHandle(FileRenameInfoEx): "+
			"POSIX rename semantics are a precondition of this bridge and something here does not provide them — "+
			"they need Windows 10 1709 / Server 2019 or later AND a filesystem that implements them "+
			"(NTFS does; SMB shares and FAT typically do not). Not a transient failure: %w", last))

	default:
		// Not worth repeating (the loop already stopped at the attempt where it
		// turned up): the error as it came, in os.Rename's shape.
		return trace, renameLinkError(oldpath, newpath, last)
	}
}

// tryReplace performs ONE attempt: open the source, then replace the target.
//
// The open is INSIDE the attempt, which is the whole point of the stage return:
// a lock on the source is as transient as a lock on the target and belongs to
// the same budget.
func tryReplace(oldpath string, info []byte) (renameStage, error) {
	h, err := openForRename(oldpath)
	if err != nil {
		return stageOpen, err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return stageReplace, windows.SetFileInformationByHandle(h, windows.FileRenameInfoEx, &info[0], uint32(len(info)))
}

// isTransientReplaceError reports whether the operation is worth repeating.
//
// ONLY these two, and the restriction is the point: ERROR_NOT_SAME_DEVICE is
// permanent (a config that put temp and target on different volumes),
// ERROR_FILE_NOT_FOUND is permanent, and repeating either would spend the whole
// backoff allowance to reach the same verdict.
func isTransientReplaceError(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}

// openForRename opens the SOURCE for the rename: DELETE is the access a rename
// needs, SYNCHRONIZE lets the call be a synchronous one.
//
// FILE_FLAG_OPEN_REPARSE_POINT so a symlink is moved as itself rather than
// resolved — moving what the link points at would be a different operation than
// the caller asked for. FILE_FLAG_BACKUP_SEMANTICS for the same reason os.Open
// passes it: without it a directory cannot be opened at all.
func openForRename(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(
		p,
		windows.DELETE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
}

// renameInfo builds the FILE_RENAME_INFO buffer for FileRenameInfoEx.
//
// Built as bytes rather than by writing through the struct: the struct is padded
// to its alignment, so its Sizeof would include trailing padding that is not
// part of what Win32 is being handed. The header offsets still come from the
// struct — see fileRenameInfo — so nothing here is counted by eye.
//
// FileNameLength is in BYTES and excludes the terminator.
func renameInfo(newpath string) ([]byte, error) {
	name, err := windows.UTF16FromString(newpath)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, int(renameInfoHeaderLen)+len(name)*2)
	binary.LittleEndian.PutUint32(buf[unsafe.Offsetof(fileRenameInfo{}.Flags):],
		windows.FILE_RENAME_REPLACE_IF_EXISTS|windows.FILE_RENAME_POSIX_SEMANTICS)
	// RootDirectory stays 0: newpath is fully qualified.
	binary.LittleEndian.PutUint32(buf[unsafe.Offsetof(fileRenameInfo{}.FileNameLength):],
		uint32((len(name)-1)*2))
	for i, c := range name {
		binary.LittleEndian.PutUint16(buf[int(renameInfoHeaderLen)+i*2:], c)
	}
	return buf, nil
}
