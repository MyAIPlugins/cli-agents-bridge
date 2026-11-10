package session

// Helpers shared by the lock tests that need the new API (so they cannot live in
// lock_exclusion_test.go, which compiles on the tree before the fix).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/fslock"
)

const (
	probeEnv      = "CAB_SESSION_LOCK_PROBE"
	probeFree     = 0
	probeBusy     = 7
	probeFailed   = 9
	probeMaxBound = 30 * time.Second
)

// TestHelperProcess_LockProbe is the child of probeLock, not a test: it tries to
// take the lock at the given path ONCE, as a different process, and reports in its
// exit code whether it was free, busy, or failed.
func TestHelperProcess_LockProbe(t *testing.T) {
	path := os.Getenv(probeEnv)
	if path == "" {
		t.Skip("helper process: runs only when re-executed by another test")
	}
	release, err := fslock.TryLock(path)
	switch {
	case err == nil:
		_ = release()
		os.Exit(probeFree)
	case errors.Is(err, fslock.ErrBusy):
		os.Exit(probeBusy)
	default:
		fmt.Fprintln(os.Stderr, err)
		os.Exit(probeFailed)
	}
}

// probeLock reports whether ANOTHER PROCESS can take the lock at path right now.
// It is the only honest way to ask: in the same process, the registry of
// re-entrant holds would answer for us.
func probeLock(t *testing.T, path string) (free bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), probeMaxBound)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcess_LockProbe$")
	cmd.Env = append(os.Environ(), probeEnv+"="+path)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return true
	case errors.As(err, &exitErr) && exitErr.ExitCode() == probeBusy:
		return false
	default:
		require.FailNow(t, "the probe process failed", "%v: %s", err, out)
		return false
	}
}

// heldInThisProcess reports whether this process's registry holds the lock at
// path. The registry is shared by every test in the package, so a test asks about
// ITS path and never about the size of the whole map.
func heldInThisProcess(path string) bool {
	processHoldsMu.Lock()
	defer processHoldsMu.Unlock()
	_, ok := processHolds[filepath.Clean(path)]
	return ok
}

// registeredSession registers a session on a fresh manager and gives its lock
// back. The session has a manifest, which is what every session lock needs.
func registeredSession(t *testing.T) (*Manager, string) {
	t.Helper()
	dataDir, sid := exclSession(t)
	mgr := NewManager(dataDir, time.Second)
	return mgr, sid
}

// dirNames lists the names under dir, in the order ReadDir gives (sorted), for
// before/after comparisons.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
