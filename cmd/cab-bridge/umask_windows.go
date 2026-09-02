//go:build windows

package main

// SC-1 has no counterpart here, and the absence is deliberate rather than
// pending.
//
// Windows has no process umask: there is no inherited mask to subtract
// permissions with. New objects get their ACL from the parent directory's
// inheritable entries, so what protects the data dir is that it lives under
// %USERPROFILE% — whose default ACL grants the user, SYSTEM and Administrators
// and nobody else — and that children inherit it.
//
// Two consequences worth stating instead of discovering:
//
//   - The protection is INHERITED, not enforced by us. Create the data dir
//     somewhere with a permissive ACL (the root of a data drive, a share) and
//     nothing here tightens it. On Unix the umask would still have applied.
//   - os.Chmod on this platform only toggles the read-only bit. Anywhere the
//     code appears to enforce 0o600/0o700, it does not — see enforceMode in
//     internal/security/perms_windows.go, which says so where it happens.
//
// This file has no init() on purpose: an empty one would suggest something runs.
