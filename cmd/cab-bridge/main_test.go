package main

import (
	"fmt"
	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

// TestMain fences the WHOLE package away from the real data dir.
//
// The commands in this package resolve their config and their session from the
// environment and the cwd. A test that calls one of the run* entry points
// without setting CAB_DATA_DIR therefore operates on whatever session owns the
// current directory — and that is not hypothetical: one such test sent real
// replies to a real peer and closed its open asks, on every `go test ./...` run
// from a registered worktree. It passed the whole time, because it did exactly
// what it asserted; it just did it to production.
//
// Individual tests can still call t.Setenv to point somewhere of their own —
// this only supplies a safe DEFAULT, so the dangerous case stops depending on
// every future test author remembering. That is the difference between a rule
// and a fence: a rule protects the tests written by someone who knows it.
//
// Deliberately NOT a guard that refuses to run: refusing would make the failure
// mode "the suite does not start", which is loud but also easy to switch off
// under pressure. A temp dir makes the dangerous action impossible while the
// suite behaves normally.
func TestMain(m *testing.M) {
	if os.Getenv("CAB_DATA_DIR") == "" {
		dir, err := os.MkdirTemp("", "cab-bridge-testdata-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "test setup: cannot create a sandbox data dir: %v\n", err)
			os.Exit(1)
		}
		if err := os.Setenv("CAB_DATA_DIR", dir); err != nil {
			fmt.Fprintf(os.Stderr, "test setup: cannot set CAB_DATA_DIR: %v\n", err)
			os.Exit(1)
		}
		code := m.Run()
		// os.Exit skips defers, so the cleanup is explicit and before it.
		_ = os.RemoveAll(dir)
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// TestSuiteNeverTouchesTheRealDataDir is the regression the incident asks for:
// it fails if the fence above is removed or bypassed.
//
// It asserts the property that actually matters — the resolved data dir is not
// the user's — rather than trying to detect writes after the fact, which would
// be both fragile and too late.
func TestSuiteNeverTouchesTheRealDataDir(t *testing.T) {
	cfg, err := loadConfigOrFail()
	if err != nil {
		t.Fatalf("config must load inside the suite: %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir to compare against")
	}
	if pointsAtTheRealDataDir(cfg, home) {
		t.Fatalf("the suite is pointed at the REAL data dir (base %s, state %s): a test calling a run* entry point "+
			"would send live messages to real peers and close their asks", cfg.BaseDir, cfg.DataDir)
	}
}

// pointsAtTheRealDataDir says whether cfg resolves to the data of the user whose home
// is home: either the BASE they installed into (~/.claude/cli-agents-bridge) or the
// STATE dir the binary derives from it (<base>/layout-2). The guard compared only
// DataDir with the base, and since F-160 DataDir is the state dir: it could never be
// equal, and a suite pointed at the real base passed the check (CRI diff-gate, M2).
func pointsAtTheRealDataDir(cfg config.Config, home string) bool {
	realBase := filepath.Join(home, ".claude", "cli-agents-bridge")
	for _, real := range []string{realBase, filepath.Join(realBase, config.LayoutDir)} {
		if cfg.BaseDir == real || cfg.DataDir == real {
			return true
		}
	}
	return false
}

// The guard itself, on a home that is not the user's: HOME is a temporary directory,
// so the "real" base is a path inside it and nothing of the user's is at stake. It
// must say yes when CAB_DATA_DIR is that base, and yes when nothing is set and the
// default is that base; and no for a sandbox. This does not touch the fence in
// TestMain: it tests the predicate the fence's regression test relies on.
func TestPointsAtTheRealDataDir_RecognisesTheBaseAndTheStateDirOfAHome(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("USERPROFILE", fakeHome)
	fakeBase := filepath.Join(fakeHome, ".claude", "cli-agents-bridge")

	t.Run("CAB_DATA_DIR is the default base", func(t *testing.T) {
		t.Setenv("CAB_DATA_DIR", fakeBase)
		cfg, err := loadConfigOrFail()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(fakeBase, config.LayoutDir), cfg.DataDir, "premise: the state dir is below the base")
		assert.True(t, pointsAtTheRealDataDir(cfg, fakeHome))
	})
	t.Run("nothing set: the default is the real base", func(t *testing.T) {
		t.Setenv("CAB_DATA_DIR", "")
		cfg, err := loadConfigOrFail()
		require.NoError(t, err)
		assert.Equal(t, fakeBase, cfg.BaseDir, "premise: the default base is the one under HOME")
		assert.True(t, pointsAtTheRealDataDir(cfg, fakeHome))
	})
	t.Run("a sandbox is not the real data dir", func(t *testing.T) {
		t.Setenv("CAB_DATA_DIR", t.TempDir())
		cfg, err := loadConfigOrFail()
		require.NoError(t, err)
		assert.False(t, pointsAtTheRealDataDir(cfg, fakeHome))
	})
	t.Run("the old comparison would have missed it", func(t *testing.T) {
		t.Setenv("CAB_DATA_DIR", fakeBase)
		cfg, err := loadConfigOrFail()
		require.NoError(t, err)
		assert.NotEqual(t, fakeBase, cfg.DataDir, "DataDir is the state dir, so DataDir == base never held")
		assert.True(t, pointsAtTheRealDataDir(cfg, fakeHome), "and the guard still says yes")
	})
}

// The CRI diff-gate P0, as an automated test: a leaf check cannot see a
// redirected tree. An empty data dir whose `sessions` points at somebody else's
// passed EVERY control we had — SC-7 because the base really was ours and 0700,
// SC-3 because each file behind the link is a perfectly regular file of ours.
// The lie was in the path.
func TestBootstrapDataDir_RefusesARedirectedTree(t *testing.T) {
	for _, sub := range []string{"sessions", "archive"} {
		t.Run(sub+"_as_a_symlink_is_refused", func(t *testing.T) {
			base := t.TempDir()
			elsewhere := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(elsewhere, "victim"), 0o700))
			mustSymlink(t, elsewhere, filepath.Join(base, sub))

			err := bootstrapDataDir(base)
			require.Error(t, err, "a redirected %s must stop the command", sub)
			assert.ErrorIs(t, err, security.ErrOwnershipMismatch)
			assert.Contains(t, err.Error(), "symlink")

			// And nothing was touched on the way out.
			assert.DirExists(t, filepath.Join(elsewhere, "victim"))
		})
	}

	t.Run("a_real_owned_tree_is_fine", func(t *testing.T) {
		base := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(base, "sessions"), 0o700))
		require.NoError(t, os.MkdirAll(filepath.Join(base, "archive"), 0o700))
		require.NoError(t, bootstrapDataDir(base), "the ordinary case must stay ordinary")
	})

	t.Run("a_first_run_has_neither_and_that_is_not_an_error", func(t *testing.T) {
		require.NoError(t, bootstrapDataDir(filepath.Join(t.TempDir(), "brand-new")))
	})

	// A plain FILE where a directory belongs is refused too: redirection is only
	// one of the ways a path leads somewhere it should not.
	t.Run("a_file_where_a_directory_belongs_is_refused", func(t *testing.T) {
		base := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(base, "sessions"), []byte("x"), 0o600))
		err := bootstrapDataDir(base)
		require.Error(t, err)
		assert.ErrorIs(t, err, security.ErrOwnershipMismatch)
	})
}
