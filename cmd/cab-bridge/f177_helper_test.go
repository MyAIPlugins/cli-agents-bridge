package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/fslock"
	"github.com/myAIPlugins/cli-agents-bridge/internal/fslock/locktest"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// F-177: whoever contends for the lock of a session in these tests is ANOTHER PROCESS. The lock is
// re-entrant within a process (internal/session/lock.go, processHolds), so a second goroutine of
// the test would be let in and every exclusion test would pass or fail for the wrong reason. The
// children are the test binary re-executed with -test.run, the way internal/session and
// internal/cleanup do it, and every wait in them has a numeric bound.

const (
	childEnvKind    = "CAB_F177_CHILD"
	childEnvData    = "CAB_F177_DATA"
	childEnvSID     = "CAB_F177_SID"
	childEnvMarkers = "CAB_F177_MARKERS"
	childEnvWaitMs  = "CAB_F177_WAIT_MS"

	childBound = 30 * time.Second
	// holderWait is how long the holder keeps trying to take a lock someone else has for an instant.
	holderWait  = 5 * time.Second
	childFree   = 0
	childBusy   = 7
	childFailed = 9
)

// childLockPath is the lock of a session, as the production code names it.
func childLockPath(data, sid string) string { return session.SessionLockBase(data, sid) }

// TestHelperProcess_F177Hold is a child, not a test: it takes the lock of the session and keeps it
// until the file "release" appears in the markers directory (or the bound runs out).
func TestHelperProcess_F177Hold(t *testing.T) {
	if os.Getenv(childEnvKind) != "hold" {
		t.Skip("helper process: runs only when re-executed by another test")
	}
	markers := os.Getenv(childEnvMarkers)
	// A bounded wait, not one attempt: a heartbeat of the test process can hold the same lock for
	// a few microseconds, and a holder that gave up on the first "busy" would fail the premise of
	// every test that needs it.
	release, err := session.AcquireLock(childLockPath(os.Getenv(childEnvData), os.Getenv(childEnvSID)), holderWait, nil)
	if err != nil {
		_ = os.WriteFile(filepath.Join(markers, "error"), []byte(err.Error()), 0o600)
		os.Exit(childFailed)
	}
	require.NoError(t, os.WriteFile(filepath.Join(markers, "held"), nil, 0o600))
	deadline := time.Now().Add(childBound)
	for time.Now().Before(deadline) {
		if _, serr := os.Stat(filepath.Join(markers, "release")); serr == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = release()
	os.Exit(childFree)
}

// TestHelperProcess_F177Probe is a child: one attempt to take the lock of the session, and it says
// in its exit code whether the lock was free, busy or failed in some other way.
func TestHelperProcess_F177Probe(t *testing.T) {
	if os.Getenv(childEnvKind) != "probe" {
		t.Skip("helper process: runs only when re-executed by another test")
	}
	release, err := fslock.TryLock(childLockPath(os.Getenv(childEnvData), os.Getenv(childEnvSID)))
	switch {
	case err == nil:
		_ = release()
		os.Exit(childFree)
	case errors.Is(err, fslock.ErrBusy):
		os.Exit(childBusy)
	default:
		fmt.Fprintln(os.Stderr, err)
		os.Exit(childFailed)
	}
}

// TestHelperProcess_F177Tidy is a child: the production tidyInbox, run as another process of the
// same session, with the wait for the lock taken from the environment. It prints what it saw.
func TestHelperProcess_F177Tidy(t *testing.T) {
	if os.Getenv(childEnvKind) != "tidy" {
		t.Skip("helper process: runs only when re-executed by another test")
	}
	data, sid := os.Getenv(childEnvData), os.Getenv(childEnvSID)
	mgr := session.NewManager(data, time.Second)
	wait, err := strconv.Atoi(os.Getenv(childEnvWaitMs))
	require.NoError(t, err)
	mgr.LockWait = time.Duration(wait) * time.Millisecond
	res, terr := tidyInbox(mgr, sid, filepath.Join(data, "sessions", sid), 65536)
	fmt.Println(childReport(res.moved, terr))
}

// childReport is the one line a child archiver prints, and childSaw parses it back.
func childReport(moved int, err error) string {
	return fmt.Sprintf("F177CHILD moved=%d busy=%t gone=%t err=%t", moved,
		errors.Is(err, session.ErrLockHeld), errors.Is(err, session.ErrSessionGone), err != nil)
}

var childReportRe = regexp.MustCompile(`F177CHILD moved=(\d+) busy=(true|false) gone=(true|false) err=(true|false)`)

type childSaw struct {
	moved     int
	busy      bool
	gone      bool
	failed    bool
	rawOutput string
}

// sawFrom parses the report of a child archiver. A child that printed nothing parseable is a
// failure of the test, never a "moved nothing".
func sawFrom(t *testing.T, out string) childSaw {
	t.Helper()
	m := childReportRe.FindStringSubmatch(out)
	require.NotNil(t, m, "the child archiver reported nothing parseable:\n%s", out)
	moved, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	return childSaw{moved: moved, busy: m[2] == "true", gone: m[3] == "true", failed: m[4] == "true", rawOutput: out}
}

// runF177Child runs one child to the end, under a bound, and returns its combined output and exit code.
func runF177Child(t *testing.T, kind, data, sid string, extraEnv ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), childBound)
	defer cancel()
	run := map[string]string{"hold": "F177Hold", "probe": "F177Probe", "tidy": "F177Tidy", "ack": "F177Ack"}[kind]
	require.NotEmpty(t, run, "unknown child kind %q", kind)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcess_"+run+"$", "-test.v")
	cmd.Env = append(os.Environ(), childEnvKind+"="+kind, childEnvData+"="+data, childEnvSID+"="+sid)
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exitErr):
		return string(out), exitErr.ExitCode()
	default:
		require.NoError(t, err, "the child could not run: %s", out)
		return string(out), -1
	}
}

