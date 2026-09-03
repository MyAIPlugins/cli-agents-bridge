//go:build !windows

package session

import "strings"

// The OS-specific half of path identity. Everything above it (pathid.go) is
// shared; only these three answers differ between hosts.

// pathsEqualOS compares two already-absolute, already-Clean paths. On Unix a
// path is a byte string and two spellings that differ by a byte are two paths.
func pathsEqualOS(a, b string) bool { return a == b }

// pathHasPrefixOS is the same comparison applied to a prefix.
func pathHasPrefixOS(s, prefix string) bool { return strings.HasPrefix(s, prefix) }

// PathNeedsVolume reports whether a path is rooted but names no volume, which
// on Unix cannot happen: a leading "/" IS the root. Always false here, so the
// refusal it drives exists only where the ambiguity exists.
func PathNeedsVolume(string) bool { return false }
