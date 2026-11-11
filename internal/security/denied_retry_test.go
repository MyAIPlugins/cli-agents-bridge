package security

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-154, the half that runs everywhere. The loop is generic and has no Windows
// in it, so everything it promises is proved here against a fake sleep: no test
// below waits for real time, and none needs the platform the defect lives on.

var (
	errTransient = errors.New("transient")
	errPermanent = errors.New("permanent")
)

func isTransient(err error) bool { return errors.Is(err, errTransient) }

// sleeps records every wait the loop asks for.
type sleeps struct{ waits []time.Duration }

func (s *sleeps) sleep(d time.Duration) { s.waits = append(s.waits, d) }

// TestRetryWhile_SucceedsAtOnceWithoutWaiting: the common case costs nothing.
func TestRetryWhile_SucceedsAtOnceWithoutWaiting(t *testing.T) {
	var s sleeps
	calls := 0
	err := retryWhile(6, 5*time.Millisecond, s.sleep, isTransient, func() error {
		calls++
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	assert.Empty(t, s.waits, "a first-time success must not wait")
}

// TestRetryWhile_RidesOutATransientErrorAndStopsAtTheFirstSuccess pins the
// backoff shape (it doubles) and that the loop stops as soon as the operation
// does, instead of spending the rest of the budget.
func TestRetryWhile_RidesOutATransientErrorAndStopsAtTheFirstSuccess(t *testing.T) {
	var s sleeps
	calls := 0
	err := retryWhile(6, 5*time.Millisecond, s.sleep, isTransient, func() error {
		calls++
		if calls < 3 {
			return errTransient
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, calls)
	assert.Equal(t, []time.Duration{5 * time.Millisecond, 10 * time.Millisecond}, s.waits)
}

// TestRetryWhile_NeverSucceedingReportsTheRealErrorAfterTheWholeBudget is the
// bound: six calls, five waits, 155 ms requested, and an error that still
// answers errors.Is for the original cause. 155 ms is a literal on purpose: it is
// the figure the design was accepted on, and computing it from the constants would
// let both drift together without anyone noticing.
func TestRetryWhile_NeverSucceedingReportsTheRealErrorAfterTheWholeBudget(t *testing.T) {
	var s sleeps
	calls := 0
	err := retryWhile(deniedAttempts, deniedFirstDelay, s.sleep, isTransient, func() error {
		calls++
		return errTransient
	})
	require.Error(t, err, "running out of attempts is never a success")
	require.ErrorIs(t, err, errTransient, "the real cause must stay reachable")
	assert.Contains(t, err.Error(), "6 attempts", "and the message must say it was not a first reaction")

	assert.Equal(t, 6, calls)
	assert.Equal(t, []time.Duration{
		5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond,
	}, s.waits)

	var total time.Duration
	for _, w := range s.waits {
		total += w
	}
	assert.Equal(t, 155*time.Millisecond, total)
	assert.Equal(t, 155*time.Millisecond, backoffTotal(deniedAttempts, deniedFirstDelay),
		"the figure in the error message comes from the same two constants the loop runs on")
}

// TestRetryWhile_ANonRetryableErrorComesBackAtOnceAndUntouched: a symlink, an
// ownership mismatch, a missing file. No wait, no second call, no wrapping.
func TestRetryWhile_ANonRetryableErrorComesBackAtOnceAndUntouched(t *testing.T) {
	var s sleeps
	calls := 0
	err := retryWhile(6, 5*time.Millisecond, s.sleep, isTransient, func() error {
		calls++
		return errPermanent
	})
	assert.Same(t, errPermanent, err, "the very same error value, not a wrapper around it")
	assert.Equal(t, 1, calls)
	assert.Empty(t, s.waits)
}

// TestRetryWhile_AnErrorThatTurnsPermanentStopsTheLoop: the verdict of the LAST
// attempt wins. A name that was denied and is then found to be a link (or gone)
// must be reported as that, not retried until the budget runs out.
func TestRetryWhile_AnErrorThatTurnsPermanentStopsTheLoop(t *testing.T) {
	var s sleeps
	calls := 0
	err := retryWhile(6, 5*time.Millisecond, s.sleep, isTransient, func() error {
		calls++
		if calls == 1 {
			return errTransient
		}
		return errPermanent
	})
	assert.Same(t, errPermanent, err)
	assert.Equal(t, 2, calls)
	assert.Equal(t, []time.Duration{5 * time.Millisecond}, s.waits)
}

// TestRetryWhile_ZeroAttemptsIsAnErrorNotASuccess: running nothing and
// returning nil would be a success nobody earned.
func TestRetryWhile_ZeroAttemptsIsAnErrorNotASuccess(t *testing.T) {
	calls := 0
	err := retryWhile(0, 5*time.Millisecond, func(time.Duration) {}, isTransient, func() error {
		calls++
		return nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 1", "the error must name the invalid budget, not be a side effect of an empty loop")
	assert.Equal(t, 0, calls, "the operation must not run under an invalid budget")
}
