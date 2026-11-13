package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// F-177, tidy. `inbox --tidy` moves messages out of the inbox of ITS OWN session, and until now it
// did so without the lock of that session: it could cross a retirement (cleanup), and a delivery
// that looks for the response among what the session has archived. Every test here that needs
// somebody else holding the lock puts that somebody in another PROCESS (f177_helper_test.go):
// the lock is re-entrant inside one process, so a goroutine of the test would be let in.

const f177Tell = "msg-aaaaaaaaaaaa"

// f177Session plants a live session with one tell that was shown (NOTIFIED), so that tidy has
// exactly one thing it is allowed to move.
func f177Session(t *testing.T, data, sid string) *session.Manager {
	t.Helper()
	mgr := newSessionManager(fixtureConfig(t, data))
	plantLiveSession(t, data, sid)
	plantMsg(t, data, sid, "inbox", f177Tell, "val12345", "VAL-x", message.TypeNotify, "a shown tell")
	_, err := mgr.CommitWakeCursor(sid, []string{f177Tell}, time.Now().UTC(), nil, nil)
	require.NoError(t, err)
	return mgr
}

func f177SessionDir(data, sid string) string { return filepath.Join(data, "sessions", sid) }

func f177InInbox(data, sid, id string) bool {
	_, err := os.Stat(filepath.Join(f177SessionDir(data, sid), "inbox", id+".json"))
	return err == nil
}

// f177Archived counts the copies of id under processed/ of the session.
func f177Archived(t *testing.T, data, sid, id string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f177SessionDir(data, sid), "processed"))
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "-"+id+".json") {
			n++
		}
	}
	return n
}

// A1: the lock is somebody else's, so tidy does not run, and it says why.
func TestTidyInbox_DoesNotRunWhileAnotherProcessHoldsTheSessionLock(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	const sid = "f177ts01"
	mgr := f177Session(t, data, sid)
	mgr.LockWait = 150 * time.Millisecond
	holdLock(t, data, sid)
	requireHeldByAnother(t, data, sid)

	res, err := tidyInbox(mgr, sid, f177SessionDir(data, sid), 65536)

	require.ErrorIs(t, err, session.ErrLockHeld, "a busy lock is said, not swallowed")
	assert.Equal(t, 0, res.moved)
	assert.True(t, f177InInbox(data, sid, f177Tell), "nothing moved while the lock was somebody else's")
	assert.Equal(t, 0, f177Archived(t, data, sid, f177Tell))
}

// A2: the wait is real. Tidy given time waits for the holder, and moves only after the release.
func TestTidyInbox_WaitsForTheHolderAndMovesOnlyAfterTheRelease(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	const sid = "f177ts02"
	mgr := f177Session(t, data, sid)
	mgr.LockWait = 20 * time.Second // reached only if the holder is never released: the test fails first
	h := holdLock(t, data, sid)
	requireHeldByAnother(t, data, sid)

	type outcome struct {
		res tidyResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := tidyInbox(mgr, sid, f177SessionDir(data, sid), 65536)
		done <- outcome{res, err}
	}()

	time.Sleep(100 * time.Millisecond)
	select {
	case o := <-done:
		require.FailNow(t, "tidy returned while another process still held the lock", "moved=%d err=%v", o.res.moved, o.err)
	default:
	}
	assert.True(t, f177InInbox(data, sid, f177Tell), "while the holder holds the lock the message has not moved")

	h.release()
	select {
	case o := <-done:
		require.NoError(t, o.err)
		assert.Equal(t, 1, o.res.moved)
	case <-time.After(childBound):
		require.FailNow(t, "tidy did not finish within the bound after the holder let go")
	}
	assert.False(t, f177InInbox(data, sid, f177Tell))
	assert.Equal(t, 1, f177Archived(t, data, sid, f177Tell))
}

// A3: the same thing through the CLI the way an agent runs it: exit with an error that says it did
// not run, and what the retry is. The message stays.
func TestRunInbox_Tidy_SaysItDidNotRunWhenTheSessionLockStaysHeld(t *testing.T) {
	// Not parallel: t.Setenv.
	data := useBase(t, t.TempDir())
	t.Setenv("CAB_SESSION_LOCK_WAIT_MS", "150")
	const sid = "f177ts03"
	f177Session(t, data, sid)
	holdLock(t, data, sid)
	requireHeldByAnother(t, data, sid)

	var runErr error
	out := captureStdout(t, func() {
		runErr = runInbox([]string{"--session-id=" + sid, "--tidy"})
	})

	require.Error(t, runErr)
	require.ErrorIs(t, runErr, session.ErrLockHeld)
	assert.Contains(t, runErr.Error(), "did not run")
	assert.Contains(t, runErr.Error(), "nothing was moved")
	assert.Contains(t, runErr.Error(), "Run it again")
	assert.NotContains(t, out, "tidied", "no success line next to a refusal")
	assert.True(t, f177InInbox(data, sid, f177Tell))
}

