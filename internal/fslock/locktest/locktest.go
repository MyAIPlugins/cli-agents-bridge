// Package locktest is for tests that ask a kernel lock "are you free?" right after
// letting go of it in the same process.
//
// On Unix a flock is dropped when the LAST descriptor of the open file is closed,
// and a fork in flight in the same process (another goroutine, another parallel
// test, starting a child) carries a copy of every descriptor until its exec. So for
// a few milliseconds a lock that was just released is still busy. The window is
// real and bounded, and it makes "is it free right now?" fail on a rare tail while
// "does it become free?" never does (see internal/fslock).
//
// Only a BUSY answer is retried, and only for Bound: any other error comes back at
// once, because a failure is not contention. A test that asserts the lock is HELD
// does not use this package, since the window only ever makes a lock look busier
// than it is, never freer.
package locktest

import (
	"errors"
	"syscall"
	"time"
)

const (
	// Bound is how long a test waits for a released lock to become free. It is a
	// limit of the TEST, not a promise of the product: the window lasts
	// milliseconds, and a loaded CI runner needs the room.
	Bound = time.Second
	// Poll is the pause between two looks.
	Poll = 5 * time.Millisecond

	// attempts is the numeric limit of every loop here: one look per Poll across
	// Bound, plus the first.
	attempts = int(Bound/Poll) + 1
)

// errNotFree is what BecomesFree feeds to Take while free() says no.
var errNotFree = errors.New("locktest: not free yet")

// Take calls take until it stops answering busy, at most `attempts` times, Poll
// apart. It returns the first result that is not busy, a success or a failure, or
// the last busy one when the bound runs out, so the test prints what it really saw.
func Take[R any](busy func(error) bool, take func() (R, error)) (R, error) {
	var (
		res R
		err error
	)
	for i := 0; i < attempts; i++ {
		res, err = take()
		if err == nil || !busy(err) {
			return res, err
		}
		if i < attempts-1 {
			time.Sleep(Poll)
		}
	}
	return res, err
}

// Settle waits until take succeeds and lets the lock go again, and returns when the
// lock is free for whoever comes next. It is for a fixture that has just released a
// lock and hands the object to code that does NOT wait for it (a retirement, a
// zero-wait acquire): a plain take-and-release probe would be one more "just
// released" lock, with a window of its own.
//
// take runs holding syscall.ForkLock for reading. A fork is done holding it for
// writing (syscall/exec_unix.go), so while the probe's descriptor is open no fork
// can start in this process, and nothing can copy it: its release is a real one.
// take must not create descriptors through a path that takes ForkLock itself (os.Pipe
// does); opening a file does not.
func Settle(busy func(error) bool, take func() (release func() error, err error)) error {
	_, err := Take(busy, func() (struct{}, error) {
		syscall.ForkLock.RLock()
		defer syscall.ForkLock.RUnlock()
		release, err := take()
		if err != nil {
			return struct{}{}, err
		}
		return struct{}{}, release()
	})
	return err
}

// BecomesFree reports whether free returns true within the same bound. It is for
// the probes that already answer with a bool.
func BecomesFree(free func() bool) bool {
	_, err := Take(
		func(err error) bool { return errors.Is(err, errNotFree) },
		func() (struct{}, error) {
			if free() {
				return struct{}{}, nil
			}
			return struct{}{}, errNotFree
		},
	)
	return err == nil
}
