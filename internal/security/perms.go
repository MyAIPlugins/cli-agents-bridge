// Package security implements the cli-agents-bridge security baseline (PLAN §9).
//
// This package provides the P0 primitives required by every file-touching
// operation in the codebase:
//
//   - SC-3 CheckOwnership: verify a path is owned by the current UID before
//     reading or writing it. Protects against threat TM-1 (other-UID malware)
//     and TM-6 (cross-session manifest spoofing).
//   - SC-4 ValidateSessionID: regex-validate any session ID before using it as
//     a path component. Protects against threat TM-2 (path traversal via
//     injected session ID values).
//   - EnforceDirPerms: idempotent chmod to enforce required directory mode.
//     Backs SC-2 (mkdir 700) when a directory pre-exists with wrong perms.
//
// SC-1 (umask 077) lives in cmd/cab-bridge/main.go init(), not here.
//
// Platform: Unix-only (darwin, linux). Windows is out of scope.
package security

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"syscall"
)

// sessionIDPattern is the strict regex applied to every session ID before it
// is used to build a filesystem path. Matches lowercase alphanumeric, 6-32
// chars. Covers both the upstream random 6-char IDs and future friendly names
// (e.g. "cli-agents-bridge-val-1" = 22 chars).
//
// Compiled once at package init via MustCompile — a malformed regex here is a
// build-time bug, not a runtime concern.
var sessionIDPattern = regexp.MustCompile(`^[a-z0-9]{6,32}$`)

// ErrInvalidSessionID is returned by ValidateSessionID when the input does
// not match sessionIDPattern. Callers should treat this as a hard failure
// (exit 2, never retry) — a malformed session ID is either a programming bug
// or a path-traversal attempt.
var ErrInvalidSessionID = errors.New("invalid session ID: must match ^[a-z0-9]{6,32}$")

// ErrOwnershipMismatch is returned by CheckOwnership when a file's UID does
// not match the calling process UID. Treated as a hard security failure.
var ErrOwnershipMismatch = errors.New("ownership mismatch: file not owned by current user")

// teamIDPattern validates the F-5 team label. Unlike a session ID this is NOT
// used to build a filesystem path (it is only a manifest field), so the rule is
// hygienic rather than security-critical: 1-32 chars, must start with a
// lowercase alphanumeric, then lowercase alphanumeric / '-' / '_'. Lowercase
// only — consistency with sessionIDPattern beats the convenience of mixed case.
var teamIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ErrInvalidTeamID is returned by ValidateTeamID when a non-empty team label
// does not match teamIDPattern. A hard failure (exit 1) — the user passed a
// malformed --team value.
var ErrInvalidTeamID = errors.New("invalid team ID: must match ^[a-z0-9][a-z0-9_-]{0,31}$")

// ValidateSessionID applies sessionIDPattern to id and returns
// ErrInvalidSessionID if it does not match. Apply this BEFORE using id in any
// filepath.Join or os.Open call — see threat TM-2.
//
// Empty string fails (does not match {6,32}). Whitespace fails. Path
// separators fail. Mixed case fails. Unicode fails.
func ValidateSessionID(id string) error {
	if !sessionIDPattern.MatchString(id) {
		return fmt.Errorf("%w: got %q", ErrInvalidSessionID, id)
	}
	return nil
}

// ValidateTeamID applies teamIDPattern to a NON-EMPTY team label and returns
// ErrInvalidTeamID if it does not match. The caller must only invoke this when
// --team was actually provided: an empty team is valid ("no team") and must NOT
// reach here. Whitespace, uppercase, path separators and a leading '-'/'_' all
// fail.
func ValidateTeamID(id string) error {
	if !teamIDPattern.MatchString(id) {
		return fmt.Errorf("%w: got %q", ErrInvalidTeamID, id)
	}
	return nil
}

// CheckOwnership verifies that path is owned by the current process UID. Use
// this BEFORE reading or writing any file in a session directory that could
// have been created by another process.
//
// Edge cases:
//   - Running as root (Getuid()==0): the check is skipped because root can
//     read everything anyway. A warning is logged to stderr. Not an error.
//   - NFS mounts: stat() over NFS can return synthetic UIDs that do not match
//     local Getuid(). Documented limitation, no MVP fix.
//   - Non-Unix filesystem (Windows, Plan9): returns an error. The plugin is
//     Unix-only.
func CheckOwnership(path string) error {
	if os.Getuid() == 0 {
		fmt.Fprintf(os.Stderr, "cab-bridge: running as root, ownership check skipped for %q\n", path)
		return nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %q: %w", path, err)
	}

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

// EnforceDirPerms ensures path is a directory with exactly mode. If path does
// not exist, it returns the underlying os error (caller decides whether to
// MkdirAll first). If path exists with different perms, EnforceDirPerms
// chmod-s it to mode. Idempotent.
//
// Typical use:
//
//	if err := os.MkdirAll(sessionDir, 0o700); err != nil { ... }
//	if err := security.EnforceDirPerms(sessionDir, 0o700); err != nil { ... }
//
// The double call covers the case where sessionDir pre-existed with wrong
// perms (e.g. user manually chmodded it to 755).
func EnforceDirPerms(path string, mode fs.FileMode) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %q: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory: %q", path)
	}
	currentMode := info.Mode().Perm()
	if currentMode == mode.Perm() {
		return nil
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("chmod %q to %o: %w", path, mode, err)
	}
	return nil
}

