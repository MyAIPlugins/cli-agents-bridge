//go:build !windows

package session

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHelperIdentityHolder is not a test: it is the body of a child process the
// tests below start, so the lock is exercised across REAL processes. A test that
// asserts an exclusion between two goroutines of one process proves nothing about
// the case that matters, and this repository has already paid for that once.
func TestHelperIdentityHolder(t *testing.T) {
	if os.Getenv("CAB_TEST_IDENTITY_HELPER") != "1" {
		t.Skip("helper process only")
	}
	mgr := NewManager(os.Getenv("CAB_TEST_DATADIR"), time.Second)
	lock, err := mgr.AcquireIdentityLock(os.Getenv("CAB_TEST_IDENTITY"))
	if err != nil {
		fmt.Println("ERR", err)
		os.Exit(3)
	}
	defer func() { _ = lock.Release() }()
	fmt.Println("held", os.Getpid())
	if os.Getenv("CAB_TEST_SPAWN_CHILD") == "1" {
		child := exec.Command("/bin/sleep", "120")
		if err := child.Start(); err != nil {
			fmt.Println("ERR spawn", err)
			os.Exit(4)
		}
		fmt.Println("child", child.Process.Pid)
	}
	_, _ = io.Copy(io.Discard, os.Stdin) // hold the lock until the test closes our stdin
}

type holder struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	pid      int
	childPID int
}

func startHolder(t *testing.T, dataDir, identity string, spawnChild bool) *holder {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperIdentityHolder$")
	cmd.Env = append(os.Environ(), "CAB_TEST_IDENTITY_HELPER=1", "CAB_TEST_DATADIR="+dataDir, "CAB_TEST_IDENTITY="+identity)
	if spawnChild {
		cmd.Env = append(cmd.Env, "CAB_TEST_SPAWN_CHILD=1")
	}
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())

	h := &holder{cmd: cmd, stdin: stdin}
	t.Cleanup(func() {
		if h.childPID > 0 {
			_ = syscall.Kill(h.childPID, syscall.SIGKILL)
		}
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	lines := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	want := 1
	if spawnChild {
		want = 2
	}
	for i := 0; i < want; i++ {
		select {
		case line, ok := <-lines:
			require.True(t, ok, "the helper exited before reporting")
			require.False(t, strings.HasPrefix(line, "ERR"), "helper: %s", line)
			fields := strings.Fields(line)
			require.Len(t, fields, 2, line)
			n, err := strconv.Atoi(fields[1])
			require.NoError(t, err)
			if fields[0] == "held" {
				h.pid = n
			} else {
				h.childPID = n
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the helper did not report in time")
		}
	}
	require.NotZero(t, h.pid)
	return h
}

func newLockManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(t.TempDir(), time.Second)
}

func TestIdentityLock_SecondAcquirerInTheSameProcessIsRefusedWithTheHolder(t *testing.T) {
	t.Parallel()
	mgr := newLockManager(t)
	first, err := mgr.AcquireIdentityLock("/repo/a/.cab-desktop/cowork")
	require.NoError(t, err)

	second, err := mgr.AcquireIdentityLock("/repo/a/.cab-desktop/cowork")
	require.Error(t, err)
	assert.Nil(t, second)
	require.ErrorIs(t, err, ErrIdentityHeld)
	var held *HeldError
	require.True(t, errors.As(err, &held))
	assert.Equal(t, os.Getpid(), held.PID, "the error names who holds it")

	require.NoError(t, first.Release())
	third, err := mgr.AcquireIdentityLock("/repo/a/.cab-desktop/cowork")
	require.NoError(t, err, "released means free")
	require.NoError(t, third.Release())
}

func TestIdentityLock_DifferentIdentitiesAreIndependent(t *testing.T) {
	t.Parallel()
	mgr := newLockManager(t)
	a, err := mgr.AcquireIdentityLock("/repo/a/.cab-desktop/cowork")
	require.NoError(t, err)
	b, err := mgr.AcquireIdentityLock("/repo/a/.cab-desktop/chat")
	require.NoError(t, err, "chat and Cowork of one project hold different identities")
	c, err := mgr.AcquireIdentityLock("/repo/b/.cab-desktop/cowork")
	require.NoError(t, err)
	for _, l := range []*IdentityLock{a, b, c} {
		require.NoError(t, l.Release())
	}
}

func TestIdentityLock_FilesAndDirectoryArePrivateAndNeverRemoved(t *testing.T) {
	t.Parallel()
	mgr := newLockManager(t)
	lock, err := mgr.AcquireIdentityLock("/repo/a/.cab-desktop/cowork")
	require.NoError(t, err)

	dirInfo, err := os.Stat(filepath.Join(mgr.DataDir, identityLockDir))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
	fileInfo, err := os.Stat(lock.Path())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fileInfo.Mode().Perm())
	data, err := os.ReadFile(lock.Path())
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(os.Getpid())+"\n", string(data), "the holder is recorded for the next contender")

	require.NoError(t, lock.Release())
	require.NoError(t, lock.Release(), "release is idempotent")
	assert.FileExists(t, lock.Path(), "an unlocked file is a free identity: removing it would put the race back")
}