// lockIsFreeFor asks, from another process, whether the lock of the session is free right now.
func lockIsFreeFor(t *testing.T, data, sid string) bool {
	t.Helper()
	out, code := runF177Child(t, "probe", data, sid)
	switch code {
	case childFree:
		return true
	case childBusy:
		return false
	default:
		require.FailNow(t, "the probe child failed", "exit %d: %s", code, out)
		return false
	}
}

// lockBecomesFreeFor is lockIsFreeFor for a lock that was just let go in THIS process: a fork in
// flight of another test holds a copy of the descriptor until its exec, so for a few ms another
// process may still see it busy. The bound is numeric (internal/fslock/locktest).
func lockBecomesFreeFor(t *testing.T, data, sid string) bool {
	t.Helper()
	return locktest.BecomesFree(func() bool { return lockIsFreeFor(t, data, sid) })
}

// requireHeldByAnother asserts the premise of an exclusion test: another process really holds it.
func requireHeldByAnother(t *testing.T, data, sid string) {
	t.Helper()
	require.False(t, lockIsFreeFor(t, data, sid), "premise: another process must be holding the lock of %s", sid)
}

// lockHolder is a child that holds the lock of a session until release.
type lockHolder struct {
	markers string
	cmd     *exec.Cmd
	done    bool
}

// holdLock starts a child that takes the lock of the session and returns once it holds it.
func holdLock(t *testing.T, data, sid string) *lockHolder {
	t.Helper()
	markers := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess_F177Hold$")
	cmd.Env = append(os.Environ(), childEnvKind+"=hold", childEnvData+"="+data, childEnvSID+"="+sid, childEnvMarkers+"="+markers)
	require.NoError(t, cmd.Start())
	h := &lockHolder{markers: markers, cmd: cmd}
	t.Cleanup(h.release)
	deadline := time.Now().Add(childBound)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(markers, "held")); err == nil {
			return h
		}
		if _, err := os.Stat(filepath.Join(markers, "error")); err == nil {
			msg, _ := os.ReadFile(filepath.Join(markers, "error"))
			require.FailNow(t, "the holder could not take the lock", string(msg))
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.FailNow(t, "the holder never took the lock within the bound")
	return nil
}

// release lets the holder go and waits for it. Safe to call twice (a test calls it, Cleanup too).
func (h *lockHolder) release() {
	if h.done {
		return
	}
	h.done = true
	_ = os.WriteFile(filepath.Join(h.markers, "release"), nil, 0o600)
	_ = h.cmd.Wait()
}

// breakLockFile replaces the lock file of the session with a directory: the lock can no longer be
// opened, and the failure is neither "busy" nor "gone" (CRI, F-177 gate of the plan).
func breakLockFile(t *testing.T, data, sid string) {
	t.Helper()
	lock := childLockPath(data, sid)
	if err := os.Remove(lock); err != nil && !os.IsNotExist(err) {
		require.NoError(t, err)
	}
	require.NoError(t, os.Mkdir(lock, 0o700))
}
