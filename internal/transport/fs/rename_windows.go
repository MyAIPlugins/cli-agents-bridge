//go:build windows

package fs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
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
// last handle closes. os.Remove, which DOES use them, succeeds on the very same
// held file — which is how the two halves were told apart.
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
// ELSE — an antivirus mid-scan, an editor, `type` — still blocks the replace.
// Those handles are transient by nature, so the same operation is repeated,
// briefly. It is NOT a fallback: nothing degrades to a copy, the operation
// retried is the same atomic one. If it never succeeds the original error comes
// back, enriched with how long we tried. Two mechanisms, two different causes;
// neither is redundant and removing either brings back a share of the defect.
const (
	// Six attempts at 10/20/40/80/160/320ms — 630ms of waiting at worst, chosen
	// so an antivirus scan of a small file passes under it while a real
	// permissions error (an ACL that will never yield) costs that much delay
	// before it is reported. That cost is declared rather than hidden: a
	// permanent ACCESS_DENIED is answered 630ms late.
	renameAttempts   = 6
	renameFirstDelay = 10 * time.Millisecond
)

// renameInfoHeaderLen is the size of FILE_RENAME_INFO before its FileName:
// Flags(4) + padding(4) + RootDirectory(8) + FileNameLength(4). Verified by
// offsetof rather than counted by eye.
const renameInfoHeaderLen = 20

// renameAtomic replaces newpath with oldpath. See the block above for why this
// is not os.Rename.
//
// The error shape is os.Rename's — *os.LinkError wrapping the platform error —
// because callers across the tree ask errors.Is(err, fs.ErrNotExist) and read
// the message, and a drop-in replacement that answers a different question is
// not a drop-in replacement.
func renameAtomic(oldpath, newpath string) error {
	h, err := openForRename(oldpath)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: err}
	}
	defer func() { _ = windows.CloseHandle(h) }()

	info, err := renameInfo(newpath)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: err}
	}

	started := time.Now()
	delay := renameFirstDelay
	var last error
	for attempt := 1; attempt <= renameAttempts; attempt++ {
		last = windows.SetFileInformationByHandle(h, windows.FileRenameInfoEx, &info[0], uint32(len(info)))
		if last == nil {
			return nil
		}
		if errors.Is(last, windows.ERROR_INVALID_PARAMETER) {
			// The info class itself was refused: this build of Windows does not
			// know FileRenameInfoEx. Named as a precondition rather than dressed
			// up as a failed rename, because no amount of retrying changes it and
			// the next reader of this message needs to know what to install, not
			// what to try again.
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath,
				Err: fmt.Errorf("atomic replace needs FILE_RENAME_POSIX_SEMANTICS, which this Windows does not support "+
					"(it arrived in Windows 10 1709 / Server 2019): %w", last)}
		}
		if !isTransientReplaceError(last) {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: last}
		}
		if attempt < renameAttempts {
			time.Sleep(delay)
			delay *= 2
		}
	}
	return &os.LinkError{Op: "rename", Old: oldpath, New: newpath,
		Err: fmt.Errorf("still blocked after %d attempts over %s — another process is holding the target open "+
			"without sharing deletion (antivirus, editor, an open shell): %w",
			renameAttempts, time.Since(started).Round(time.Millisecond), last)}
}

// isTransientReplaceError reports whether the replace is worth repeating.
//
// ONLY these two, and the restriction is the point: ERROR_NOT_SAME_DEVICE is
// permanent (a config that put temp and target on different volumes),
// ERROR_FILE_NOT_FOUND is permanent, and repeating either would turn an instant
// verdict into a 630ms one for no gain.
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
// Built as bytes rather than as a Go struct with a trailing array: the struct
// would be padded to its alignment and the length passed to Win32 would include
// that padding, which is the sort of thing that works until a field is added.
// FileNameLength is in BYTES and excludes the terminator.
func renameInfo(newpath string) ([]byte, error) {
	name, err := windows.UTF16FromString(newpath)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, renameInfoHeaderLen+len(name)*2)
	binary.LittleEndian.PutUint32(buf[0:], windows.FILE_RENAME_REPLACE_IF_EXISTS|windows.FILE_RENAME_POSIX_SEMANTICS)
	// RootDirectory stays 0: newpath is fully qualified.
	binary.LittleEndian.PutUint32(buf[16:], uint32((len(name)-1)*2))
	for i, c := range name {
		binary.LittleEndian.PutUint16(buf[renameInfoHeaderLen+i*2:], c)
	}
	return buf, nil
}
