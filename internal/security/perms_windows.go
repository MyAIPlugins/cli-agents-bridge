//go:build windows

package security

import (
	"fmt"
	"io/fs"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Windows half of the ownership model. Nothing here was executed on Windows
// while it was written — it was cross-compiled from a Mac — so every error names
// the Win32 call that produced it: when this breaks on the machine, the message
// says which call, not just that something failed.
//
// What replaces what:
//
//	uid in a Stat_t  →  the owner SID of the object's security descriptor
//	os.Getuid()      →  the token's user SID and its DEFAULT OWNER SID
//	O_NOFOLLOW       →  Lstat, refuse, then open (a TOCTOU, declared below)
//	mode & 0o077     →  nothing. Perm() is 0777/0555 here; the ACLs inherited
//	                    from %USERPROFILE% are what stands in for it

// ownerCheckPath verifies that path belongs to this process's user. info comes
// from the caller's Stat/Lstat and is deliberately unused: a Win32FileAttributeData
// carries no owner, so the answer has to be fetched by name. The parameter stays
// because the Unix implementation reads its answer out of it — one signature, two
// ways of arriving at the same verdict.
//
// GetNamedSecurityInfo FOLLOWS reparse points. Every caller that cares has
// already refused a symlink via Lstat before reaching here (CheckOwnedDir does,
// openNoFollow does); CheckOwnership follows links on Unix too, so the two
// platforms agree.
func ownerCheckPath(path string, info fs.FileInfo) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("GetNamedSecurityInfo %q: %w", path, err)
	}
	return compareOwner(path, sd)
}

// ownerCheckFile is the descriptor-based counterpart, and the distinction is the
// whole point of openOwned: the handle is interrogated, not the name, so nothing
// can be swapped underneath between the open and the check. info is unused for
// the same reason as above.
//
// f was opened for reading, which includes READ_CONTROL — the right
// GetSecurityInfo needs to return an owner.
func ownerCheckFile(f *os.File, info fs.FileInfo) error {
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("GetSecurityInfo %q: %w", f.Name(), err)
	}
	return compareOwner(f.Name(), sd)
}

// compareOwner accepts the object when its owner is either SID this process
// would answer with: the token USER, and the token's DEFAULT OWNER — the SID
// Windows stamps on objects this process creates.
//
// Both are needed, and each covers a case the other does not:
//
//   - Not elevated: default owner == user, so this is one comparison and the
//     ordinary case is exact.
//   - Elevated: Windows stamps new objects with BUILTIN\Administrators, not with
//     the user. Comparing against the user alone would make us refuse OUR OWN
//     files, and since the boot check (SC-7) runs on every single command, the
//     binary would refuse to start at all.
//
// Two limits, neither of which is visible from the code, so both are written here
// and belong in SECURITY.md:
//
//   - IT REFUSES OURS: a file created by an elevated shell (owner
//     Administrators) and read later by a NON-elevated shell of the same user is
//     rejected — that token's default owner is the user, and the group is
//     deny-only in it. The case is "I ran it once from an admin PowerShell". The
//     error names the owner and says so, so the message explains itself.
//   - IT ACCEPTS SOMEBODY ELSE'S: while elevated, files owned by any OTHER
//     elevated administrator pass, because both carry the same group SID. Under
//     %USERPROFILE% the inherited ACLs already prevent that file from being there
//     at all; it is a declared limit, not a hole.
func compareOwner(path string, sd *windows.SECURITY_DESCRIPTOR) error {
	// Documented in x/sys: the descriptor can be nil with a nil error when the
	// object exists but carries no security information — a filesystem without
	// ACLs (FAT32/exFAT on a removable drive, some network mounts). Ownership is
	// then not merely mismatched, it is UNANSWERABLE.
	//
	// Accepted with a one-time warning, which is the same shape Unix gives the
	// root case: a check that cannot discriminate is announced rather than
	// silently failed open, and rather than bricking every command. Not silent,
	// not fatal — the third option is the wrong one here.
	if sd == nil {
		warnNoSecurityInfo(path)
		return nil
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("SECURITY_DESCRIPTOR.Owner %q: %w", path, err)
	}
	if owner == nil {
		warnNoSecurityInfo(path)
		return nil
	}

	ours, err := processOwnerSIDs()
	if err != nil {
		return fmt.Errorf("ownership check %q: %w", path, err)
	}
	for _, sid := range ours {
		if owner.Equals(sid) {
			return nil
		}
	}

	detail := ""
	if owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		detail = " (owner is BUILTIN\\Administrators: the file was created by an elevated process; re-run elevated, or delete it)"
	}
	return fmt.Errorf("%w: path=%q file_owner=%s current_owner=%s%s",
		ErrOwnershipMismatch, path, owner, ours[0], detail)
}

