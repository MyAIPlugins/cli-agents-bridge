package session

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/fslock"
)

// holdElsewhere takes the kernel lock on path from a different open file, which
// the kernel treats exactly like another process (the lock belongs to the open file
// description). The tests that need a real second PROCESS say so and use
// startExclHolder or probeLock.
func holdElsewhere(t *testing.T, path string) {
	t.Helper()
	release, err := fslock.TryLock(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = release() })
}

func TestAcquireLock_Fresh(t *testing.T) {
	t.Parallel()

	lockPath := filepath.Join(t.TempDir(), "session.lock")
	release, err := AcquireLock(lockPath, 0, nil)
	require.NoError(t, err)
	require.NotNil(t, release)
	t.Cleanup(func() { _ = release() })

	info, err := os.Stat(lockPath)
	require.NoError(t, err)
	assertPOSIXPerm(t, info.Mode().Perm(), 0o600, "the lock file")
	// The lock is the kernel's: the file holds nothing, and no PID is written to it.
	assert.Zero(t, info.Size(), "the lock file is empty")
}

func TestAcquireLock_HeldByAnotherOpenFile_ReturnsErrLockHeld(t *testing.T) {
	t.Parallel()

	lockPath := filepath.Join(t.TempDir(), "session.lock")
	holdElsewhere(t, lockPath)

	release, err := AcquireLock(lockPath, 0, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrLockHeld)
	assert.Nil(t, release)

	_, statErr := os.Stat(lockPath)
	assert.NoError(t, statErr, "a contender must not remove a lock that is held")
}

func TestAcquireLock_Release_LeavesTheFile(t *testing.T) {
	t.Parallel()

	lockPath := filepath.Join(t.TempDir(), "session.lock")
	release, err := AcquireLock(lockPath, 0, nil)
	require.NoError(t, err)

	require.NoError(t, release())

	info, statErr := os.Stat(lockPath)
	require.NoError(t, statErr, "release gives the lock back and leaves the file: only cleanup removes it, after retiring the session")
	assert.Zero(t, info.Size())

	again, err := AcquireLock(lockPath, 0, nil)
	require.NoError(t, err, "and the lock is free")
	require.NoError(t, again())
}

// CRI2-M1. A lock file that already has something in it (a PID from the old
// format, garbage, a lot of bytes) is locked like any other, and neither taking nor
// releasing the lock writes to it: the sha256 of the bytes is the same before,
// while held, and after.
func TestAcquireLock_APreexistingLockFileWithContentIsLockedAndNeverModified(t *testing.T) {
	t.Parallel()

	contents := map[string][]byte{
		"a pid like the old format":    []byte(strconv.Itoa(os.Getpid()) + "\n"),
		"garbage":                      []byte("not-a-pid\n"),
		"a few kilobytes":              make([]byte, 4096),
		"a pid of a process long gone": []byte("999999\n"),
	}
	for name, content := range contents {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			lockPath := filepath.Join(t.TempDir(), "session.lock")
			require.NoError(t, os.WriteFile(lockPath, content, 0o600))
			want := sha256.Sum256(content)

			release, err := AcquireLock(lockPath, 0, nil)
			require.NoError(t, err, "the content of the file is not a verdict on the lock")

			heldBytes, err := os.ReadFile(lockPath)
			require.NoError(t, err)
			assert.Equal(t, want, sha256.Sum256(heldBytes), "taking the lock must not write to the file")

			require.NoError(t, release())
			afterBytes, err := os.ReadFile(lockPath)
			require.NoError(t, err)
			assert.Equal(t, want, sha256.Sum256(afterBytes), "releasing the lock must not write to the file")
		})
	}
}

func TestIsProcessAlive(t *testing.T) {
	t.Parallel()

	// Self is always alive
	assert.True(t, IsProcessAlive(os.Getpid()))

	// A live process that is not us. PID 1 would be the obvious pick and is
	// wrong: it is init on Unix and nothing at all on Windows, where this
	// function correctly reports it dead.
	assert.True(t, IsProcessAlive(os.Getppid()))

	// PID 0 / negative are invalid
	assert.False(t, IsProcessAlive(0))
	assert.False(t, IsProcessAlive(-1))

	// Very high PID is unlikely to exist
	assert.False(t, IsProcessAlive(999999))
}

// TestAcquireLock_TheMessageNamesNoRemedy is the F-126 regression, and what it
// protects is a SENTENCE — the kind of thing no other test in this repo can
// fail on.
//
// The message used to end with "(use --force-new to override)", baked into the
// primitive. Eleven call sites inherit whatever it says, and the advice was
// true at exactly one of them: Register, the only caller whose forceNew came
// from a flag. Five of the other ten are reached from the five loop verbs,
// which v0.8 gave no flags at all — `cab-bridge ask --force-new` answers "takes
// no flags". So the loop printed a remedy that its own parser refuses.
//
// The rule this pins: a primitive that does not know its caller states FACTS.
// The remedy belongs to whoever knows the command. F-160 removed the flag from
// the lock altogether, but the sentence is still where the next remedy would be
// written, so the tripwire stays.
//
// WHAT IT CANNOT SHOW: that the remedies the callers DO add are the right ones.
// It is a tripwire on the primitive, not an oracle on the eleven.
func TestAcquireLock_TheMessageNamesNoRemedy(t *testing.T) {
	t.Parallel()
	lockPath := filepath.Join(t.TempDir(), "session.lock")
	holdElsewhere(t, lockPath)

	_, err := AcquireLock(lockPath, 0, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrLockHeld)

	msg := err.Error()
	assert.NotContains(t, msg, "--", "the primitive must name NO flag: it does not know which command is calling")

	// And the other half, which is what keeps the assertion above from being
	// satisfied by an empty message: the facts a caller needs are still there.
	// %q is how the message renders it, and on Windows that doubles every
	// backslash: comparing against the raw path would fail on a message that is
	// exactly right. Quoting the expectation the same way keeps one assertion
	// meaning one thing on both hosts.
	assert.Contains(t, msg, strconv.Quote(lockPath), "the lock path is a fact and must survive")
	assert.Contains(t, msg, "still held after waiting", "so is how long the call tried")
	assert.NotContains(t, msg, "pid=", "the lock carries no PID, so the message must not pretend to know the holder")
}
