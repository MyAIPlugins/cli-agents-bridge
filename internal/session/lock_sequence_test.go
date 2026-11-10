package session

// The sequence of a session lock (precheck, open, lock, validate) is what makes
// retiring a session safe, so it is tested as a sequence and not as an outcome:
// every test here goes through acquire, the one function that AcquireLock and
// AcquireSessionLock both call.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/fslock"
)

const (
	seqHelperEnv     = "CAB_SESSION_LOCK_SEQ_HELPER"
	seqModeRetired   = "retired-waiter"
	seqModeWatcher   = "watcher-holder"
	seqEnvDataDir    = seqHelperEnv + "_DATADIR"
	seqEnvSID        = seqHelperEnv + "_SID"
	seqEnvName       = seqHelperEnv + "_NAME"
	seqExitGone      = 11
	seqExitAcquired  = 12
	seqExitOther     = 13
	seqExitStdin     = 14
	seqStdinBound    = 20 * time.Second
	seqChildReadWait = 10 * time.Second
)

func TestAcquire_TheOrderIsPrecheckOpenLockValidate(t *testing.T) {
	t.Parallel()
	lockPath := filepath.Join(t.TempDir(), "session.lock")

	var order []string
	release, err := acquire(lockPath, acquireOpts{
		precheck: func() error {
			order = append(order, "precheck")
			assert.NoFileExists(t, lockPath, "precheck runs before anything is created")
			return nil
		},
		afterOpen: func() {
			order = append(order, "open")
			assert.FileExists(t, lockPath, "the file exists once opened")
			// A second descriptor can still lock it: the lock has not been taken.
			second, serr := fslock.TryLock(lockPath)
			if assert.NoError(t, serr, "between open and lock the lock is still free") {
				assert.NoError(t, second())
			}
		},
		validate: func() error {
			order = append(order, "validate")
			// The lock IS taken: a second descriptor is refused. Validating before
			// the lock would leave the gap in which a session is retired.
			_, serr := fslock.TryLock(lockPath)
			assert.ErrorIs(t, serr, fslock.ErrBusy, "validate runs on a lock that is already held")
			return nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = release() })
	assert.Equal(t, []string{"precheck", "open", "validate"}, order)
}

func TestAcquire_APrecheckThatFailsCreatesNothing(t *testing.T) {
	t.Parallel()
	mgr, sid := registeredSession(t)
	retireByHand(t, mgr, sid)
	require.NoError(t, os.MkdirAll(mgr.sessionDir(sid), 0o700), "a session directory with no manifest")
	before := dirNames(t, mgr.sessionDir(sid))

	release, err := mgr.AcquireSessionLock(sid, 0)

	require.ErrorIs(t, err, ErrSessionGone)
	assert.Nil(t, release)
	assert.Equal(t, before, dirNames(t, mgr.sessionDir(sid)), "nothing was created in the directory of a retired session")
}

func TestAcquire_EveryFailureReleasesWhatItTook(t *testing.T) {
	t.Parallel()

	t.Run("validate fails", func(t *testing.T) {
		t.Parallel()
		lockPath := filepath.Join(t.TempDir(), "session.lock")
		wantErr := errors.New("validate says no")

		_, err := acquire(lockPath, acquireOpts{validate: func() error { return wantErr }})

		require.ErrorIs(t, err, wantErr)
		assert.False(t, heldInThisProcess(lockPath), "no hold is left in the registry")
		assertLockIsFree(t, lockPath)
	})

	t.Run("a directory where the lock should be", func(t *testing.T) {
		t.Parallel()
		lockPath := filepath.Join(t.TempDir(), "session.lock")
		require.NoError(t, os.Mkdir(lockPath, 0o700))

		_, err := acquire(lockPath, acquireOpts{})

		require.Error(t, err)
		assert.False(t, heldInThisProcess(lockPath))
	})

	t.Run("contention", func(t *testing.T) {
		t.Parallel()
		lockPath := filepath.Join(t.TempDir(), "session.lock")
		holdElsewhere(t, lockPath)

		_, err := acquire(lockPath, acquireOpts{})

		require.ErrorIs(t, err, ErrLockHeld)
		assert.False(t, heldInThisProcess(lockPath), "a refused acquisition leaves no hold behind")
	})
}

func assertLockIsFree(t *testing.T, lockPath string) {
	t.Helper()
	release, err := fslock.TryLock(lockPath)
	require.NoError(t, err, "the lock must be free: the failed acquisition gave its descriptor back")
	require.NoError(t, release())
}

// CRI-1 / R2-3. A waiter that opened the lock file just before cleanup retired the
// session locks an inode nobody contends for any more, so the kernel will say yes.
// What stops it is the check made on the lock once taken. The waiter is a real
// process, stopped between the open and the lock, while this process retires the
// session completely.
func TestAcquire_ARetiredInodeStopsAtValidate(t *testing.T) {
	dataDir, sid := exclSession(t)
	mgr := NewManager(dataDir, time.Second)

	child := startSeqChild(t, seqModeRetired, dataDir, sid, "")
	child.expectLine(t, "OPENED")

	retireByHand(t, mgr, sid)
	child.say(t, "GO")

	code := child.wait(t)
	assert.Equal(t, seqExitGone, code, "the waiter must stop with ErrSessionGone, not carry on with a retired session: %s", child.stderr.String())
	assert.NoDirExists(t, mgr.sessionDir(sid), "and it must have created nothing in the place of the retired session")
}

func TestAcquire_ANewcomerAfterRetirementCreatesNothing(t *testing.T) {
	t.Parallel()
	mgr, sid := registeredSession(t)
	retireByHand(t, mgr, sid)

	_, err := mgr.AcquireSessionLock(sid, time.Second)

	require.ErrorIs(t, err, ErrSessionGone)
	assert.NoDirExists(t, mgr.sessionDir(sid), "the directory of the retired session is not recreated")
}

// retireByHand does what cleanup does to a session, in the order cleanup does it
// (manifest first, the other entries, the lock file, the directory). cleanup itself
// cannot be called from here: it imports this package.
func retireByHand(t *testing.T, mgr *Manager, sid string) {
	t.Helper()
	dir := mgr.sessionDir(sid)
	require.NoError(t, os.Remove(filepath.Join(dir, "manifest.json")))
	for _, name := range dirNames(t, dir) {
		if name == LockFileName {
			continue
		}
		require.NoError(t, os.RemoveAll(filepath.Join(dir, name)))
	}
	require.NoError(t, os.Remove(filepath.Join(dir, LockFileName)))
	require.NoError(t, os.Remove(dir))
}

// --- helper process: a waiter stopped between open and lock, and a watcher --------

func TestHelperProcess_SessionLockSequence(t *testing.T) {
	mode := os.Getenv(seqHelperEnv)
	if mode == "" {
		t.Skip("helper process: runs only when re-executed by another test")
	}
	mgr := NewManager(os.Getenv(seqEnvDataDir), time.Second)
	sid := os.Getenv(seqEnvSID)
	switch mode {
	case seqModeRetired:
		_, err := mgr.acquireSessionLock(sid, 0, func() {
			fmt.Println("OPENED")
			if !seqWaitLine("GO") {
				os.Exit(seqExitStdin)
			}
		})
		switch {
		case errors.Is(err, ErrSessionGone):
			os.Exit(seqExitGone)
		case err == nil:
			os.Exit(seqExitAcquired)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(seqExitOther)
	case seqModeWatcher:
		release, err := mgr.AcquireWatcherLock(sid, os.Getenv(seqEnvName))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(seqExitOther)
		}
		fmt.Println("HELD")
		if !seqWaitLine("RELEASE") {
			os.Exit(seqExitStdin)
		}
		_ = release()
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "unknown helper mode %q\n", mode)
	os.Exit(seqExitOther)
}

// seqWaitLine waits for a line on stdin (or EOF) with a bound, and reports whether
// it arrived in time.
func seqWaitLine(want string) bool {
	got := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		got <- strings.TrimSpace(s)
	}()
	select {
	case line := <-got:
		return line == want || line == ""
	case <-time.After(seqStdinBound):
		return false
	}
}

