package main

import (
	"fmt"
	"os"
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
