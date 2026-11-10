package session

// What happens to the lock FILE: it is created once, by whoever gets there first, it
// is empty, and nobody who merely wants the lock ever removes or replaces it. These
// are the properties a PID file could not give (windows 1, 2 and 4 of the BAR) and
// that the kernel lock gives by construction; the tests say so by trying.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/fslock"
	"github.com/myAIPlugins/cli-agents-bridge/internal/fslock/locktest"
)

const (
	firstEnv        = "CAB_SESSION_LOCK_FIRST_HELPER"
	firstEnvDir     = firstEnv + "_DIR"
	firstEnvID      = firstEnv + "_ID"
	firstEnvRounds  = firstEnv + "_ROUNDS"
	firstExitMixup  = 8
	firstExitFailed = 9
	firstProcs      = 8
	firstRounds     = 300
)

// N6. Eight processes race to create the lock at a path that does not exist yet, in
// 300 rounds. In every round they all get the lock in turn, the descriptor each one
// locked is the file that is at the path (one inode, not one per creator), and
// nobody finds the file with anything in it.
func TestLock_FirstCreationLocksOneInode(t *testing.T) {
	rounds := firstRounds
	if testing.Short() {
		rounds = 60
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), exclRunBound)
	defer cancel()

	cmds := make([]*exec.Cmd, firstProcs)
	for id := range cmds {
		cmds[id] = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcess_FirstCreation$")
		cmds[id].Env = append(os.Environ(), firstEnvDir+"="+dir, firstEnvID+"="+strconv.Itoa(id), firstEnvRounds+"="+strconv.Itoa(rounds))
		require.NoError(t, cmds[id].Start())
	}
	t.Cleanup(func() {
		for _, cmd := range cmds {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		}
	})

	for r := 0; r < rounds; r++ {
		rd := filepath.Join(dir, fmt.Sprintf("r%d", r))
		require.NoError(t, os.Mkdir(rd, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(rd, "go"), nil, 0o600))
		for id := 0; id < firstProcs; id++ {
			held := filepath.Join(rd, "held-"+strconv.Itoa(id))
			require.True(t, exclWaitForFile(held), "round %d: process %d never held the lock", r, id)
		}
	}
	for id, cmd := range cmds {
		require.NoError(t, cmd.Wait(), "process %d reported that it locked a different file, or failed", id)
	}
}

// TestHelperProcess_FirstCreation is the child of the test above, not a test.
func TestHelperProcess_FirstCreation(t *testing.T) {
	dir := os.Getenv(firstEnvDir)
	if dir == "" {
		t.Skip("helper process: runs only when re-executed by another test")
	}
	id := os.Getenv(firstEnvID)
	rounds := exclEnvInt(firstEnvRounds)
	for r := 0; r < rounds; r++ {
		rd := filepath.Join(dir, fmt.Sprintf("r%d", r))
		if !exclWaitForFile(filepath.Join(rd, "go")) {
			fmt.Fprintf(os.Stderr, "process %s: round %d never started\n", id, r)
			os.Exit(firstExitFailed)
		}
		lockPath := filepath.Join(rd, "lock")
		release, err := AcquireLock(lockPath, 10*time.Second, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "process %s: round %d: %v\n", id, r, err)
			os.Exit(firstExitFailed)
		}
		if msg := firstCheckLocked(lockPath); msg != "" {
			fmt.Fprintf(os.Stderr, "process %s: round %d: %s\n", id, r, msg)
			os.Exit(firstExitMixup)
		}
		if err := os.WriteFile(filepath.Join(rd, "held-"+id), nil, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(firstExitFailed)
		}
		if err := release(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(firstExitFailed)
		}
	}
	os.Exit(0)
}

// firstCheckLocked compares the descriptor this process holds (read from the
// registry) with the file at the path, and looks at what is in it.
func firstCheckLocked(lockPath string) string {
	processHoldsMu.Lock()
	h, ok := processHolds[filepath.Clean(lockPath)]
	processHoldsMu.Unlock()
	if !ok {
		return "the lock was acquired but is not in the registry"
	}
	locked, err := h.f.Stat()
	if err != nil {
		return err.Error()
	}
	atPath, err := os.Stat(lockPath)
	if err != nil {
		return err.Error()
	}
	if !os.SameFile(locked, atPath) {
		return "the locked descriptor is not the file at the path: two inodes were created"
	}
	if atPath.Size() != 0 {
		return fmt.Sprintf("the lock file has %d bytes in it", atPath.Size())
	}
	return ""
}

