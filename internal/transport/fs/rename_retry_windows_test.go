//go:build windows

package fs

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// The wiring of the atomic replace to the common retry loop, run on the body production
// runs. renameRetrying takes the attempt as an argument, so each test scripts the stage and
// the error of every attempt and no Windows syscall is involved: what is under test is what
// the body DECIDES with what it gets, which rename_windows_test.go cannot reach because it
// only has the errors the real filesystem happens to produce.
//
// Three outcomes of the body are fixed here, with their full text and not with a word that
// a poorer text would also contain, because rename_windows_test.go asserts only that the
// exhausted-budget message contains "attempts" and has no case at all for INVALID_PARAMETER:
//
//   - the budget runs out: the last error, the real elapsed time, the list of possible causes;
//   - the replace stage answers INVALID_PARAMETER: the requirement, at the attempt it turned up;
//   - anything else: the error as it came, with no diagnosis.
//
// The wording is repeated below as literals ON PURPOSE. A test that built the expectation from
// the production text would pass with any text.

const (
	rrOld = `C:\bridge\inbox\msg.tmp`
	rrNew = `C:\bridge\inbox\msg.json`

	rrInvalidParameterText = "atomic replace refused by SetFileInformationByHandle(FileRenameInfoEx): " +
		"POSIX rename semantics are a precondition of this bridge and something here does not provide them — " +
		"they need Windows 10 1709 / Server 2019 or later AND a filesystem that implements them " +
		"(NTFS does; SMB shares and FAT typically do not). Not a transient failure: "

	// %s is the time really spent, the only part of the text that varies.
	rrStillRefusedText = "still refused after 6 attempts over %s, and the cause is NOT established here: " +
		"another process may be holding one of the two files open without sharing deletion " +
		"(antivirus, editor, an open shell), or it may be permissions or a file attribute — " +
		"a read-only target produces this same error with nobody else involved: "
)

// rrWaits is the backoff the budget asks for, as literals: computing it from the constants
// would let the constants and the expectation drift together.
var rrWaits = []time.Duration{
	10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond, 160 * time.Millisecond,
}

type rrStep struct {
	stage renameStage
	err   error
}

// rrScript replays the steps in order; an attempt nobody scripted is a failure of the test.
func rrScript(t *testing.T, steps []rrStep) func() (renameStage, error) {
	t.Helper()
	next := 0
	return func() (renameStage, error) {
		require.Less(t, next, len(steps), "the body asked for an attempt nobody scripted")
		s := steps[next]
		next++
		return s.stage, s.err
	}
}

// rrRecordWaits replaces renameSleep with a recorder. pause is how long each wait REALLY lasts:
// zero for every test but the one that checks the time the message reports, so that the others
// do not wait for real time.
func rrRecordWaits(t *testing.T, pause time.Duration) *[]time.Duration {
	t.Helper()
	original := renameSleep
	waits := []time.Duration{} // never nil: an empty run must compare equal to an empty expectation
	renameSleep = func(d time.Duration) {
		waits = append(waits, d)
		time.Sleep(pause)
	}
	t.Cleanup(func() { renameSleep = original })
	return &waits
}

// rrTransient is the i-th refusal of a run that never settles: the stage alternates and so
// does the error, because the budget is shared by both stages and by both errors.
func rrTransient(i int) rrStep {
	if i%2 == 0 {
		return rrStep{stageOpen, fmt.Errorf("attempt %d: %w", i+1, windows.ERROR_ACCESS_DENIED)}
	}
	return rrStep{stageReplace, fmt.Errorf("attempt %d: %w", i+1, windows.ERROR_SHARING_VIOLATION)}
}

// rrAfter returns k-1 transient refusals followed by last: the outcome arrives at attempt k.
func rrAfter(k int, last rrStep) []rrStep {
	steps := make([]rrStep, 0, k)
	for i := 0; i < k-1; i++ {
		steps = append(steps, rrTransient(i))
	}
	return append(steps, last)
}

// rrFailures counts what the trace must say: failures per stage over the scripted steps.
func rrFailures(steps []rrStep) (open, replace int) {
	for _, s := range steps {
		switch {
		case s.err == nil:
		case s.stage == stageOpen:
			open++
		default:
			replace++
		}
	}
	return open, replace
}

func rrRun(t *testing.T, steps []rrStep) (renameTrace, []time.Duration, error) {
	t.Helper()
	return rrRunPausing(t, steps, 0)
}

func rrRunPausing(t *testing.T, steps []rrStep, pause time.Duration) (renameTrace, []time.Duration, error) {
	t.Helper()
	waits := rrRecordWaits(t, pause)
	trace, err := renameRetrying(rrOld, rrNew, rrScript(t, steps))
	return trace, *waits, err
}

