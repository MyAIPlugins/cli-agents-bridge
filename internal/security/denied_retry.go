package security

import (
	"fmt"
	"time"
)

// F-154: on Windows a read that crosses a deletion is sometimes answered
// "Access is denied" for a few milliseconds, and then gets the true answer: the
// file is gone, or it is the new one. That is MEASURED (PR #25, windows-latest:
// 2763 denials, on a stat as much as on an open; of the 72 events kept, 64 ended
// in "not found" and 8 in success, none was still denied after 3 s, the longest
// lasted 11.84 ms). The state behind it is measured too (probe 2, run
// 43020768866): the name answers STATUS_DELETE_PENDING while a delete is in
// flight, all 876 denials of the race overlapped a delete call, the longest denial
// lasted 811 us and the longest delete call 18.9 ms, against the 155 ms of backoff
// below. One run on one image: the tail is not bounded by it, and
// perms_windows.go has the details. A reader that gave up on the first answer
// reported a permission problem about a file that was about to not exist.
//
// The remedy is to ask again, briefly, and it does not depend on the cause: it
// reacts to the error code and repeats every check. It lives in one place: this
// file holds the generic loop, perms_windows.go says WHICH error is worth asking
// again about. The loop is untagged so that it is tested on every platform; on
// Unix nothing calls it outside the tests, which is deliberate and not a leftover.
//
// THE SAME LOOP SERVES THE ATOMIC REPLACE. The rename of F-133
// (internal/transport/fs/rename_windows.go) repeats through RetryLoop below, with
// its own budget and its own list of errors worth asking again about (ACCESS_DENIED
// and SHARING_VIOLATION). What stays per operation is exactly that, the budget and
// the predicate; what is shared is the loop, so a mistake of count, wait or doubling
// has one place to be made and one place to be fixed. That it stays one is not left to
// the reader: retry_single_loop_test.go reads the whole tree and is red on a second
// copy of the loop, on a budget written as a number at a call, and on a new place that
// waits or that names the predicates, the budgets or the errnos of this retry. What it
// does not see is declared at the top of that file.
//
// THE BUDGETS, in one block and each with where it comes from. Both are the backoff
// REQUESTED from the sleep function, not a deadline: the syscalls and the scheduler
// add to it, and nothing here measures wall-clock time.
const (
	// DENIED READS AND REMOVALS (F-154, F-170): ACCESS_DENIED only. Six attempts with
	// FIVE waits between them: 5+10+20+40+80 = 155 ms. ORIGIN: a chosen figure, set
	// against what the block above measured (the longest denial 11.84 ms, the longest
	// delete call 18.9 ms) and with the limit that block states: one run on one image,
	// the tail is not bounded by it.
	deniedAttempts   = 6
	deniedFirstDelay = 5 * time.Millisecond

	// ATOMIC REPLACE (F-133): ACCESS_DENIED and SHARING_VIOLATION, which
	// rename_windows.go decides. ORIGIN: the ratified design of F-133. No measure of
	// how long the refusals it rides out last is cited for it, and none was made when
	// the budget was moved here: the number is unchanged.
	//
	// Six attempts with FIVE waits between them: 10+20+40+80+160 = 310 ms of waiting
	// at worst. The number is 310 and not 630: six attempts are separated by five
	// gaps, so the sixth delay of the ratified backoff is never spent. The first
	// version of the comment said 630, a comment asserting more than the code
	// executes. The budget is SHARED by the whole operation: opening the source and
	// replacing the target are retried together in ONE loop, so the two stages share
	// at most 310 ms. Chosen so that an antivirus scan of a small file passes under
	// it, while a real permissions error (an ACL that will never yield) costs that
	// much delay before it is reported: the cost is declared, not hidden.
	RenameAttempts   = 6
	RenameFirstDelay = 10 * time.Millisecond
)

// RetryLoop runs op up to attempts times, calling sleep between two runs with a
// delay that starts at firstDelay and doubles each time: attempts-1 waits. It is the
// one loop of the repository that repeats an operation because a filesystem call was
// refused. The polls of the session lock and of the findings lock are another thing:
// they wait for another process to let go, they do not ask again about a refusal.
//
// It returns (nil, false) on the first success. An error that retryable rejects
// comes back at once, untouched and with no wait, as (err, false): a symlink, an
// ownership mismatch or a file that is simply not there will not change in a few
// milliseconds, and repeating them would only spend the budget to reach the same
// verdict. When every attempt ends in an error retryable accepts it returns
// (last, true), where last is the error of the LAST attempt, the very value and
// not a wrapper: each caller words its own message with the numbers it has
// (attempts, backoff requested, time really spent), and the loop measures none of
// them.
//
// attempts below 1 is a programming error and is reported as one, as (err, false):
// running op zero times and returning nil would be a success nobody earned.
func RetryLoop(attempts int, firstDelay time.Duration, sleep func(time.Duration),
	retryable func(error) bool, op func() error) (last error, exhausted bool) {
	if attempts < 1 {
		return fmt.Errorf("RetryLoop: attempts must be at least 1, got %d", attempts), false
	}
	delay := firstDelay
	for attempt := 1; attempt <= attempts; attempt++ {
		last = op()
		if last == nil || !retryable(last) {
			return last, false
		}
		if attempt == attempts {
			break
		}
		sleep(delay)
		delay *= 2
	}
	return last, true
}

// retryWhile is RetryLoop with the message of F-154 and F-170: when every attempt
// ends in an error retryable accepts, the last one is returned wrapped (%w) with how
// many attempts were made and how much backoff was requested, so the real cause is
// still reachable with errors.Is / errors.As and the message says that the answer
// was not a first reaction. Everything else comes back as RetryLoop gives it, an
// attempts below 1 included: that error names RetryLoop, since the check lives there.
func retryWhile(attempts int, firstDelay time.Duration, sleep func(time.Duration),
	retryable func(error) bool, op func() error) error {
	last, exhausted := RetryLoop(attempts, firstDelay, sleep, retryable, op)
	if !exhausted {
		return last
	}
	return fmt.Errorf("%w (still refused after %d attempts, %s of backoff requested)",
		last, attempts, backoffTotal(attempts, firstDelay))
}

// backoffTotal is the sum of the waits RetryLoop requests for the given number
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
