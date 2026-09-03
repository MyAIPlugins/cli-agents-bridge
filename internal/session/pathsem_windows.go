//go:build windows

package session

import (
	"os"
	"path/filepath"
	"strings"
)

// The OS-specific half of path identity. Everything above it (pathid.go) is
// shared; only these three answers differ between hosts.

// pathsEqualOS compares two already-absolute, already-Clean paths.
//
// Windows file names are case-insensitive: `C:\Repo` and `c:\repo` are ONE
// directory, and the two spellings reach us from different places on purpose —
// the stored scope carries the casing EvalSymlinks read off the disk, while a
// hint carries the casing a human typed. Comparing them byte-for-byte answers
// "were these typed the same way", which is not the question anyone is asking.
//
// EqualFold, not ToLower on both: it needs no allocation and no locale, and the
// paths we compare are already Clean, so no separator normalisation is left to
// do here.
func pathsEqualOS(a, b string) bool { return strings.EqualFold(a, b) }

// pathHasPrefixOS is the same comparison applied to a prefix.
func pathHasPrefixOS(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// PathNeedsVolume reports whether a path is ROOTED BUT NAMES NO VOLUME — `/foo`
// or `\foo` written literally on Windows.
//
// It is the one shape that is neither of the two things an address can carry.
// It is not a complete path: filepath.IsAbs says false, and Clean turns it into
// `\foo`, which resolves against whatever drive the process happens to be on.
// And it is not a basename either, because a basename never contains a
// separator. Left to fall through it would be compared against a basename,
// never match, and produce "no agent named X in project /foo" — an error that
// describes the symptom and hides the cause.
//
// Git Bash usually converts such a word before the binary sees it. When it does
// not, refusing and naming the two forms that work is the answer.
func PathNeedsVolume(p string) bool {
	if p == "" || filepath.IsAbs(p) {
		return false
	}
	for i := 0; i < len(p); i++ {
		if os.IsPathSeparator(p[i]) {
			return true
		}
	}
	return false
}
