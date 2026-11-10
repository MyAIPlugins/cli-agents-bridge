package locktest

import (
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	errBusy   = errors.New("busy")
	errFailed = errors.New("a failure that is not contention")
)

func isBusy(err error) bool { return errors.Is(err, errBusy) }

func TestTake_ASuccessComesBackAtOnce(t *testing.T) {
	t.Parallel()
	calls := 0
	got, err := Take(isBusy, func() (string, error) {
		calls++
		return "release", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "release", got)
	assert.Equal(t, 1, calls)
}

func TestTake_BusyIsRetriedUntilItClears(t *testing.T) {
	t.Parallel()
	calls := 0
	got, err := Take(isBusy, func() (string, error) {
		calls++
		if calls < 4 {
			return "", errBusy
		}
		return "release", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "release", got, "the result of the attempt that worked, not of a busy one")
	assert.Equal(t, 4, calls)
}

// The reason the busy check is passed in: a helper that retried everything would
// hide a real failure behind a second of polling.
func TestTake_AnErrorThatIsNotBusyIsNeverRetried(t *testing.T) {
	t.Parallel()
	calls := 0
	_, err := Take(isBusy, func() (string, error) {
		calls++
		return "", errFailed
	})

	require.ErrorIs(t, err, errFailed)
	assert.Equal(t, 1, calls)
}

func TestTake_TheBoundIsNumericAndTheLastBusyAnswerComesBack(t *testing.T) {
	t.Parallel()
	calls := 0
	_, err := Take(isBusy, func() (string, error) {
		calls++
		return "", errBusy
	})

	require.ErrorIs(t, err, errBusy)
	assert.Equal(t, attempts, calls, "one look per Poll across Bound, and then it stops")
}

func TestSettle_ReleasesWhatItTookExactlyOnce(t *testing.T) {
	t.Parallel()
	released := 0
	err := Settle(isBusy, func() (func() error, error) {
		return func() error { released++; return nil }, nil
	})

	require.NoError(t, err)
	assert.Equal(t, 1, released)
}

func TestSettle_BusyIsRetriedAndOnlyTheSuccessIsReleased(t *testing.T) {
	t.Parallel()
	calls, released := 0, 0
	err := Settle(isBusy, func() (func() error, error) {
		calls++
		if calls < 3 {
			return nil, errBusy
		}
		return func() error { released++; return nil }, nil
	})

	require.NoError(t, err)
	assert.Equal(t, 3, calls)
	assert.Equal(t, 1, released)
}

func TestSettle_AnErrorThatIsNotBusyComesBackAndNothingIsReleased(t *testing.T) {
	t.Parallel()
	calls := 0
	err := Settle(isBusy, func() (func() error, error) {
		calls++
		return nil, errFailed
	})

	require.ErrorIs(t, err, errFailed)
	assert.Equal(t, 1, calls)
}

func TestSettle_AReleaseThatFailsIsReported(t *testing.T) {
	t.Parallel()
	err := Settle(isBusy, func() (func() error, error) {
		return func() error { return errFailed }, nil
	})

	require.ErrorIs(t, err, errFailed)
}

// The point of Settle: while the probe's descriptor is open, nothing can fork in
// this process, because a fork needs ForkLock for writing and the probe holds it for
// reading. A goroutine that asks for it must wait for the probe to end.
func TestSettle_NoForkCanStartWhileTheProbeIsOpen(t *testing.T) {
	t.Parallel()
	forked := make(chan struct{})

	err := Settle(isBusy, func() (func() error, error) {
		go func() {
			syscall.ForkLock.Lock()
			close(forked) // proof that the lock was obtained: only reachable once the probe is over
			syscall.ForkLock.Unlock()
		}()
		time.Sleep(50 * time.Millisecond) // long enough for the goroutine to be refused, if it can be
		select {
		case <-forked:
			t.Error("a fork could start while the probe's descriptor was open")
		default:
		}
		return func() error { return nil }, nil
	})

	require.NoError(t, err)
	select {
	case <-forked:
	case <-time.After(5 * time.Second):
		t.Fatal("the fork lock was not given back when the probe ended")
	}
}

func TestBecomesFree_TrueAsSoonAsItIsFree(t *testing.T) {
	t.Parallel()
	calls := 0
	ok := BecomesFree(func() bool {
		calls++
		return calls == 3
	})

	assert.True(t, ok)
	assert.Equal(t, 3, calls)
}

func TestBecomesFree_FalseWhenItNeverIs(t *testing.T) {
	t.Parallel()
	calls := 0
	ok := BecomesFree(func() bool {
		calls++
		return false
	})

	assert.False(t, ok)
	assert.Equal(t, attempts, calls)
}
