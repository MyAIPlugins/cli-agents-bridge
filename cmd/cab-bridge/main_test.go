package main

import (
	"fmt"
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
	real := home + "/.claude/cli-agents-bridge"
	if cfg.DataDir == real {
		t.Fatalf("the suite is pointed at the REAL data dir (%s): a test calling a run* entry point "+
			"would send live messages to real peers and close their asks", cfg.DataDir)
	}
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
			require.NoError(t, os.Symlink(elsewhere, filepath.Join(base, sub)))

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