// rrAssertLinkError checks the shape os.Rename's error has and every caller relies on.
func rrAssertLinkError(t *testing.T, err error, want string) {
	t.Helper()
	var le *os.LinkError
	require.ErrorAs(t, err, &le, "the error must be an *os.LinkError, as os.Rename's is")
	assert.Equal(t, "rename", le.Op)
	assert.Equal(t, rrOld, le.Old)
	assert.Equal(t, rrNew, le.New)
	assert.Equal(t, "rename "+rrOld+" "+rrNew+": "+want, err.Error())
}

// TestRenameRetrying_InvalidParameterAtTheReplaceStageIsRefusedWhereItTurnsUp: whatever came
// before (nothing, or up to five refusals of either kind), the replace stage answering
// INVALID_PARAMETER ends the operation at that attempt, with no wait after it, and says what
// is REQUIRED without claiming to know the cause.
func TestRenameRetrying_InvalidParameterAtTheReplaceStageIsRefusedWhereItTurnsUp(t *testing.T) {
	for k := 1; k <= 6; k++ {
		t.Run(fmt.Sprintf("attempt %d", k), func(t *testing.T) {
			steps := rrAfter(k, rrStep{stageReplace, windows.ERROR_INVALID_PARAMETER})

			trace, waits, err := rrRun(t, steps)

			require.Error(t, err)
			rrAssertLinkError(t, err, rrInvalidParameterText+windows.ERROR_INVALID_PARAMETER.Error())
			assert.ErrorIs(t, err, windows.ERROR_INVALID_PARAMETER, "the real cause stays reachable")
			assert.NotContains(t, err.Error(), "still refused", "it is not an exhausted budget")
			open, replace := rrFailures(steps)
			assert.Equal(t, renameTrace{attempts: k, waits: k - 1, openFails: open, replaceFails: replace}, trace)
			assert.Equal(t, rrWaits[:k-1], waits, "no wait after the attempt that was refused for good")
		})
	}
}

// TestRenameRetrying_InvalidParameterAtTheOpenStageIsAnOrdinaryError is the branch next door:
// the same code from the open stage says nothing about POSIX semantics, because only the
// replace can tell that the info class was refused.
func TestRenameRetrying_InvalidParameterAtTheOpenStageIsAnOrdinaryError(t *testing.T) {
	for k := 1; k <= 6; k++ {
		t.Run(fmt.Sprintf("attempt %d", k), func(t *testing.T) {
			steps := rrAfter(k, rrStep{stageOpen, windows.ERROR_INVALID_PARAMETER})

			trace, waits, err := rrRun(t, steps)

			require.Error(t, err)
			rrAssertLinkError(t, err, windows.ERROR_INVALID_PARAMETER.Error())
			assert.ErrorIs(t, err, windows.ERROR_INVALID_PARAMETER)
			assert.NotContains(t, err.Error(), "POSIX")
			open, replace := rrFailures(steps)
			assert.Equal(t, renameTrace{attempts: k, waits: k - 1, openFails: open, replaceFails: replace}, trace)
			assert.Equal(t, rrWaits[:k-1], waits)
		})
	}
}

// rrPause is how long each of the five waits really lasts in the test of the exhausted budget.
const rrPause = 2 * time.Millisecond

var rrDuration = regexp.MustCompile(`over ([^ ,]+), and the cause`)

// TestRenameRetrying_ARefusalThatNeverEndsSaysSoWithTheWholeText: six attempts, five waits of
// 10, 20, 40, 80 and 160 ms, the LAST error reachable, and the complete message. The elapsed
// time is the only normalised part. It is a real measurement (the five waits really last 2 ms
// each here, so the message cannot say less than 10 ms) and not a value that can be pinned.
func TestRenameRetrying_ARefusalThatNeverEndsSaysSoWithTheWholeText(t *testing.T) {
	cases := []struct {
		name  string
		steps func() []rrStep
	}{
		{"both stages, alternating", func() []rrStep {
			var s []rrStep
			for i := 0; i < 6; i++ {
				s = append(s, rrTransient(i))
			}
			return s
		}},
		{"the source open every time", func() []rrStep {
			var s []rrStep
			for i := 0; i < 6; i++ {
				s = append(s, rrStep{stageOpen, fmt.Errorf("attempt %d: %w", i+1, windows.ERROR_SHARING_VIOLATION)})
			}
			return s
		}},
		{"the replace every time", func() []rrStep {
			var s []rrStep
			for i := 0; i < 6; i++ {
				s = append(s, rrStep{stageReplace, fmt.Errorf("attempt %d: %w", i+1, windows.ERROR_ACCESS_DENIED)})
			}
			return s
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			steps := c.steps()

			trace, waits, err := rrRunPausing(t, steps, rrPause)

			require.Error(t, err)
			m := rrDuration.FindStringSubmatch(err.Error())
			require.Len(t, m, 2, "the message must carry the time really spent: %q", err.Error())
			spent, perr := time.ParseDuration(m[1])
			require.NoError(t, perr, "and it must be a duration")
			assert.GreaterOrEqual(t, spent, 5*rrPause, "the time reported is the time spent, from before the first attempt")

			lastErr := steps[5].err
			want := fmt.Sprintf(rrStillRefusedText, "<elapsed>") + lastErr.Error()
			got := rrDuration.ReplaceAllString(err.Error(), "over <elapsed>, and the cause")
			assert.Equal(t, "rename "+rrOld+" "+rrNew+": "+want, got)
			assert.ErrorIs(t, err, lastErr, "the LAST error of the six, not the first")
			assert.NotContains(t, err.Error(), "attempt 5:", "no earlier error is carried along")

			open, replace := rrFailures(steps)
			assert.Equal(t, renameTrace{attempts: 6, waits: 5, openFails: open, replaceFails: replace}, trace)
			assert.Equal(t, rrWaits, waits)
		})
	}
}

