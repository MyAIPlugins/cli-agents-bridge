package session

import (
	"fmt"
	"os"
	"path/filepath"
)

// FindProjectRoot walks up from cwd to the project root and returns its
// absolute, cleaned path — the value stored as a session's scope (F-17). Two
// sessions whose cwds resolve to the same root (a VAL at the repo root and an
// ESC in a subfolder) share a scope and so see each other in the default
// `peers` view with zero configuration.
//
// Marker: a `.git` entry — a directory for a normal clone, a FILE for a linked
// git worktree (so multi-ESC worktrees resolve to their own root). The walk
// returns the first ancestor carrying the marker. With none, cwd is its own
// scope: a marker-less project stays isolated to itself instead of collapsing
// onto a shared parent.
//
// home is injected (never read from the environment here) so the helper stays
// pure and testable with temp dirs. It guards the one isolation-breaking case:
// $HOME must never count as a project root even when it holds a `.git` — a
// dotfiles repository in $HOME is common, and without this guard every
// marker-less project under $HOME would collapse onto $HOME and share a single
// scope. The same `dir != home` exclusion will gate any future secondary marker
// (e.g. a project-level `.claude`), kept here so adding it stays purely additive
// (VAL ratification H1). An empty home disables the exclusion (no $HOME known).
//
// Resolution is lexical (filepath.Abs + Clean), matching LongestPrefixLookup and
// isPathDescendantOrEqual; symlinks are not resolved so scope and the cwd lookup
// agree on one canonical form.
//
// Returns an error only if filepath.Abs fails on cwd (effectively never). The
// caller treats that as "no scope" and proceeds — the feature must never block a
// register or a peers listing.
func FindProjectRoot(cwd, home string) (string, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("findprojectroot: resolve cwd %q: %w", cwd, err)
	}
	abs = filepath.Clean(abs)
	cleanHome := ""
	if home != "" {
		cleanHome = filepath.Clean(home)
	}

	for dir := abs; ; {
		if dir != cleanHome && hasGitMarker(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached the filesystem root
		}
		dir = parent
	}
	// No marker on any ancestor (or the only marker was $HOME's dotfiles repo):
	// cwd is its own scope.
	return abs, nil
}

// hasGitMarker reports whether dir holds a `.git` entry, as a directory (a
// normal clone) or a FILE (a linked git worktree's gitdir pointer). os.Lstat,
// not an IsDir check, so the worktree file counts and a symlink is not followed.
func hasGitMarker(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}
