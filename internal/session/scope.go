package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FindProjectRoot walks up from cwd to the project root and returns its
// absolute, cleaned path — the value stored as a session's scope (F-17). Two
// sessions whose cwds resolve to the same root share a scope and so see each
// other in the default `peers` view with zero configuration.
//
// Scope is the git REPOSITORY, not the physical checkout (F-41, VAL+Alan
// ratified). A linked git worktree resolves to its git-common-root (the main
// repository), NOT to the worktree directory itself. So a VAL at the main repo
// and an ESC in a `git worktree add`-ed checkout of the SAME repo share one
// scope and pair with zero config — the v0.5 onboarding goal. Clones of
// DIFFERENT repos keep distinct common-roots and stay isolated (correct). This
// reverses the earlier "a worktree resolves to its own root" intent on purpose.
//
// Marker: a `.git` entry on an ancestor —
//   - a DIRECTORY (a normal clone or the main repo): the dir holding it IS the
//     git-common-root.
//   - a FILE (a linked worktree): a `gitdir: <path>` pointer. When <path> has
//     the canonical `<root>/.git/worktrees/<name>` shape, <root> is the
//     git-common-root. Any other pointer (a submodule's `.git/modules/...`, an
//     unexpected layout, an unreadable file) FALLS BACK to the worktree dir
//     itself — never fatal, scope must never block a register/peers. No git
//     process is ever executed (consistent with the rest of the codebase).
//
// The walk returns the first ancestor carrying a marker. With none, cwd is its
// own scope: a marker-less project stays isolated to itself instead of
// collapsing onto a shared parent.
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
		if dir != cleanHome {
			if root, ok := gitMarkerRoot(dir); ok {
				return root, nil
			}
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

// gitMarkerRoot reports the git-common-root anchored at dir when dir carries a
// `.git` marker, and ok=false when it has none (so the caller keeps walking up).
//   - `.git` DIRECTORY: dir IS the root (a normal clone or the main repo).
//   - `.git` FILE: a linked-worktree pointer resolved to the main repo's root
//     (F-41); a non-worktree or unresolvable pointer falls back to dir itself.
//
// os.Lstat (not following a symlink) keeps the dir-vs-file decision on the entry
// itself; a `.git` symlink is treated as the file branch and falls back to dir
// if it does not read as a worktree pointer.
func gitMarkerRoot(dir string) (string, bool) {
	info, err := os.Lstat(filepath.Join(dir, ".git"))
	if err != nil {
		return "", false // no marker here
	}
	if info.IsDir() {
		return dir, true
	}
	if root, ok := worktreeCommonRoot(dir); ok {
		return root, true
	}
	return dir, true // a .git FILE that is not a resolvable worktree pointer
}

// worktreeCommonRoot parses dir/.git as a linked-worktree gitdir pointer and
// returns the main repository root. A worktree's `.git` file holds a single line
// `gitdir: <path>` pointing at `<root>/.git/worktrees/<name>`; the main root is
// three parents up from that path (strip <name>, "worktrees", ".git"). A
// relative pointer is resolved against dir. Returns ok=false (caller falls back
// to dir) when the file is unreadable, has no gitdir line, or the pointer is not
// the canonical worktree shape — e.g. a submodule's `.git/modules/<name>`.
func worktreeCommonRoot(dir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, ".git"))
	if err != nil {
		return "", false
	}
	gitdir := ""
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:"); ok {
			gitdir = strings.TrimSpace(rest)
			break
		}
	}
	if gitdir == "" {
		return "", false
	}
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(dir, gitdir)
	}
	gitdir = filepath.Clean(gitdir)
	worktreesDir := filepath.Dir(gitdir)    // <root>/.git/worktrees
	commonDir := filepath.Dir(worktreesDir) // <root>/.git
	root := filepath.Dir(commonDir)         // <root>
	if filepath.Base(worktreesDir) != "worktrees" || filepath.Base(commonDir) != ".git" {
		return "", false // submodule (.git/modules/...) or an unexpected layout
	}
	return root, true
}