// A contender that fails to get the lock leaves the file exactly as it was: same
// inode, same bytes, and the holder is still holding. 10,000 contenders, one after
// the other, none of them with a reason to touch it.
func TestLock_AContenderNeverRemovesOrReplacesTheFile(t *testing.T) {
	t.Parallel()
	lockPath := filepath.Join(t.TempDir(), "session.lock")
	holdElsewhere(t, lockPath)
	before, err := locktest.Identity(lockPath) // the identity now: see locktest.Identity
	require.NoError(t, err)
	beforeBytes, readable := readHeldLockFile(t, lockPath)

	attempts := 10000
	if testing.Short() {
		attempts = 1000
	}
	for i := 0; i < attempts; i++ {
		_, err := AcquireLock(lockPath, 0, nil)
		require.ErrorIs(t, err, ErrLockHeld, "attempt %d", i)
	}

	after, err := locktest.Identity(lockPath)
	require.NoError(t, err)
	afterBytes, _ := readHeldLockFile(t, lockPath)
	assert.True(t, os.SameFile(before, after), "the file is the same inode after %d contenders", attempts)
	assert.Equal(t, before.Size(), after.Size(), "and it is as long as before")
	if readable { // Unix. Windows cannot read the bytes while the lock is held: the size above is what it can compare
		assert.Equal(t, sha256.Sum256(beforeBytes), sha256.Sum256(afterBytes))
	}
	_, err = fslock.TryLock(lockPath)
	assert.ErrorIs(t, err, fslock.ErrBusy, "and the holder is still holding")
}

// Window 2, seen from the new code. The old function, in the order "the holder
// releases after our EEXIST, a successor takes the lock after our ENOENT", returned
// success having deleted the successor's lock. Here a contender that polls across a
// hand-off disturbs nothing: the file is the same inode before and after, and
// whoever holds the lock afterwards is still holding it when the polling is over.
//
// The successor is a tight loop and the contender polls every lockPoll, so the
// successor wins the hand-off almost always. If the contender wins it, that round
// proves nothing and is repeated, at most three times, with a fresh lock.
func TestLock_ASuccessorKeepsItsLockWhileAContenderPolls(t *testing.T) {
	const maxRounds = 3
	for round := 0; round < maxRounds; round++ {
		lockPath := filepath.Join(t.TempDir(), "session.lock")
		releaseH, err := fslock.TryLock(lockPath)
		require.NoError(t, err)
		before, err := locktest.Identity(lockPath) // the identity now: see locktest.Identity
		require.NoError(t, err)

		type result struct {
			release func() error
			err     error
		}
		contender := make(chan result, 1)
		go func() {
			rel, cerr := AcquireLock(lockPath, 300*time.Millisecond, nil)
			contender <- result{rel, cerr}
		}()

		time.Sleep(100 * time.Millisecond) // the contender is polling
		require.NoError(t, releaseH())
		var releaseN func() error
		for i := 0; i < 2000 && releaseN == nil; i++ { // the successor, as fast as it can
			if rel, terr := fslock.TryLock(lockPath); terr == nil {
				releaseN = rel
			}
		}

		got := <-contender
		if releaseN == nil {
			// The contender took it in the gap, so there is no successor to check:
			// give its lock back and go again.
			require.NoError(t, got.err)
			require.NoError(t, got.release())
			continue
		}
		t.Cleanup(func() { _ = releaseN() })
		assert.ErrorIs(t, got.err, ErrLockHeld, "the contender gave up: the successor holds the lock")
		after, err := locktest.Identity(lockPath)
		require.NoError(t, err)
		assert.True(t, os.SameFile(before, after), "the contender's polling did not remove or replace the file")
		_, err = fslock.TryLock(lockPath)
		assert.ErrorIs(t, err, fslock.ErrBusy, "the successor still holds its lock")
		return
	}
	t.Fatalf("the contender won the hand-off in %d rounds out of %d: nothing was checked", maxRounds, maxRounds)
}