var (
	ownerSIDsOnce sync.Once
	ownerSIDs     []*windows.SID
	ownerSIDsErr  error
)

// processOwnerSIDs returns the token user SID first, then the token's default
// owner SID when it differs. Both are fixed for the life of the process, so they
// are resolved once: this runs on every file read.
func processOwnerSIDs() ([]*windows.SID, error) {
	ownerSIDsOnce.Do(func() {
		// A pseudo-token: no handle to close, no leak to get wrong on an error path.
		tok := windows.GetCurrentProcessToken()

		user, err := tok.GetTokenUser()
		if err != nil {
			ownerSIDsErr = fmt.Errorf("GetTokenUser: %w", err)
			return
		}
		sids := []*windows.SID{user.User.Sid}

		if def, derr := tokenDefaultOwner(tok); derr != nil {
			// Not fatal: the user SID alone still answers the ordinary
			// non-elevated case, which is every normal run.
			fmt.Fprintf(os.Stderr, "cab-bridge: cannot read the token default owner (%v); ownership checks use the user SID only\n", derr)
		} else if !def.Equals(user.User.Sid) {
			sids = append(sids, def)
		}
		ownerSIDs = sids
	})
	return ownerSIDs, ownerSIDsErr
}

// tokenOwner mirrors TOKEN_OWNER. x/sys/windows has no typed accessor for
// TokenOwner (it has one for TokenUser), so the raw call is unavoidable.
type tokenOwner struct{ Owner *windows.SID }

// tokenDefaultOwner returns the SID Windows stamps as owner on objects this
// process creates. The returned SID is copied onto the Go heap: the one inside
// the buffer dies with it.
func tokenDefaultOwner(tok windows.Token) (*windows.SID, error) {
	var n uint32
	err := windows.GetTokenInformation(tok, windows.TokenOwner, nil, 0, &n)
	if err != nil && err != windows.ERROR_INSUFFICIENT_BUFFER {
		return nil, fmt.Errorf("GetTokenInformation(TokenOwner) sizing: %w", err)
	}
	if n == 0 {
		return nil, fmt.Errorf("GetTokenInformation(TokenOwner): reported a zero-length buffer")
	}
	buf := make([]byte, n)
	if err := windows.GetTokenInformation(tok, windows.TokenOwner, &buf[0], n, &n); err != nil {
		return nil, fmt.Errorf("GetTokenInformation(TokenOwner): %w", err)
	}
	owner := (*tokenOwner)(unsafe.Pointer(&buf[0])).Owner
	if owner == nil {
		return nil, fmt.Errorf("GetTokenInformation(TokenOwner): returned a nil owner")
	}
	return owner.Copy()
}

var noSecurityInfoOnce sync.Once

func warnNoSecurityInfo(path string) {
	noSecurityInfoOnce.Do(func() {
		fmt.Fprintf(os.Stderr, "cab-bridge: %q carries no security descriptor (a filesystem without ACLs?): ownership cannot be verified here, continuing\n", path)
	})
}

// fileAttributeTagInfo mirrors FILE_ATTRIBUTE_TAG_INFO. x/sys/windows carries
// the info class constant but no struct for it — the same gap tokenOwner above
// fills for TOKEN_OWNER, filled the same way.
type fileAttributeTagInfo struct {
	FileAttributes uint32
	ReparseTag     uint32
}