// ReadOwnedFile opens path, verifies that the OPEN DESCRIPTOR belongs to the
// current uid, and reads the contents from that same descriptor. It returns
// ErrOwnershipMismatch (wrapped) when the file belongs to somebody else.
//
// The fstat is on the descriptor, not on the path, and that is the whole point:
// checking a path and then opening it checks one file and reads another. One
// open, one fstat of that fd, one read of that fd — nothing can be swapped
// underneath in between.
//
// WHAT THIS IS FOR, precisely — because the generic threat it was filed under
// (TM-6, spoofing) is already impossible: with the data dir at 0700 owned by us
// (SC-7) and its subdirectories at 0700 (SC-1 umask + SC-2), another user cannot
// create a file in there at all, since writing into a directory needs permission
// on the directory.
//
// What remains is a window this code prints a message about:
//
//	cab-bridge: data dir "..." has loose perms 0755, tightening to 0700
//
// The base dir CAN have been created world-writable — by a script, a different
// umask, a copy — and in that window another local user could drop a file in.
// SC-7 shuts the door and carries on; it does not walk out whoever already came
// in. This is that cleanup. The payload is worth naming: a planted manifest
// carries an agentName (which intercepts everything addressed to that name,
// since recipients resolve by name within a scope) and a projectPath (which
// takes part in LongestPrefixLookup, i.e. in deciding who YOU are).
//
// Skipped for root, like every other ownership check here: root can read
// anything regardless, so refusing would cost without protecting.
func openOwned(path string) (*os.File, error) {
	// O_NOFOLLOW, and it is not decoration. `os.Open` FOLLOWS symlinks, so the
	// fstat that follows would report the owner of the TARGET: another uid who
	// planted a symlink of their own — during the same loose-perms window this
	// check exists to clean up after — points it at a file of ours, and the
	// ownership test passes on the victim's own uid. The content check was sound
	// and the identity of the path was not: we closed "is this still the same
	// file?" and left open "is this the file it claims to be?".
	//
	// Refusing at OPEN rather than with an Lstat beforehand is what makes it
	// atomic: an Lstat-then-open pair is the very TOCTOU shape being avoided.
	// O_NONBLOCK alongside it, and this one was learned from a failing test: on a
	// FIFO the OPEN itself blocks until a writer shows up, so the "regular file"
	// check below never gets to run. The refusal has to be reachable, not just
	// written.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if isSymlinkRefusal(err) {
			return nil, fmt.Errorf("%w: path=%q is a symlink", ErrOwnershipMismatch, path)
		}
		return nil, err
	}
	info, serr := f.Stat()
	if serr != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat %q: %w", path, serr)
	}
	// Regular files only: a link to a FIFO or a device would otherwise sail
	// through whenever the target belongs to us — and reading one blocks or
	// yields something that is not a message.
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("%w: path=%q is not a regular file (mode %s)", ErrOwnershipMismatch, path, info.Mode())
	}

	if os.Getuid() != 0 {
		sys, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			_ = f.Close()
			return nil, fmt.Errorf("ownership check unsupported on this platform (path %q)", path)
		}
		if int(sys.Uid) != os.Getuid() {
			_ = f.Close()
			return nil, fmt.Errorf("%w: path=%q file_uid=%d current_uid=%d",
				ErrOwnershipMismatch, path, sys.Uid, os.Getuid())
		}
	}

	return f, nil
}

// ReadOwnedFile opens path with the ownership and shape checks above, and reads
// it. See openOwned for what is verified and why.
func ReadOwnedFile(path string) ([]byte, error) {
	f, err := openOwned(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// CheckOwnedFile runs the same verification WITHOUT reading the contents, for
// callers that derive meaning from a directory entry alone.
//
// One exists because one was needed: `sent` builds its index from archived
// FILENAMES — deliberately, to avoid decoding every archived file — so a planted
// entry named <timestamp>-msg-<id>.json used to forge the "archived" state of a
// message, telling the sender their peer had replied when nobody had. No file
// was ever read there, so no amount of auditing READS would have found it: the
// consumed datum was the name.
func CheckOwnedFile(path string) error {
	f, err := openOwned(path)
	if err != nil {
		return err
	}
	return f.Close()
}

// WarnNotOurs reports whether err is an ownership violation, announcing it on
// stderr when it is.
//
// The policy lives HERE, in one place, because the CRI gate's closing line is
// the risk: "centralise the policy or the next caller will lose it again". It
// had already been lost once — every scan in the codebase has the shape "read
// it, skip on error", so the typed error was landing in the same bucket as a
// truncated JSON and being skipped in silence, or reported as "unreadable file,
// no action needed from you".
//
// Enumerations SKIP rather than abort — a command that dies on the anomaly it
// should be displaying is useless exactly when it is needed. What they must not
// do is stay quiet.
func WarnNotOurs(subject string, err error) bool {
	if !errors.Is(err, ErrOwnershipMismatch) {
		return false
	}
	fmt.Fprintf(os.Stderr, "cab-bridge: ignoring %s — it does not belong to this user: %v\n", subject, err)
	return true
}

// isSymlinkRefusal reports whether err is the kernel refusing O_NOFOLLOW on a
// symlink. Linux answers ELOOP, macOS/BSD answer EMLINK for this specific case;
// both are reported rather than guessed at.
func isSymlinkRefusal(err error) bool {
	return errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK)
}

// WhatItCovers, for SECURITY.md and for whoever reads this in six months:
//
//   - the LEAF is checked atomically — not a symlink, a regular file, ours.
//   - the DIRECTORIES above it are not re-validated on every read. Doing that
//     properly means walking the path with openat(O_NOFOLLOW) per component, on
//     every single read, and the residual it buys is narrow: SC-7 keeps the base
//     dir 0700 and owned by us, so from the first run onwards nobody else can
//     alter the tree underneath. What stays uncovered is an intermediate
//     directory replaced by a symlink DURING the same loose-perms window — and
//     that is stated in SECURITY.md rather than papered over, because a control
//     described as broader than it is teaches the reader to stop looking.
