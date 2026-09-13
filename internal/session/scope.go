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
// scope. The exclusion is byte-equality FIRST (the ordinary case, no syscall)
// and, only when a marker is present under a different spelling, an identity
// check — see sameDirectoryAsHome, which also states the error policy and the
// branch that stays open. The same exclusion will gate any future secondary
// marker (e.g. a project-level `.claude`), kept here so adding it stays purely
// additive (VAL ratification H1). An empty home disables it (no $HOME known).
//
// Resolution in THIS walk is lexical (filepath.Abs + Clean), matching
// LongestPrefixLookup and IsDescendantLexical — including what this function
// RETURNS, which the identity check never rewrites: it decides whether a marker
// counts, never what the scope is spelled like. Symlinks are NOT resolved here;
// the callers symlink-canonicalize the returned scope so the `.git` DIR branch
// (lexical cwd) and the `.git` FILE branch (git writes the gitdir already
// symlink-resolved) converge on one form under a symlinked path (F-41). There
// are TWO of them, not one as this comment said until the sentence was checked:
// cmd/cab-bridge resolveScope, on the caller's own cwd, and EffectiveScope,
// which runs this walk over OTHER sessions' manifests when their stored scope is
// empty. Scope and the cwd lookup are independent axes — the lookup compares
// ProjectPath, never Scope — so the two need not share a form.
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
			// The identity check runs only when a marker is actually here. What
			// that saves, stated accurately because the first version of this
			// comment overstated it: ancestors WITHOUT a marker cost no I/O, and
			// neither does a home matched lexically. An ordinary repository does
			// find a marker, so it pays up to two Stat calls — one on the home,
			// one on the directory. See sameDirectoryAsHome for the scale of that
			// on manifests whose scope is not stored yet.
			if root, ok := gitMarkerRoot(dir); ok && !sameDirectoryAsHome(dir, cleanHome) {
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

// sameDirectoryAsHome reports whether dir IS the home directory reached under a
// different spelling — the case a byte comparison cannot see.
//
// It exists because the exclusion above was `dir != cleanHome` while the walk is
// lexical BY CONTRACT (canonicalisation belongs to the caller), so the guard was
// routinely handed a spelling of $HOME it could not recognise. Four reach it and
// none needs a case-insensitive volume: a symlinked home in either direction,
// /tmp against /private/tmp, Unicode NFC against NFD, and macOS firmlinks —
// /Users/x and /System/Volumes/Data/Users/x share a (dev,ino) while EvalSymlinks
// keeps them distinct, a firmlink not being a symlink. Under any of them two
// marker-less projects under $HOME collapsed onto one scope and could message
// each other: the isolation failed OPEN.
//
// os.SameFile is (dev,ino) on Unix and volume+file-id on Windows, with no cgo.
// os.Stat and not Lstat: a symlinked home must identify its TARGET, which is the
// directory the walk is standing in.
//
// ERROR POLICY, and it is a decision rather than a fallback. With no proof of
// identity we PRESERVE THE PREVIOUS BEHAVIOUR and accept the marker. Not "it
// cannot be stat'd, therefore it is not the home" — that is a negative proof we
// do not have. The alternative, treating an unverifiable home as a reason to
// fall back, reads prudent and regresses far more: with HOME=/nonexistent — a
// container, `sudo -H`, CI with a synthetic user — the stat fails on EVERY join
// and pairing breaks in every repository, including those that have nothing to
// do with $HOME. That would trade a defect with a rare precondition for a
// regression with a common one. It is also what Windows already does: there
// SameFile returns false when loadFileId fails, so the marker is accepted.
//
// COST, declared rather than optimised: an ordinary repository pays up to two
// Stat calls per walk. EffectiveScope skips the walk entirely when a manifest
// already stores its scope, but runs it for legacy ones — and that happens
// inside the loops of LookupByCWDDetails and collectPeers, so a scan over N
// legacy manifests that do carry a marker costs up to 2N Stat calls. No cache:
// for this lot the cost is proportionate, and if it ever needs optimising the
// legacy path gets measured rather than guessed at now.
//
// DECLARED LIMIT: this PROTECTS the cases where identity is verifiable, and
// errors preserve the previous behaviour — it does not close the defect. The
// branch that stays open is the one where the SPELLING handed in goes through a
// directory that cannot be traversed while the home directory itself remains
// reachable to the walk: the stat is denied, the policy above accepts the
// marker, and the scopes collapse exactly as before. A test documents that as a
// limit rather than asserting it as a guarantee.
func sameDirectoryAsHome(dir, cleanHome string) bool {
	if cleanHome == "" {
		return false // no home known: the exclusion is disabled entirely
	}
	homeInfo, err := os.Stat(cleanHome)
	if err != nil {
		return false
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		return false
	}
	return os.SameFile(homeInfo, dirInfo)
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
