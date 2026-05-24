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
