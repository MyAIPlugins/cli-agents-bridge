package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// F-177: what is said when the lock was not taken. This file needs the types of the fix, so it is
// not part of the tests whose red is measured on main (red-f177.sh).

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
		// No advice to try again, in any capitalization: only the "occupied" text carries one.
		assert.NotContains(t, strings.ToLower(g.Error()), "again", "a retired session does not come back")
		assert.NotContains(t, strings.ToLower(o.Error()), "again", "a fault of the folder is not repaired by asking again")
		assert.Contains(t, o.Error(), "could not be taken")
		assert.Contains(t, o.Error(), "not a wait that will pass")
		assert.NotContains(t, o.Error(), "still held")
		for name, e := range map[string]error{"busy": b, "gone": g, "other": o} {
			assert.Contains(t, e.Error(), "nothing was moved", name)
		}
		assert.ErrorIs(t, b, session.ErrLockHeld, "the original error stays in the chain")
		assert.ErrorIs(t, g, session.ErrSessionGone)
		// CRI2 N1: the cause of a lock that cannot be opened is in the sentence once, not twice.
		assert.Equal(t, 1, strings.Count(o.Error(), "is a directory"), "the cause is said once: %q", o.Error())
		assert.ErrorIs(t, o, other, "and it is still in the chain")
	})
	// CRI2 N2: the archive step of `reply` says the same three things as tidy and ack, and in
	// addition what is true only of it: the response is already delivered, how far the archive got,
	// and that the next `reply` finishes it.
	t.Run("reply", func(t *testing.T) {
		b, g, o := replyArchiveNotRun(busy, 1, 2), replyArchiveNotRun(gone, 0, 1), replyArchiveNotRun(other, 0, 1)
		for name, e := range map[string]error{"busy": b, "gone": g, "other": o} {
			assert.Contains(t, e.Error(), "the response is already delivered", name)
			assert.Contains(t, e.Error(), "nothing more was moved", name)
			assert.True(t, strings.HasPrefix(e.Error(), "reply: "), name)
		}
		assert.Contains(t, b.Error(), "stopped at 1 of 2", "the count is the index the journal had")
		assert.Contains(t, b.Error(), "still held by another operation after 5s")
		assert.Contains(t, b.Error(), "Run `reply` again")
		assert.Contains(t, b.Error(), "without sending the response again")
		assert.Contains(t, g.Error(), "no longer exists")
		assert.Contains(t, g.Error(), "nothing left to archive")
		assert.NotContains(t, strings.ToLower(g.Error()), "again", "a retired session does not come back")
		assert.Contains(t, o.Error(), "could not be taken")
		assert.Contains(t, o.Error(), "not a wait that will pass")
		assert.NotContains(t, o.Error(), "still held")
		assert.NotContains(t, strings.ToLower(o.Error()), "again", "a fault of the folder is not repaired by asking again")
		assert.Equal(t, 1, strings.Count(o.Error(), "is a directory"), "the cause is said once: %q", o.Error())
		assert.ErrorIs(t, b, session.ErrLockHeld)
		assert.ErrorIs(t, g, session.ErrSessionGone)
	})
	t.Run("ack", func(t *testing.T) {
		ids := []string{f177Tell, "msg-bbbbbbbbbbbb", f177Tell}
		b, g, o := renderAckNotRun(ids, busy), renderAckNotRun(ids, gone), renderAckNotRun(ids, other)
		assert.Contains(t, b, "still held by another operation after 5s")
		assert.Contains(t, b, "ack again")
		assert.Contains(t, g, "no longer exists")
		assert.NotContains(t, strings.ToLower(g), "again", "a retired session does not come back")
		assert.NotContains(t, strings.ToLower(o), "again", "a fault of the folder is not repaired by asking again")
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
