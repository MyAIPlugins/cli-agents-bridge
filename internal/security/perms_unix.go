//go:build !windows

package security

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// The Unix half of the ownership model: an owner is a uid, and comparing two of
// them is one integer comparison. Everything in this file exists because the
// Windows half (perms_windows.go) cannot express any of it that way — see there
// for what replaces each piece.

// ownerCheckPath verifies that the object described by info belongs to the
// current user. info must come from a Stat or Lstat OF path: on this platform
// the answer is read out of info, and path is only used to name the object in
// the error. On Windows it is the other way round, which is why both are passed.
//
// Caller contract: the root skip (Getuid()==0) happens BEFORE this is reached.
func ownerCheckPath(path string, info fs.FileInfo) error {
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("ownership check unsupported on this platform (path %q)", path)
	}
	if int(sys.Uid) != os.Getuid() {
		return fmt.Errorf("%w: path=%q file_uid=%d current_uid=%d",
			ErrOwnershipMismatch, path, sys.Uid, os.Getuid())
	}
	return nil
}

// ownerCheckFile is the fd-based counterpart: info must be f.Stat(), i.e. an
// fstat OF THAT DESCRIPTOR, which is what makes openOwned atomic. Here that
// falls out of the same Stat_t; on Windows the descriptor has to be interrogated
// separately from the path.
func ownerCheckFile(f *os.File, info fs.FileInfo) error {
	return ownerCheckPath(f.Name(), info)
}

// openNoFollow opens path for reading and refuses a symlink AT OPEN TIME.
//
// Refusing at open rather than with an Lstat beforehand is what makes it atomic:
// an Lstat-then-open pair is the very TOCTOU shape being avoided. O_NONBLOCK
// alongside it, and this one was learned from a failing test: on a FIFO the OPEN
// itself blocks until a writer shows up, so openOwned's "regular file" check
// never gets to run. The refusal has to be reachable, not just written.
func openNoFollow(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if isSymlinkRefusal(err) {
			return nil, fmt.Errorf("%w: path=%q is a symlink", ErrOwnershipMismatch, path)
		}
		return nil, err
	}
	return f, nil
}

// isSymlinkRefusal reports whether err is the kernel refusing O_NOFOLLOW on a
// symlink. Linux answers ELOOP, macOS/BSD answer EMLINK for this specific case;
// both are reported rather than guessed at.
func isSymlinkRefusal(err error) bool {
	return errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK)
}

// enforceMode chmods path to want when current differs. Exact match, not a
// mask: EnforceDirPerms promises "exactly mode", so 0o750 is as wrong as 0o755.
func enforceMode(path string, current, want fs.FileMode) error {
	if current == want.Perm() {
		return nil
	}
	if err := os.Chmod(path, want); err != nil {
		return fmt.Errorf("chmod %q to %o: %w", path, want, err)
	}
	return nil
}

// DirPermsAreLoose reports whether perm grants anything to group or other. Used
// by the boot check (SC-7) to decide whether to warn and tighten. Exported
// because that check lives in cmd/cab-bridge, and this is the one place the
// answer is allowed to be computed.
func DirPermsAreLoose(perm fs.FileMode) bool {
	return perm&0o077 != 0
}