type seqChild struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan string
	stderr *syncBuffer
}

func startSeqChild(t *testing.T, mode, dataDir, sid, name string) *seqChild {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestHelperProcess_SessionLockSequence$")
	cmd.Env = append(os.Environ(), seqHelperEnv+"="+mode, seqEnvDataDir+"="+dataDir, seqEnvSID+"="+sid, seqEnvName+"="+name)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	child := &seqChild{cmd: cmd, stdin: stdin, lines: make(chan string, 8), stderr: &syncBuffer{}}
	cmd.Stderr = child.stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			child.lines <- strings.TrimSpace(sc.Text())
		}
		close(child.lines)
	}()
	return child
}

func (c *seqChild) expectLine(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-c.lines:
		require.Equal(t, want, got, "child said something else; stderr: %s", c.stderr.String())
	case <-time.After(seqChildReadWait):
		require.FailNow(t, "the child never said "+want, c.stderr.String())
	}
}

func (c *seqChild) say(t *testing.T, line string) {
	t.Helper()
	_, err := c.stdin.Write([]byte(line + "\n"))
	require.NoError(t, err)
}

// wait returns the exit code of the child.
func (c *seqChild) wait(t *testing.T) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		require.NoError(t, err)
		return 0
	case <-time.After(seqChildReadWait):
		require.FailNow(t, "the child did not exit", c.stderr.String())
		return -1
	}
}
