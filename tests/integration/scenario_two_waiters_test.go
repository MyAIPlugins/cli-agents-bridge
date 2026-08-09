package integration

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two real `next` processes on one session.
//
// This scenario lives here, and not in cmd/cab-bridge, because the guarantee it
// checks cannot be produced in-process: the session lock is deliberately
// RE-ENTRANT for the same PID (internal/session/lock.go), so two goroutines
// pretending to be two waiters both walk straight through the critical section
// that is supposed to separate them. An in-process version of this test passes
// or fails on scheduling luck — it did pass, for a while, on the extra
// milliseconds of two manifest writes that have since been removed — and what it
// exercises is not what a user runs.
//
// With two OS processes the exclusion is real, and so is the answer.
type waiter struct {
	cmd    *exec.Cmd
	stdout *syncBuffer
	stderr *syncBuffer
	done   chan error
}

// syncBuffer is a bytes.Buffer safe to READ while the subprocess is still
// writing to it. The race detector caught this on the failing run, which is the
// only run that reads a buffer early — a diagnostic that only appears on failure
// is exactly where an unsynchronised read hides.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startWaiter launches `next` as a real background process from dir.
func startWaiter(t *testing.T, dir, dataDir string) *waiter {
	t.Helper()
	cmd := exec.Command(buildBinary(t), "next")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), dataDirEnv(dataDir, "CAB_POLL_INTERVAL_MS=50")...)
	w := &waiter{cmd: cmd, stdout: &syncBuffer{}, stderr: &syncBuffer{}, done: make(chan error, 1)}
	cmd.Stdout = w.stdout
	cmd.Stderr = w.stderr
	require.NoError(t, cmd.Start())
	go func() { w.done <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	return w
}

// waitExit reports whether the waiter exited within d.
func (w *waiter) waitExit(d time.Duration) bool {
	select {
	case <-w.done:
		return true
	case <-time.After(d):
		return false
	}
}

// awaitListening polls overview until it reports a live waiter, and returns the
// listener line. Fails the test if it never does.
func awaitListening(t *testing.T, dir, dataDir, sid string, w *waiter) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		out, _, exit := runInDir(t, dir, []string{"overview", "--session-id=" + sid}, dataDirEnv(dataDir))
		if exit == 0 {
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(line, "listener:") {
					last = line
					if strings.Contains(line, "listening (PID") {
						return line
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("overview never reported a live waiter; last listener line: %q\nwaiter stderr: %s\nwaiter stdout: %s", last, w.stderr.String(), w.stdout.String())
	return ""
}

// TestTwoWaiters_EvictionLeavesTheLiveOneVisibleAndDeliversOnce covers the two
// halves of the same accident, in the order they happened to a real orchestrator.
//
// First half — observability. A second `next` evicts the first; the first then
// exits. Before the fix its teardown cleared the manifest marker the SECOND one
// had just published, so overview answered "not listening" about a session that
// was listening. The agent, following the rule to re-arm when not listening,
// then evicted its own healthy waiter — the command that answers "am I
// listening?" destroyed the thing it was asked about.
//
// Second half — delivery. With two waiters alive at once, a message must still
// reach exactly ONE of them: a page on screen has woken an agent, and a second
// copy means two agents acting on one brief.
func TestTwoWaiters_EvictionLeavesTheLiveOneVisibleAndDeliversOnce(t *testing.T) {
	dataDir := t.TempDir()
	dirs := sharedScopeDirs(t, "val", "esc")
	valDir, escDir := dirs[0], dirs[1]

	registerIn(t, dataDir, valDir, "val", "VAL-tw")
	escID := registerIn(t, dataDir, escDir, "esc", "ESC-tw")

	first := startWaiter(t, escDir, dataDir)
	firstLine := awaitListening(t, escDir, dataDir, escID, first)

	second := startWaiter(t, escDir, dataDir)
	require.True(t, first.waitExit(15*time.Second), "the first waiter must exit once evicted")
	assert.Contains(t, first.stderr.String(), "reclaimed", "and it must say why")

	// THE REGRESSION: asked AFTER the evicted one has exited and run its teardown.
	afterLine := awaitListening(t, escDir, dataDir, escID, second)
	assert.NotEqual(t, firstLine, afterLine, "the live waiter is the second one, not the first")

	// One message, two waiters alive between them: exactly one delivery.
	msgID := plantQuery(t, dataDir, escID, "valfake1", "val", "VAL-tw", "one brief")
	require.True(t, second.waitExit(15*time.Second), "the surviving waiter must deliver and exit")

	both := first.stdout.String() + second.stdout.String()
	assert.Equal(t, 1, strings.Count(both, `"id": "`+msgID+`"`),
		"a message must wake exactly one instance\nfirst:\n%s\nsecond:\n%s", first.stdout.String(), second.stdout.String())
	assert.Contains(t, second.stdout.String(), msgID, "and it is the one that still owns the wait")
}