// isLinkByHandle asks the HANDLE, not the path, whether it refers to a symlink
// or a junction.
//
// The TAG is what decides, and the attribute alone would not: plenty of things
// that are not links carry FILE_ATTRIBUTE_REPARSE_POINT, and a OneDrive cloud
// placeholder is one of them — under %USERPROFILE%, which is exactly where the
// data dir lives. Refusing on the attribute would turn "this file is synced"
// into "this file is a symlink", on the machine of anybody whose profile is
// backed by OneDrive.
//
// These two tags are the ones os.Lstat itself reports as ModeSymlink, so this
// check and the Lstat above cannot disagree about what a link is.
func isLinkByHandle(h windows.Handle) (bool, error) {
	var info fileAttributeTagInfo
	if err := windows.GetFileInformationByHandleEx(
		h,
		windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		return false, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		return false, nil
	}
	return info.ReparseTag == windows.IO_REPARSE_TAG_SYMLINK ||
		info.ReparseTag == windows.IO_REPARSE_TAG_MOUNT_POINT, nil
}

// openNoFollow opens path for reading, refuses a symlink, and — this is F-133 —
// opens it in a way that does not freeze the file in place while we hold it.
//
// WHY THE SHARE MODE IS THE FIX. `os.Open` goes through Go's syscall layer,
// which asks for FILE_SHARE_READ|FILE_SHARE_WRITE and NOT FILE_SHARE_DELETE
// ($GOROOT/src/syscall/syscall_windows.go). On Windows a rename onto a target
// is a delete-class operation on that target, so while any bridge reader held a
// file open, every `os.Rename` onto it failed with "Access is denied" — the
// atomic write primitive this project is built on, defeated by its own reader.
// Measured, not reasoned: a second `next` died in `adopt: savemanifest` roughly
// one run in seven, and a reader opened outside the repository reproduced it on
// demand — open, denied; closed, fine. It was never the antivirus.
//
// What share-delete buys, and what it does not: the rename succeeds, and the
// handle we are holding keeps pointing at the OLD file object. We read the old
// contents, whole and consistent — never a mixture — and a fresh open sees the
// new ones. That is the semantics the atomic-write pattern always assumed and
// Windows was silently refusing to provide.
//
// It covers OUR readers. A handle held by somebody else — an antivirus, an
// editor, `type` — still blocks the rename, and that is the other half of F-133.
//
// AND IT IS STILL A TOCTOU, though a narrower one than before. Windows has no
// O_NOFOLLOW: the Lstat and the open are two operations, so a link planted
// between them is opened. What is new is that we no longer take the Lstat's word
// for it afterwards — the handle is interrogated too, so a swap in that window
// is refused instead of read. The Unix version refuses inside the open syscall
// and has no window at all; that difference is real and stays declared.
//
// FILE_FLAG_BACKUP_SEMANTICS is what os.Open passes as well, and it is here for
// the same reason: without it CreateFile refuses a directory outright, and
// openOwned's IsRegular() check — which answers "not a regular file" rather than
// "access denied" — would never get to run.
func openNoFollow(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: path=%q is a symlink", ErrOwnershipMismatch, path)
	}

	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	// The error shape is os.Open's on purpose: callers all over the tree ask
	// errors.Is(err, fs.ErrNotExist) / os.IsNotExist about what comes back from
	// ReadOwnedFile, and a bare Errno would answer a different question.
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	isLink, terr := isLinkByHandle(h)
	if terr != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("GetFileInformationByHandleEx(FileAttributeTagInfo) %q: %w", path, terr)
	}
	if isLink {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("%w: path=%q is a symlink", ErrOwnershipMismatch, path)
	}
	return os.NewFile(uintptr(h), path), nil
}

// enforceMode is a NO-OP on Windows, and this is not laziness.
//
// Perm() reports 0777 (or 0555 for read-only) for every file, so an exact
// comparison against 0700 never matches and a chmod would run on every single
// call — one that only ever toggles the read-only bit, which is not the
// permission anyone is asking about. Enforcing a Unix mode here would be a write
// that means nothing.
//
// What actually protects the data dir on this platform: it lives under
// %USERPROFILE%, whose ACLs grant the user (and SYSTEM/Administrators) and
// nobody else, and children inherit them. That is the substitute, and it is
// inherited rather than enforced by us — SECURITY.md has to say so.
func enforceMode(path string, current, want fs.FileMode) error {
	return nil
}

// DirPermsAreLoose always reports false here, for the reason in enforceMode:
// with Perm() pinned at 0777 the Unix test would fire on every command and print
// "loose perms, tightening" forever, about a mode that carries no meaning. See
// the boot check in cmd/cab-bridge/common.go for the caller.
func DirPermsAreLoose(perm fs.FileMode) bool {
	return false
}
