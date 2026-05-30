package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mkGitDir creates dir plus a `.git` subdirectory (normal-clone marker).
func mkGitDir(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o700))
}

// mkGitFile creates dir plus a `.git` FILE (linked git-worktree marker).
func mkGitFile(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /elsewhere\n"), 0o600))
}

func TestFindProjectRoot_GitDirMarker_FromNestedCwd(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "repo")
	mkGitDir(t, root)
	nested := filepath.Join(root, "a", "b", "c")
	require.NoError(t, os.MkdirAll(nested, 0o700))

	got, err := FindProjectRoot(nested, home)
	require.NoError(t, err)
	assert.Equal(t, root, got)
}

func TestFindProjectRoot_GitFileMarker_Worktree(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "wt")
	mkGitFile(t, root)
	nested := filepath.Join(root, "sub")
	require.NoError(t, os.MkdirAll(nested, 0o700))

	got, err := FindProjectRoot(nested, home)
	require.NoError(t, err)
	assert.Equal(t, root, got, "a .git FILE (worktree) must count as a marker")
}

func TestFindProjectRoot_CwdIsRoot_ExactMatch(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "repo")
	mkGitDir(t, root)

	got, err := FindProjectRoot(root, home)
	require.NoError(t, err)
	assert.Equal(t, root, got)
}

func TestFindProjectRoot_NoMarker_FallsBackToCwd(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cwd := filepath.Join(t.TempDir(), "x", "y")
	require.NoError(t, os.MkdirAll(cwd, 0o700))

	got, err := FindProjectRoot(cwd, home)
	require.NoError(t, err)
	assert.Equal(t, cwd, got, "no marker on any ancestor -> cwd is its own scope")
}

// CRUCIAL (F-17): a marker-less project under $HOME must NOT collapse onto $HOME
// even when $HOME itself holds a .git (a dotfiles repo). Without the exclusion
// every marker-less project under home would share one scope = isolation broken.
func TestFindProjectRoot_HomeDotfilesGit_Excluded(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	mkGitDir(t, home) // simulate a dotfiles repo living at $HOME
	proj := filepath.Join(home, "scratch")
	require.NoError(t, os.MkdirAll(proj, 0o700))

	got, err := FindProjectRoot(proj, home)
	require.NoError(t, err)
	assert.Equal(t, proj, got, "$HOME's .git must be excluded; the marker-less project stays its own scope")
	assert.NotEqual(t, home, got, "must never return $HOME as scope")
}

// CRUCIAL (F-17): a global ~/.claude in $HOME is never a marker (only .git is),
// so a marker-less project under such a $HOME resolves to its own cwd.
func TestFindProjectRoot_HomeGlobalClaude_NotAMarker(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700)) // global ~/.claude
	proj := filepath.Join(home, "myproj")
	require.NoError(t, os.MkdirAll(proj, 0o700))

	got, err := FindProjectRoot(proj, home)
	require.NoError(t, err)
	assert.Equal(t, proj, got, "global ~/.claude is not a project marker; cwd stays its own scope")
}

// CRUCIAL (F-17): VAL at the repo root and ESC in a subfolder must resolve to
// the SAME scope so they see each other in the default peers view.
func TestFindProjectRoot_ValRootAndEscSubfolder_SameScope(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "cli-agents-bridge")
	mkGitDir(t, root)
	docs := filepath.Join(root, "docs")
	require.NoError(t, os.MkdirAll(docs, 0o700))

	valScope, err := FindProjectRoot(root, home)
	require.NoError(t, err)
	escScope, err := FindProjectRoot(docs, home)
	require.NoError(t, err)
	assert.Equal(t, valScope, escScope, "VAL root and ESC subfolder must share one scope")
	assert.Equal(t, root, escScope)
}

// CRUCIAL (F-17): two different project roots must produce different scopes.
func TestFindProjectRoot_TwoDifferentRoots_DifferentScopes(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	base := t.TempDir()
	rootA := filepath.Join(base, "projA")
	rootB := filepath.Join(base, "projB")
	mkGitDir(t, rootA)
	mkGitDir(t, rootB)
	subA := filepath.Join(rootA, "sub")
	subB := filepath.Join(rootB, "sub")
	require.NoError(t, os.MkdirAll(subA, 0o700))
	require.NoError(t, os.MkdirAll(subB, 0o700))

	scopeA, err := FindProjectRoot(subA, home)
	require.NoError(t, err)
	scopeB, err := FindProjectRoot(subB, home)
	require.NoError(t, err)
	assert.NotEqual(t, scopeA, scopeB)
	assert.Equal(t, rootA, scopeA)
	assert.Equal(t, rootB, scopeB)
}

func TestFindProjectRoot_EmptyHome_NoExclusion(t *testing.T) {
	t.Parallel()
	// No home known -> the exclusion is disabled, but a normal repo still
	// resolves to its root (the exclusion only ever suppresses a $HOME match).
	root := filepath.Join(t.TempDir(), "repo")
	mkGitDir(t, root)
	nested := filepath.Join(root, "x")
	require.NoError(t, os.MkdirAll(nested, 0o700))

	got, err := FindProjectRoot(nested, "")
	require.NoError(t, err)
	assert.Equal(t, root, got)
}

// H6 degenerate: registering from $HOME itself (which holds a dotfiles .git).
// $HOME is excluded as a marker, the walk finds nothing, and the fallback
// returns the cwd — which equals home. scope == home is the accepted result.
func TestFindProjectRoot_CwdEqualsHome_Degenerate(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	mkGitDir(t, home)

	got, err := FindProjectRoot(home, home)
	require.NoError(t, err)
	assert.Equal(t, home, got)
}
