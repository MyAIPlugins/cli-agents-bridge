package session

// What happens to the lock FILE: it is created once, by whoever gets there first, it
// is empty, and nobody who merely wants the lock ever removes or replaces it. These
// are the properties a PID file could not give (windows 1, 2 and 4 of the BAR) and
// that the kernel lock gives by construction; the tests say so by trying.

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
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
// The hand-off is made to happen, not hoped for. The first version raced a tight
// loop of the successor against the contender's poll and counted on the successor
// winning; it does not win often enough. Measured on a Mac (F-163, 11 Nov 2026, 300
// rounds each): the contender took the lock in the gap in 29 rounds on a quiet
// machine and in 25 on a loaded one, each time before the successor's loop was over,
// and the macOS runner lost three rounds in a row. The cause is the race, not a
// descriptor copied by a fork (locktest): in none of the 600 rounds did the contender
// get the lock after the successor's loop was over.
//
// So the contender is parked by the afterOpen seam inside its first attempt, with its
// file open and not yet locked, which is the position a poll across a hand-off is
// in. While it is parked the holder lets go and the successor takes the lock, with no
// competitor; then the contender is released, locks, finds the successor and keeps
// polling until its wait is over.
func TestLock_ASuccessorKeepsItsLockWhileAContenderPolls(t *testing.T) {
	const (
		contenderWait = 100 * time.Millisecond // 11 attempts, lockPoll apart
		parkedBound   = 5 * time.Second        // how long to wait for the contender to reach its first attempt
	)
	lockPath := filepath.Join(t.TempDir(), "session.lock")
	releaseH, err := fslock.TryLock(lockPath)
	require.NoError(t, err)
	before, err := locktest.Identity(lockPath) // the identity now: see locktest.Identity
	require.NoError(t, err)

	parked, proceed := make(chan struct{}), make(chan struct{})
	var parkOnce, proceedOnce sync.Once
	letGo := func() { proceedOnce.Do(func() { close(proceed) }) }
	// A parked attempt holds the registry mutex of acquire: whatever fails below, the
	// contender must not stay parked, or every later acquire in this package waits for it.
	t.Cleanup(letGo)
	hook := func() { parkOnce.Do(func() { close(parked); <-proceed }) }

	type result struct {
		release func() error
		err     error
	}
	contender := make(chan result, 1)
	go func() {
		rel, cerr := acquire(lockPath, acquireOpts{wait: contenderWait, afterOpen: hook})
		contender <- result{rel, cerr}
	}()

	select {
	case <-parked:
	case <-time.After(parkedBound):
		t.Fatalf("the contender did not reach its first attempt within %s", parkedBound)
	}
	require.NoError(t, releaseH())
	// The lock is free and the only other file open on it is the parked one, so a busy
	// answer here can only be a fork in flight: the bounded wait of locktest covers it.
	releaseN, err := locktest.Take(
		func(err error) bool { return errors.Is(err, fslock.ErrBusy) },
		func() (func() error, error) { return fslock.TryLock(lockPath) },
	)
	require.NoError(t, err, "the successor takes the lock: the contender has not locked yet")
	t.Cleanup(func() { _ = releaseN() })
	letGo()

	got := <-contender
	assert.ErrorIs(t, got.err, ErrLockHeld, "the contender gave up: the successor holds the lock")
	assert.Nil(t, got.release, "and it got nothing to release")
	after, err := locktest.Identity(lockPath)
	require.NoError(t, err)
	assert.True(t, os.SameFile(before, after), "the contender's polling did not remove or replace the file")
	_, err = fslock.TryLock(lockPath)
	assert.ErrorIs(t, err, fslock.ErrBusy, "the successor still holds its lock")
}
