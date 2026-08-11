package main

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureStderr survived the v0.8 removal of the commands whose tests used to
// define it; several suites still need it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	// GUARD: capturing means REPLACING a process-wide global, so this test must
	// not run beside another one. t.Setenv is refused by the runtime in a
	// parallel test — "testing: test using t.Setenv, t.Chdir ... can not use
	// t.Parallel" — which turns "do not do this" into "cannot be done", with a
	// message that says why. See mustNotRunInParallel.
	mustNotRunInParallel(t)
	old := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	defer func() { os.Stderr = old }()

	fn()
	require.NoError(t, w.Close())
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(data)
}

// mustNotRunInParallel makes it IMPOSSIBLE for a capturing test to run beside
// another one, instead of merely inadvisable.
//
// WHY IT IS NEEDED. The three capture helpers replace os.Stdout/os.Stderr —
// globals of package os — and production code READS them (85 sites for stderr
// alone). A capturing test running in parallel with any test that exercises
// production code is therefore a data race between a test write and a
// production read. TSan caught exactly that, with a full happens-before stack:
// captureStderr writing, runState reading via fs_.SetOutput(os.Stderr).
//
// Of 34 tests that capture, exactly ONE was parallel, and it was added the same
// day the race appeared. So this was never a rare window: there was no window
// until a t.Parallel() was put on a capturing test. One line reopened it, and
// one line closes it — which is precisely why a comment is not enough here.
//
// HOW. t.Setenv is refused by the testing runtime in a parallel test. Borrowing
// that refusal costs one line and gives a deterministic panic naming the
// conflict, rather than a race that shows up one run in three and gets blamed on
// whatever was being changed at the time.
//
// Called BEFORE the global is touched, so a refusal cannot leave a swapped
// os.Stderr behind.
//
// WHAT IT DOES NOT COVER, and it must be read as a limit rather than as a
// closed case: it guards the TEST as the writer. If production code under test
// spawns a goroutine that reads os.Stderr and outlives the test's return, that
// read races with the next test's capture, and nothing here notices — the test
// was sequential. Nobody has looked for such a goroutine; this comment is not
// evidence that none exists.
func mustNotRunInParallel(t *testing.T) {
	t.Helper()
	t.Setenv("CAB_TEST_CAPTURES_GLOBAL_STREAMS", "1")
}

// TestCaptureGuard_AParallelCapturingTestCannotExist is the regression, and it
// has an unusual shape because the defect does.
//
// The normal proof is "red before the fix, green after". That is not available
// here: the race showed up roughly one run in three, and nine consecutive runs
// on this machine never reproduced it. A test that fails one time in three is
// not a regression test, it is a coin.
//
// So the proof is the other one the fix affords: THE CASE IS NO LONGER
// EXPRESSIBLE. A capturing test that asks for t.Parallel() does not race — it
// panics, immediately, every time, with the runtime naming the conflict. That
// IS deterministic, and this pins it.
func TestCaptureGuard_AParallelCapturingTestCannotExist(t *testing.T) {
	for _, tc := range []struct {
		name    string
		capture func(*testing.T)
	}{
		{"captureStderr", func(t *testing.T) { captureStderr(t, func() {}) }},
		{"captureStdout", func(t *testing.T) { captureStdout(t, func() {}) }},
		{"captureStdoutStderr", func(t *testing.T) { captureStdoutStderr(t, func() {}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel() // exactly what must become impossible

			// Taken BEFORE the attempt, so the comparison after it means
			// something. The first version of this line compared os.Stderr with
			// itself — an assertion that cannot fail, in a file about assertions
			// that cannot fail.
			beforeOut, beforeErr := os.Stdout, os.Stderr

			var panicked any
			func() {
				defer func() { panicked = recover() }()
				tc.capture(t)
			}()

			require.NotNil(t, panicked, "%s from a parallel test must panic, not race", tc.name)
			assert.Contains(t, panicked.(string), "can not use t.Parallel",
				"and the runtime must be the one saying why, so the reader is not left guessing")

			// The refusal happens BEFORE the globals are swapped, so a rejected
			// capture cannot leave os.Stderr pointing at a pipe nobody will read
			// — which would break whatever runs next, far from here.
			assert.Same(t, beforeOut, os.Stdout, "a refused capture must not have swapped os.Stdout")
			assert.Same(t, beforeErr, os.Stderr, "nor os.Stderr")
		})
	}
}