func TestIdentityLock_RefusesAnEmptyIdentity(t *testing.T) {
	t.Parallel()
	mgr := newLockManager(t)
	for _, id := range []string{"", "   "} {
		_, err := mgr.AcquireIdentityLock(id)
		require.Error(t, err, "%q", id)
	}
}

func TestIdentityLock_RefusesALockDirectoryThatIsASymlink(t *testing.T) {
	t.Parallel()
	mgr := newLockManager(t)
	elsewhere := t.TempDir()
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(mgr.DataDir, identityLockDir)))

	_, err := mgr.AcquireIdentityLock("/repo/a/.cab-desktop/cowork")
	require.Error(t, err, "a redirected lock directory means the exclusion would be taken somewhere else")
	entries, rerr := os.ReadDir(elsewhere)
	require.NoError(t, rerr)
	assert.Empty(t, entries, "and nothing was written through the link")
}

// Exactly one of many racing acquirers wins, with the race detector watching.
func TestIdentityLock_ManyRacersExactlyOneWins(t *testing.T) {
	mgr := newLockManager(t)
	const racers = 24
	var wg sync.WaitGroup
	var mu sync.Mutex
	var winners []*IdentityLock
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			l, err := mgr.AcquireIdentityLock("/repo/race/.cab-desktop/cowork")
			if err != nil {
				if !errors.Is(err, ErrIdentityHeld) {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			mu.Lock()
			winners = append(winners, l)
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	require.Len(t, winners, 1, "exactly one acquirer may hold the identity")
	require.NoError(t, winners[0].Release())
}

// The case that decides the design: a REAL other process holds it, then dies the
// hardest way there is. No stale file to detect, no takeover to race.
func TestIdentityLock_AnotherProcessHoldsItAndKillDashNineFreesIt(t *testing.T) {
	dataDir := t.TempDir()
	const identity = "/repo/real/.cab-desktop/cowork"
	h := startHolder(t, dataDir, identity, false)
	mgr := NewManager(dataDir, time.Second)

	_, err := mgr.AcquireIdentityLock(identity)
	require.ErrorIs(t, err, ErrIdentityHeld)
	var held *HeldError
	require.True(t, errors.As(err, &held))
	assert.Equal(t, h.pid, held.PID, "the contender is told which process holds it")

	require.NoError(t, h.cmd.Process.Kill()) // SIGKILL: no deferred release, nothing runs
	_, _ = h.cmd.Process.Wait()

	lock, err := mgr.AcquireIdentityLock(identity)
	require.NoError(t, err, "the kernel released it with the process")
	require.NoError(t, lock.Release())
}

// The descriptor must not be inherited: the MCP server starts `cab-bridge ask` as
// a child, and a child that kept the lock alive after the server died would lock
// the identity out until the child finished.
func TestIdentityLock_AChildOfTheHolderDoesNotKeepTheLockAlive(t *testing.T) {
	dataDir := t.TempDir()
	const identity = "/repo/child/.cab-desktop/cowork"
	h := startHolder(t, dataDir, identity, true)
	require.NotZero(t, h.childPID)
	mgr := NewManager(dataDir, time.Second)

	require.NoError(t, h.cmd.Process.Kill())
	_, _ = h.cmd.Process.Wait()
	require.NoError(t, syscall.Kill(h.childPID, 0), "the child is still alive: the case this test is about")

	lock, err := mgr.AcquireIdentityLock(identity)
	require.NoError(t, err, "a surviving child of the holder must not hold the identity")
	require.NoError(t, lock.Release())
}
