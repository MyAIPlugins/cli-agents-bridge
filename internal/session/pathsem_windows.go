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
// Windows file names do not distinguish case — with the limit stated at the
// bottom of this comment: `C:\Repo` and `c:\repo` are ONE directory, and the
// two spellings reach us from different places on purpose —
// the stored scope carries the casing EvalSymlinks read off the disk, while a
// hint carries the casing a human typed. Comparing them byte-for-byte answers
// "were these typed the same way", which is not the question anyone is asking.
//
// EqualFold, not ToLower on both: it needs no allocation and no locale, and the
// paths we compare are already Clean, so no separator normalisation is left.
//
// THE LIMIT, stated rather than discovered later: this is Unicode simple case
// folding, NOT the case-mapping table Win32 and NTFS actually use, and NTFS
// directories can be created case-SENSITIVE per-directory. For the paths this
// compares — user profiles and repository checkouts, in practice ASCII — the
// two agree exactly. Outside that it is a declared approximation, and it errs
// toward calling two paths the same. Saying "Windows is case-insensitive"
// without this qualification would be claiming the platform's rule while
// implementing Go's.
func pathsEqualOS(a, b string) bool { return strings.EqualFold(a, b) }

// pathHasPrefixOS is the same comparison applied to a prefix.
func pathHasPrefixOS(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// PathNeedsVolume reports whether a path CONTAINS A SEPARATOR BUT NAMES NO
// VOLUME. Two shapes qualify, and the name of the function is about the volume
// because that is what both are missing:
//
//	/foo, \foo   rooted, no drive — Clean makes it `\foo`, which resolves
//	             against whichever drive the process happens to be on
//	foo/bar      relative with a separator — resolves against the cwd
//
// (An earlier comment here said "rooted", which described only the first and
// was wrong about the second — CRI diff-gate.)
//
// Both are the same failure for an address: neither is a complete path, and
// neither is a basename — a basename never contains a separator. Left to fall
// through, either is compared against a basename, never matches, and produces
// "no agent named X in project /foo": the symptom, with the cause hidden.
//
// Git Bash usually converts such a word before the binary sees it. When it does
// not, saying what is missing is the answer.
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
