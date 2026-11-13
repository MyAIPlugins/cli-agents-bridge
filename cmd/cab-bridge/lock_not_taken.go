package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// F-177: `inbox --tidy`, the `ack` tool and the archiving step of `reply` move messages out of the
// inbox of the session they run in, and they do it under the lock of THAT session. When the lock
// cannot be taken the operation does not run, and what it says has to depend on WHY, because the
// advice is opposite: a lock that is held by someone else passes by waiting, a session that was
// retired will never come back, and a lock file that cannot be opened is a fault in the folder
// that no amount of waiting repairs. Collapsing the three into "busy" would send an agent into a
// retry loop on a fault, which is the worse of the three mistakes (CRI, F-177 plan gate).

// lockOutcome is why a session lock was not taken.
type lockOutcome int

const (
	lockBusy  lockOutcome = iota // another operation holds it: it passes
	lockGone                     // the session was retired: it does not come back
	lockOther                    // anything else (a lock file that is not a file, permissions): look at the folder
)

// lockNotTaken is what an operation returns when it did not run because the session lock could not
// be taken. It is a type, and not a sentinel, so that a failure of the work itself (a move that
// failed with the lock held) can never be read as one: both reach the caller as an error, and only
// this one means "nothing was done and nothing was lost".
type lockNotTaken struct {
	wait time.Duration // how long the operation was willing to wait, for the message
	err  error         // what the session package said, wrapped so errors.Is sees the sentinels
}

func (e *lockNotTaken) Error() string { return e.err.Error() }
func (e *lockNotTaken) Unwrap() error { return e.err }

func (e *lockNotTaken) outcome() lockOutcome {
	switch {
	case errors.Is(e.err, session.ErrLockHeld):
		return lockBusy
	case errors.Is(e.err, session.ErrSessionGone):
		return lockGone
	default:
		return lockOther
	}
}

// underSessionLock runs work with the lock of session sid held and reports apart the two things
// that can go wrong: the lock was not taken (*lockNotTaken, work did not run) and work returned an
// error (whatever it returned, untouched). The callback given to the session package always
// returns nil, so the error that comes out of it can only be the acquisition.
func underSessionLock(mgr *session.Manager, sid string, work func() error) error {
	var workErr error
	if err := mgr.WithSessionLock(sid, func() error {
		workErr = work()
		return nil
	}); err != nil {
		return &lockNotTaken{wait: mgr.LockWait, err: err}
	}
	return workErr
}

// tidyNotRun is the error of `inbox --tidy` for a lock that was not taken. The original error stays
// in the chain (%w), so a caller that tests for ErrLockHeld or ErrSessionGone still can.
func tidyNotRun(e *lockNotTaken) error {
	switch e.outcome() {
	case lockBusy:
		return fmt.Errorf("inbox: --tidy did not run: the session lock was still held by another operation after %s; nothing was moved. Run it again: %w", e.wait, e)
	case lockGone:
		return fmt.Errorf("inbox: --tidy did not run: this session no longer exists (it was retired); nothing was moved: %w", e)
	default:
		return fmt.Errorf("inbox: --tidy did not run: the session lock could not be taken (%v); nothing was moved. This is not a wait that will pass: look at the session folder: %w", e.err, e)
	}
}