// A5: a session that was retired has nothing to tidy, and tidy says so instead of reporting
// "0 moved" as if the session were there. Two shapes of "retired": the whole folder gone, and the
// manifest gone with the folder (and the message in it) still on disk, which is what a retirement
// that stopped halfway leaves. In neither does tidy touch the disk.
func TestTidyInbox_OnARetiredSessionSaysSoAndMovesNothing(t *testing.T) {
	t.Parallel()
	t.Run("the folder is gone", func(t *testing.T) {
		t.Parallel()
		data := t.TempDir()
		const sid = "f177ts05"
		mgr := f177Session(t, data, sid)
		require.NoError(t, os.RemoveAll(f177SessionDir(data, sid)))

		_, err := tidyInbox(mgr, sid, f177SessionDir(data, sid), 65536)

		require.ErrorIs(t, err, session.ErrSessionGone)
		assert.NoDirExists(t, f177SessionDir(data, sid), "tidy does not bring a retired session back")
	})
	t.Run("the manifest is gone and the folder is not", func(t *testing.T) {
		t.Parallel()
		data := t.TempDir()
		const sid = "f177ts06"
		mgr := f177Session(t, data, sid)
		require.NoError(t, os.Remove(filepath.Join(f177SessionDir(data, sid), "manifest.json")))

		res, err := tidyInbox(mgr, sid, f177SessionDir(data, sid), 65536)

		require.ErrorIs(t, err, session.ErrSessionGone)
		assert.Equal(t, 0, res.moved)
		assert.True(t, f177InInbox(data, sid, f177Tell), "a session that is retired is not swept")
		assert.Equal(t, 0, f177Archived(t, data, sid, f177Tell))
	})
}

// A6 (tidy half): a lock that cannot be opened at all is neither "busy" nor "gone", and has to be
// told apart from both, because the advice for them is opposite: busy passes by waiting, this one
// does not. Nothing moves.
func TestTidyInbox_ALockThatCannotBeOpenedIsNeitherBusyNorGoneAndMovesNothing(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	const sid = "f177ts07"
	mgr := f177Session(t, data, sid)
	mgr.LockWait = 150 * time.Millisecond
	breakLockFile(t, data, sid)

	res, err := tidyInbox(mgr, sid, f177SessionDir(data, sid), 65536)

	require.Error(t, err)
	assert.NotErrorIs(t, err, session.ErrLockHeld)
	assert.NotErrorIs(t, err, session.ErrSessionGone)
	assert.Equal(t, 0, res.moved)
	assert.True(t, f177InInbox(data, sid, f177Tell))
}

// The words an agent reads when the lock was not taken. They are the only thing that tells it what
// to do next, and the three differ on exactly that: run it again, stop, look at the folder. Each is
// asserted on what it says AND on what it must not say, because "busy" printed for a fault sends an
// agent into a retry loop on something that waiting does not repair.
func TestNotRun_SaysWhichOfTheThreeItIs(t *testing.T) {
	t.Parallel()
	busy := &lockNotTaken{wait: 5 * time.Second, err: fmt.Errorf("session lock: %w", session.ErrLockHeld)}
	gone := &lockNotTaken{wait: 5 * time.Second, err: fmt.Errorf("session lock: %w", session.ErrSessionGone)}
	other := &lockNotTaken{wait: 5 * time.Second, err: errors.New("session lock: open lock: is a directory")}

	t.Run("tidy", func(t *testing.T) {
		b, g, o := tidyNotRun(busy), tidyNotRun(gone), tidyNotRun(other)
		assert.Contains(t, b.Error(), "still held by another operation after 5s")
		assert.Contains(t, b.Error(), "Run it again")
		assert.Contains(t, g.Error(), "no longer exists")
		assert.NotContains(t, g.Error(), "Run it again", "a retired session does not come back")
		assert.Contains(t, o.Error(), "could not be taken")
		assert.Contains(t, o.Error(), "not a wait that will pass")
		assert.NotContains(t, o.Error(), "still held")
		for name, e := range map[string]error{"busy": b, "gone": g, "other": o} {
			assert.Contains(t, e.Error(), "nothing was moved", name)
		}
		assert.ErrorIs(t, b, session.ErrLockHeld, "the original error stays in the chain")
		assert.ErrorIs(t, g, session.ErrSessionGone)
	})
	t.Run("ack", func(t *testing.T) {
		ids := []string{f177Tell, "msg-bbbbbbbbbbbb", f177Tell}
		b, g, o := renderAckNotRun(ids, busy), renderAckNotRun(ids, gone), renderAckNotRun(ids, other)
		assert.Contains(t, b, "still held by another operation after 5s")
		assert.Contains(t, b, "ack again")
		assert.Contains(t, g, "no longer exists")
		assert.NotContains(t, g, "ack again")
		assert.Contains(t, o, "could not be taken")
		assert.Contains(t, o, "not a wait that will pass")
		assert.NotContains(t, o, "still held")
		for name, text := range map[string]string{"busy": b, "gone": g, "other": o} {
			assert.True(t, strings.HasPrefix(text, "Not done."), name)
			assert.Equal(t, 1, strings.Count(text, f177Tell+": not-attempted"), "%s: one line per id, a repeated id once", name)
			assert.Equal(t, 1, strings.Count(text, "msg-bbbbbbbbbbbb: not-attempted"), name)
		}
	})
}
