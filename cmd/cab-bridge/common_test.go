package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SC-7 boot check (FINDING-1). Owner-mismatch (Stat_t.Uid != Getuid) is not
// unit-testable without a second UID, so it is exercised only by the runtime
// FATAL path; the cases below cover everything reproducible in a temp dir.
//
// NOTE: package main's init() sets Umask(0o077), so to simulate a loose-perms
// directory we must Chmod explicitly after creation (MkdirAll would be masked
// back to 0700).

func TestBootstrapDataDir_FirstRunCreates0700(t *testing.T) {
	base := filepath.Join(t.TempDir(), "newbase")
	if err := bootstrapDataDir(base); err != nil {
		t.Fatalf("first run should create the dir, got: %v", err)
	}
	info, err := os.Lstat(base)
	if err != nil {
		t.Fatalf("data dir not created on first run: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected a directory at %q", base)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("first-run dir perms = %04o, want 0700", perm)
	}
}

func TestBootstrapDataDir_SymlinkIsFatal(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "real")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapDataDir(link); err == nil {
		t.Fatal("expected FATAL error for a symlinked base dir, got nil")
	}
}

func TestBootstrapDataDir_NotADirIsFatal(t *testing.T) {
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapDataDir(file); err == nil {
		t.Fatal("expected FATAL error for a non-directory base, got nil")
	}
}

func TestBootstrapDataDir_LoosePermsAutoTightened(t *testing.T) {
	base := filepath.Join(t.TempDir(), "loose")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapDataDir(base); err != nil {
		t.Fatalf("loose perms should auto-repair, got: %v", err)
	}
	info, err := os.Lstat(base)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("perms after auto-repair = %04o, want 0700", perm)
	}
}

func TestBootstrapDataDir_HappyPath700(t *testing.T) {
	base := filepath.Join(t.TempDir(), "good")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapDataDir(base); err != nil {
		t.Fatalf("happy path (0700, owner-self) should pass, got: %v", err)
	}
}

// F-41 symlink edge: resolveScope must return the SAME canonical scope whether a
// repo is reached via its real path or via a symlink to it, so collectPeers'
// string compare pairs sessions under a symlinked path (e.g. macOS /tmp ->
// /private/tmp).
func TestResolveScope_SymlinkedPath_CanonicalAndStable(t *testing.T) {
	real := t.TempDir()
	repo := filepath.Join(real, "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o700))
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(real, link))

	viaReal := resolveScope(repo)
	viaLink := resolveScope(filepath.Join(link, "repo"))
	assert.Equal(t, viaReal, viaLink, "the real path and a symlink to it must yield one scope")

	resolved, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)
	assert.Equal(t, resolved, viaReal, "scope is the fully symlink-resolved form")
}

// F-41 symlink edge (reproduces the VAL smoke finding): a main repo (.git DIR,
// resolved lexically) and a linked worktree (.git FILE whose gitdir git writes
// symlink-RESOLVED) under a symlinked base must resolve to the SAME scope.
// Without the EvalSymlinks canonicalization in resolveScope this test is RED
// (the DIR branch stays lexical while the FILE branch is already resolved).
func TestResolveScope_WorktreeUnderSymlink_MatchesMainRepo(t *testing.T) {
	realBase := t.TempDir()
	mainRepo := filepath.Join(realBase, "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(mainRepo, ".git", "worktrees", "wt"), 0o700))
	wt := filepath.Join(realBase, "repo-wt")
	require.NoError(t, os.MkdirAll(wt, 0o700))
	// git writes the gitdir pointer symlink-resolved:
	resolvedMain, err := filepath.EvalSymlinks(mainRepo)
	require.NoError(t, err)
	gitdir := filepath.Join(resolvedMain, ".git", "worktrees", "wt")
	require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o600))

	// reach both through a symlink to the base (the /tmp -> /private/tmp case)
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(realBase, link))

	mainScope := resolveScope(filepath.Join(link, "repo"))
	wtScope := resolveScope(filepath.Join(link, "repo-wt"))
	assert.Equal(t, mainScope, wtScope, "main repo and its worktree must share one scope even under a symlink")
}