// TestRenameRetrying_AnErrorNotWorthRepeatingComesBackAtOnceAndUntouched: a different volume,
// a missing file, at the first attempt or after refusals, at either stage, and also at the
// SIXTH: failing for good on the last attempt is not "still refused after the whole budget".
func TestRenameRetrying_AnErrorNotWorthRepeatingComesBackAtOnceAndUntouched(t *testing.T) {
	for _, k := range []int{1, 3, 6} {
		for _, stage := range []renameStage{stageOpen, stageReplace} {
			for _, errno := range []error{windows.ERROR_NOT_SAME_DEVICE, windows.ERROR_FILE_NOT_FOUND} {
				t.Run(fmt.Sprintf("attempt %d stage %d %v", k, stage, errno), func(t *testing.T) {
					injected := fmt.Errorf("probe: %w", errno)
					steps := rrAfter(k, rrStep{stage, injected})

					trace, waits, err := rrRun(t, steps)

					require.Error(t, err)
					rrAssertLinkError(t, err, injected.Error())
					var le *os.LinkError
					require.ErrorAs(t, err, &le)
					assert.Same(t, injected, le.Err, "the very error, not a copy or a wrapper")
					assert.NotContains(t, err.Error(), "still refused")
					assert.NotContains(t, err.Error(), "POSIX")
					open, replace := rrFailures(steps)
					assert.Equal(t, renameTrace{attempts: k, waits: k - 1, openFails: open, replaceFails: replace}, trace)
					assert.Equal(t, rrWaits[:k-1], waits)
				})
			}
		}
	}
}

// TestRenameRetrying_StopsAtTheFirstSuccessAndCostsNothingWhenThereIsNoRefusal: the normal case
// has no wait, and a success after k-1 refusals has exactly k-1 of them.
func TestRenameRetrying_StopsAtTheFirstSuccessAndCostsNothingWhenThereIsNoRefusal(t *testing.T) {
	for k := 1; k <= 6; k++ {
		t.Run(fmt.Sprintf("attempt %d", k), func(t *testing.T) {
			steps := rrAfter(k, rrStep{stageReplace, nil})

			trace, waits, err := rrRun(t, steps)

			require.NoError(t, err)
			open, replace := rrFailures(steps)
			assert.Equal(t, renameTrace{attempts: k, waits: k - 1, openFails: open, replaceFails: replace}, trace)
			assert.Equal(t, rrWaits[:k-1], waits)
		})
	}
}

// TestRenameAtomicTraced_ADestinationNameThatCannotBeEncodedFailsBeforeAnyAttempt is the head of
// the real function: the buffer for the replace is built first, and a name that cannot be
// encoded must come back in os.Rename's shape with no attempt made and no wait.
func TestRenameAtomicTraced_ADestinationNameThatCannotBeEncodedFailsBeforeAnyAttempt(t *testing.T) {
	waits := rrRecordWaits(t, 0)
	bad := "C:\\bridge\\inbox\\msg\x00.json"

	trace, err := renameAtomicTraced(rrOld, bad)

	var le *os.LinkError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, "rename", le.Op)
	assert.Equal(t, rrOld, le.Old)
	assert.Equal(t, bad, le.New)
	assert.ErrorIs(t, err, syscall.EINVAL)
	assert.Equal(t, renameTrace{}, trace, "no attempt, no wait")
	assert.Empty(t, *waits)
	assert.False(t, errors.Is(err, os.ErrNotExist), "and it is not mistaken for a missing file")
}
