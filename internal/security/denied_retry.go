package security

import (
	"fmt"
	"time"
)

// F-154: a read that crosses a deletion answers "Access is denied" on Windows.
//
// A file removed while somebody holds it open is not gone: Windows marks it
// delete-pending, keeps the NAME occupied, and answers every open-by-name with
// ERROR_ACCESS_DENIED until the last handle closes. Our own readers open with
// FILE_SHARE_DELETE (F-133), so they are exactly the handles that make this
// state possible and exactly the ones that end it a few milliseconds later. A
// reader that arrives inside that window used to give up on the first answer.
//
// The remedy is to ask again, briefly, and it lives in one place: this file
// holds the generic loop, perms_windows.go says WHICH error is worth asking
// again about. The loop is untagged so that it is tested on every platform; on
// Unix nothing calls it outside the tests, which is deliberate and not a leftover.
const (
	// Six attempts with FIVE waits between them: 5+10+20+40+80 = 155 ms of
	// requested backoff. This is the backoff REQUESTED from the sleep function,
	// not a deadline: the syscalls and the scheduler add to it, and nothing here
	// measures wall-clock time.
	deniedAttempts   = 6
	deniedFirstDelay = 5 * time.Millisecond
)

// retryWhile runs op up to attempts times, calling sleep between two runs with a
// delay that starts at firstDelay and doubles each time.
//
// It returns nil on the first success. An error that retryable rejects comes
// back at once and untouched, with no wait: a symlink, an ownership mismatch or a
// file that is simply not there will not change in 155 ms, and repeating them
// would only spend the budget to reach the same verdict. When every attempt ends
// in an error retryable accepts, the last one is returned wrapped (%w) with how
// many attempts were made and how much backoff was requested, so the real cause
// is still reachable with errors.Is / errors.As and the message says that the
// answer was not a first reaction.
//
// attempts below 1 is a programming error and is reported as one: running op
// zero times and returning nil would be a success nobody earned.
func retryWhile(attempts int, firstDelay time.Duration, sleep func(time.Duration),
	retryable func(error) bool, op func() error) error {
	if attempts < 1 {
		return fmt.Errorf("retryWhile: attempts must be at least 1, got %d", attempts)
	}
	delay := firstDelay
	var last error
	for attempt := 1; attempt <= attempts; attempt++ {
		last = op()
		if last == nil || !retryable(last) {
			return last
		}
		if attempt == attempts {
			break
		}
		sleep(delay)
		delay *= 2
	}
	return fmt.Errorf("%w (still refused after %d attempts, %s of backoff requested)",
		last, attempts, backoffTotal(attempts, firstDelay))
}

// backoffTotal is the sum of the waits retryWhile requests for the given number
// of attempts: attempts-1 of them, doubling from firstDelay. It exists so the
// error message and the tests compute the figure from the same two inputs
// instead of repeating a number that could drift from the constants.
func backoffTotal(attempts int, firstDelay time.Duration) time.Duration {
	var total time.Duration
	delay := firstDelay
	for wait := 1; wait < attempts; wait++ {
		total += delay
		delay *= 2
	}
	return total
}
