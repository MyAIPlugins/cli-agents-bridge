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

// openNoFollow opens path for reading and refuses a symlink.
//
// AND IT IS A TOCTOU, deliberately, because Windows has no O_NOFOLLOW: the check
// and the open are two operations, so a link planted between them is opened. The
// Unix version refuses inside the open syscall itself and that gap does not
// exist there — this is the one place where the two platforms do NOT give the
// same guarantee, and pretending otherwise is what a silent port would do.
//
// No O_NONBLOCK counterpart either: it exists on Unix because opening a FIFO
// blocks. Windows named pipes do not live in the filesystem namespace, so the
// hazard it guards against is not reachable by path here. openOwned's IsRegular()
// check still runs and still refuses anything that is not a plain file.
func openNoFollow(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: path=%q is a symlink", ErrOwnershipMismatch, path)
	}
	return os.Open(path)
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
