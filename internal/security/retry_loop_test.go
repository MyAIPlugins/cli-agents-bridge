package security

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The ONE retry loop, as the callers outside this package see it. retryWhile (the
// wrapper that words the F-154 / F-170 message) is tested in denied_retry_test.go and
// is not repeated here: what is fixed below is what the atomic replace of F-133 relies
// on to keep its own message, its own trace and its own diagnostic branches.
//
// Like the tests of retryWhile this runs on every platform: the loop has no Windows in
// it, and no test here waits for real time.

// TestRetryLoop_SucceedsAtOnceWithoutWaiting: the normal case costs nothing.
func TestRetryLoop_SucceedsAtOnceWithoutWaiting(t *testing.T) {
	var s sleeps
	calls := 0

	last, exhausted := RetryLoop(6, time.Millisecond, s.sleep, isTransient, func() error {
		calls++
		return nil
	})

	assert.NoError(t, last)
	assert.False(t, exhausted)
	assert.Equal(t, 1, calls)
	assert.Empty(t, s.waits, "no wait when the first attempt works")
}

// TestRetryLoop_RidesOutTransientErrorsAndStopsAtTheFirstSuccess: k attempts have k-1
// waits, and the loop does not run once more after the success.
func TestRetryLoop_RidesOutTransientErrorsAndStopsAtTheFirstSuccess(t *testing.T) {
	var s sleeps
	calls := 0

	last, exhausted := RetryLoop(6, 10*time.Millisecond, s.sleep, isTransient, func() error {
		calls++
		if calls < 4 {
			return errTransient
		}
		return nil
	})

	assert.NoError(t, last)
	assert.False(t, exhausted, "a success is not an exhausted budget")
	assert.Equal(t, 4, calls)
	assert.Equal(t, []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}, s.waits)
}

// TestRetryLoop_ABudgetThatRunsOutHandsBackTheLastErrorUntouched is the contract the
// atomic replace depends on, for BOTH real budgets. Two things are fixed:
//   - the error that comes back is the VALUE the last attempt returned, not a wrapper
//     around it: the rename words its own final message (with the time it really spent,
//     which this loop does not measure) around that error, and a wrapped one would change
//     the text a caller reads and what a type assertion on it finds;
//   - the waits asked for, as LITERALS. Computing them from the constants would let the
//     constants and the figure drift together without anyone noticing, which is how the
//     first comment of F-133 came to say 630 ms for a loop that asks for 310.
func TestRetryLoop_ABudgetThatRunsOutHandsBackTheLastErrorUntouched(t *testing.T) {
	ms := time.Millisecond
	for _, b := range []struct {
		name     string
		attempts int
		first    time.Duration
		waits    []time.Duration
		total    time.Duration
	}{
		{"denied reads and removals (F-154, F-170)", deniedAttempts, deniedFirstDelay,
			[]time.Duration{5 * ms, 10 * ms, 20 * ms, 40 * ms, 80 * ms}, 155 * ms},
		{"atomic replace (F-133)", RenameAttempts, RenameFirstDelay,
			[]time.Duration{10 * ms, 20 * ms, 40 * ms, 80 * ms, 160 * ms}, 310 * ms},
	} {
		t.Run(b.name, func(t *testing.T) {
			var s sleeps
			calls := 0
			failures := make([]error, 0, b.attempts)

			last, exhausted := RetryLoop(b.attempts, b.first, s.sleep, isTransient, func() error {
				calls++
				err := fmt.Errorf("attempt %d: %w", calls, errTransient)
				failures = append(failures, err)
				return err
			})

			require.True(t, exhausted)
			require.Len(t, failures, 6, "six attempts: the budget is a literal here on purpose")
			assert.Same(t, failures[5], last, "the LAST attempt's error, the very value, not a wrapper")
			assert.Equal(t, 6, calls)
			assert.Equal(t, b.waits, s.waits)
			var total time.Duration
			for _, w := range s.waits {
				total += w
			}
			assert.Equal(t, b.total, total)
		})
	}
}

// TestRetryLoop_AnErrorThatTurnsPermanentComesBackAtOnceAtAnyAttempt: the loop stops at
// the attempt where the error stops being worth repeating, whichever it is, with no wait
// after it and no second call, and it is NOT exhaustion. The sixth attempt is the branch
// next door: failing for good on the LAST attempt is not "still refused after the whole
// budget", and the rename words the two differently.
func TestRetryLoop_AnErrorThatTurnsPermanentComesBackAtOnceAtAnyAttempt(t *testing.T) {
	for k := 1; k <= 6; k++ {
		t.Run(fmt.Sprintf("permanent at attempt %d", k), func(t *testing.T) {
			var s sleeps
			calls := 0

			last, exhausted := RetryLoop(6, 10*time.Millisecond, s.sleep, isTransient, func() error {
				calls++
				if calls < k {
					return errTransient
				}
				return errPermanent
			})

			assert.Same(t, errPermanent, last, "untouched")
			assert.False(t, exhausted, "an error that is not worth repeating is not an exhausted budget")
			assert.Equal(t, k, calls)
			assert.Len(t, s.waits, k-1, "one wait between two attempts and none after the last")
		})
	}
}

// TestRetryLoop_ZeroAttemptsIsAnErrorAndRunsNothing: running op zero times and returning
// nil would be a success nobody earned. Said by name, and not as an exhausted budget.
func TestRetryLoop_ZeroAttemptsIsAnErrorAndRunsNothing(t *testing.T) {
	for _, attempts := range []int{0, -1} {
		var s sleeps
		calls := 0

		last, exhausted := RetryLoop(attempts, time.Millisecond, s.sleep, isTransient, func() error {
			calls++
			return nil
		})

		require.Error(t, last)
		assert.Contains(t, last.Error(), fmt.Sprintf("RetryLoop: attempts must be at least 1, got %d", attempts))
		assert.False(t, exhausted)
		assert.Zero(t, calls)
		assert.Empty(t, s.waits)
	}
}
