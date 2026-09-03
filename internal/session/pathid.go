package session

import "path/filepath"

// Path identity: what makes two path strings the same place. The OS-specific
// half lives in pathsem_{unix,windows}.go; everything shared is here.
//
// TWO AXES, and keeping them apart is the whole design:
//
//	SCOPE        canonicalised — FindProjectRoot then CanonicalizePath, so one
//	             directory has ONE spelling no matter which symlink you walked in
//	             through (resolveScope, EffectiveScope).
//	PROJECTPATH  LEXICAL on purpose, symlinks NOT resolved. Three comments defend
//	             it: cmd/cab-bridge/common.go:219, cmd/cab-bridge/join.go:117 and
//	             manager.go's sibling comparison (constraint #6).
//
// The two never meet: a Scope is never compared to a ProjectPath. So the OS
// primitive below is shared, and the canonicalisation is not — feeding a
// ProjectPath through EvalSymlinks would quietly retire constraint #6 as a side
// effect of a Windows fix, which is a decision nobody has taken.
//
// What IS shared, and what F-135 turned out to need, is the comparison itself:
// on Windows `C:\Repo` and `c:\repo` are one directory, and byte equality
// answers "were these typed the same way" instead of "are these the same
// place".

// cleanAbs makes a path absolute and Clean, the form both axes compare in. A
// path that cannot be made absolute is still Cleaned rather than dropped: the
// comparison then simply fails to match, which is what it did before.
func cleanAbs(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(p)
}

// SamePathLexical reports whether two paths name the same place, WITHOUT
// resolving symlinks — the lexical axis.
//
// The empty string is not a path and is never resolved against the current
// directory: an empty side matches only another empty side, so `""` keeps
// meaning "no value" at every call site instead of silently becoming the cwd.
func SamePathLexical(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	return pathsEqualOS(cleanAbs(a), cleanAbs(b))
}

// IsDescendantLexical reports whether child is parent or lives under it. The
// trailing separator is what keeps /foo/barbaz from matching /foo/bar.
func IsDescendantLexical(child, parent string) bool {
	if child == "" || parent == "" {
		return false
	}
	c, p := cleanAbs(child), cleanAbs(parent)
	if pathsEqualOS(c, p) {
		return true
	}
	return pathHasPrefixOS(c, p+string(filepath.Separator))
}

// SamePathComponent compares ONE path component — a basename — with this
// host's file-name rules.
//
// Separate from SamePathLexical because a component must NOT be made
// absolute: "project" is a name, not a relative path waiting to be resolved
// against the current directory.
//
// It exists because the short address form is a basename, and leaving that
// one comparison byte-for-byte on Windows meant `VAL-x@PROJECT` could not
// reach a session in `Project` — the same defect as F-135, on the branch
// nobody had converted (CRI diff-gate).
func SamePathComponent(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	return pathsEqualOS(a, b)
}

// CanonicalizePath is the symlink canonicalisation the SCOPE axis is built on,
// in one place instead of two.
//
// It was written twice — resolveScope and EffectiveScope each ended with the
// same three lines — and F-135 needed a third caller: a scope hint typed by a
// human has to arrive in the same form as the scope it will be compared to, or
// the comparison is between a resolved path and an unresolved one.
//
// On failure the Abs+Clean form is kept rather than dropped, which is what both
// original sites did: a path that cannot be resolved (it no longer exists, a
// permission denies the walk) is still the best answer available.
func CanonicalizePath(p string) string {
	if p == "" {
		return ""
	}
	abs := cleanAbs(p)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}
