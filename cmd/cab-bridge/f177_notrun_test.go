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
