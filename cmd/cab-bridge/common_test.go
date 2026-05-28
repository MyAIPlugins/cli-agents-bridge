package main

import (
	"os"
	"path/filepath"
	"testing"
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
