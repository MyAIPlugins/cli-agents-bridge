package integration

// F-160 C6: tell, ask and reply wait for a short hold of a session lock, and give up
// with the facts and with nothing lost when the hold outlasts the wait. Real binary,
// real subprocesses: what is under test is the path a user takes, from the
// environment variable down to the files on disk.
//
// The holder is a descriptor of the TEST process on the lock file. For the kernel that
// is another open file description than the cab-bridge subprocess's, so the contention
// is the same one two processes would have; the release is under the test's control,
// which is what makes the timings below deterministic.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/fslock"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

const (
	lwLongWait   = "3000" // ms: longer than every hold the successful cases use
	lwShortWait  = "200"
	lwReplyWait  = "300"
	lwAliveAfter = 1500 * time.Millisecond
	lwRunBound   = 20 * time.Second
)

type lwPair struct {
	dataDir      string
	valID, escID string
}

func (p lwPair) state(elem ...string) string {
	return filepath.Join(append([]string{p.dataDir, config.LayoutDir}, elem...)...)
}

func (p lwPair) sessionPath(id string, elem ...string) string {
	return p.state(append([]string{"sessions", id}, elem...)...)
}

// env is the environment of a command run as session id, with the given wait.
func (p lwPair) env(id, waitMs string) []string {
	return dataDirEnv(p.dataDir, "CAB_SESSION_ID="+id, "CAB_SESSION_LOCK_WAIT_MS="+waitMs, "CAB_AUTO_GC_HOURS=0")
}

// lwRegister registers a VAL (the one who asks) and an ESC (the one who is asked) in
// the same project, so each sees the other by name.
func lwRegister(t *testing.T) lwPair {
	t.Helper()
	dataDir := t.TempDir()
	project := t.TempDir()
	reg := func(role, name string) string {
		out, errOut, exit := run(t, []string{"register", "--role=" + role, "--agent-name=" + name, "--project-path=" + project}, dataDirEnv(dataDir, "CAB_AUTO_GC_HOURS=0"))
		require.Equal(t, 0, exit, "register %s: %s", name, errOut)
		return mustJSONField(t, out, "sessionId")
	}
	return lwPair{dataDir: dataDir, valID: reg("val", "VAL-lw"), escID: reg("esc", "ESC-lw")}
}

// holdSessionLock takes the lock of session id and keeps it until the returned
// release is called (or the test ends).
func (p lwPair) holdSessionLock(t *testing.T, id string) (release func()) {
	t.Helper()
	rel, err := fslock.TryLock(p.sessionPath(id, session.LockFileName))
	require.NoError(t, err, "premise: nobody holds the lock of %s yet", id)
	var once sync.Once
	release = func() { once.Do(func() { require.NoError(t, rel()) }) }
	t.Cleanup(release)
	return release
}

// jsonFiles lists the *.json files in dir, sorted; a missing dir is an empty list.
func jsonFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

func lwReadJSON(t *testing.T, path string, into any) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, into), "%s", path)
}

// lwBuffer collects a subprocess's output. exec copies it from its own goroutine while
// the test may look at it (to say what a still-running command printed so far), and a
// plain bytes.Buffer is a data race under -race.
type lwBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lwBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lwBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// hasMessage reports whether files holds the message id, however the directory spells
// it: processed/ prefixes the name with the time it was archived.
func hasMessage(files []string, id string) bool {
	for _, f := range files {
		if strings.HasSuffix(f, id+".json") {
			return true
		}
	}
	return false
}

// liveCmd is a cab-bridge process that is allowed to still be running.
type liveCmd struct {
	done     chan struct{}
	stdout   lwBuffer
	stderr   lwBuffer
	exit     int
	started  time.Time
	finished time.Time
}

func startCab(t *testing.T, env []string, args ...string) *liveCmd {
	t.Helper()
	return startBinary(t, buildBinary(t), env, args...)
}

// startBinary starts bin (this tree's binary, or an older one) and returns at once.
func startBinary(t *testing.T, bin string, env []string, args ...string) *liveCmd {
	t.Helper()
	c := &liveCmd{done: make(chan struct{}), started: time.Now()}
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = &c.stdout
	cmd.Stderr = &c.stderr
	require.NoError(t, cmd.Start())
	go func() {
		err := cmd.Wait()
		c.finished = time.Now()
		if exitErr, ok := err.(*exec.ExitError); ok {
			c.exit = exitErr.ExitCode()
		} else if err != nil {
			c.exit = -1
		}
		close(c.done)
	}()
	t.Cleanup(func() {
		select {
		case <-c.done:
		default:
			_ = cmd.Process.Kill()
			<-c.done
		}
	})
	return c
}

func (c *liveCmd) running() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

func (c *liveCmd) wait(t *testing.T) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(lwRunBound):
		t.Fatalf("the command is still running after %s; stderr so far: %s", lwRunBound, c.stderr.String())
	}
}

// ---------------------------------------------------------------- tell and ask

func TestLockWait_TellAndAskWaitForAShortHold(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"tell", "ask"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			p := lwRegister(t)
			inbox := p.sessionPath(p.escID, "inbox")
			outbox := p.sessionPath(p.valID, "outbox")

			t.Run("a hold that ends in time is waited for", func(t *testing.T) {
				release := p.holdSessionLock(t, p.escID)
				c := startCab(t, p.env(p.valID, lwLongWait), verb, "ESC-lw", "hello "+verb)

				time.Sleep(lwAliveAfter)
				// The only case where "still alive" is claimed: the wait is longer than
				// the time the lock has been held, so the command is still waiting.
				require.True(t, c.running(), "after %s the command is still waiting for the lock; stderr: %s", lwAliveAfter, c.stderr.String())
				assert.Empty(t, jsonFiles(t, inbox), "and nothing has been delivered yet")
				assert.Empty(t, jsonFiles(t, outbox), "nor recorded as sent")

				release()
				c.wait(t)
				require.Equal(t, 0, c.exit, "stderr: %s", c.stderr.String())
				assert.GreaterOrEqual(t, c.finished.Sub(c.started), lwAliveAfter, "it did not finish before the hold ended")
				files := jsonFiles(t, inbox)
				require.Len(t, files, 1, "exactly one message arrived")
				body, err := os.ReadFile(filepath.Join(inbox, files[0]))
				require.NoError(t, err)
				assert.Contains(t, string(body), "hello "+verb)
			})

			t.Run("a hold longer than the wait is an error that says so and loses nothing", func(t *testing.T) {
				// A fresh pair: this subtest is independent of the one above.
				q := lwRegister(t)
				release := q.holdSessionLock(t, q.escID)

				_, errOut, exit := run(t, []string{verb, "ESC-lw", "too late"}, q.env(q.valID, lwShortWait))

				assert.Equal(t, 1, exit, "stderr: %s", errOut)
				t.Logf("stderr of %s with the lock held past the wait: %s", verb, strings.TrimSpace(errOut))
				assert.Contains(t, errOut, filepath.Join("sessions", q.escID, session.LockFileName), "it names the lock file")
				assert.Contains(t, errOut, "still held after waiting 200ms", "and how long it waited")
				assert.NotContains(t, errOut, "--", "and it names no flag to try")
				assert.Empty(t, jsonFiles(t, q.sessionPath(q.escID, "inbox")), "nothing was delivered")
				assert.Empty(t, jsonFiles(t, q.sessionPath(q.valID, "outbox")), "and nothing was recorded as sent")

				release()
				_, errOut, exit = run(t, []string{verb, "ESC-lw", "second try"}, q.env(q.valID, lwShortWait))
				require.Equal(t, 0, exit, "after the release the same command works: %s", errOut)
				assert.Len(t, jsonFiles(t, q.sessionPath(q.escID, "inbox")), 1, "one message, the one of the second try")
			})

			t.Run("a wait of zero gives up at once", func(t *testing.T) {
				q := lwRegister(t)
				q.holdSessionLock(t, q.escID)

				start := time.Now()
				_, errOut, exit := run(t, []string{verb, "ESC-lw", "no waiting"}, q.env(q.valID, "0"))

				assert.Equal(t, 1, exit, "stderr: %s", errOut)
				assert.Less(t, time.Since(start), 2*time.Second, "a zero wait is one attempt")
				assert.Contains(t, errOut, "still held after waiting 0s")
				assert.Empty(t, jsonFiles(t, q.sessionPath(q.escID, "inbox")))
			})
		})
	}
}

// ----------------------------------------------------------------------- reply
//
// reply touches two locks, one after the other and never both: the replier's own
// while the journal is written (beginReply), and the asker's while the response is
// delivered (deliverResponse). A hold of either one, longer than the wait, must leave
// the exchange in a state a second reply completes without sending anything twice.

// lwAsked sets up an ask that ESC has been shown: VAL asks, ESC runs next (which marks
// it NOTIFIED, the state reply works from). It returns the pair and the id of the ask.
func lwAsked(t *testing.T) (lwPair, string) {
	t.Helper()
	p := lwRegister(t)
	_, errOut, exit := run(t, []string{"ask", "ESC-lw", "the question"}, p.env(p.valID, lwLongWait))
	require.Equal(t, 0, exit, "ask: %s", errOut)
	asks := jsonFiles(t, p.sessionPath(p.escID, "inbox"))
	require.Len(t, asks, 1, "premise: the ask is in the inbox of ESC")
	askID := strings.TrimSuffix(asks[0], ".json")

	out, errOut, exit := run(t, []string{"next"}, p.env(p.escID, lwLongWait))
	require.Equal(t, 0, exit, "next: %s", errOut)
	require.Contains(t, out, askID, "premise: next showed the ask")
	return p, askID
}

// assertReplyCompleted is the end state of a reply that went through, whichever way
// it got there: one response in the inbox of VAL under the deterministic id, the ask
// archived by ESC, no journal.
func assertReplyCompleted(t *testing.T, p lwPair, askID string) {
	t.Helper()
	responseID := session.DeterministicResponseID(p.escID, askID)
	assert.Equal(t, []string{responseID + ".json"}, jsonFiles(t, p.sessionPath(p.valID, "inbox")), "exactly one response, under the id derived from (replier, ask)")
	assert.True(t, hasMessage(jsonFiles(t, p.sessionPath(p.escID, "processed")), askID), "the ask was archived")
	assert.False(t, hasMessage(jsonFiles(t, p.sessionPath(p.escID, "inbox")), askID), "and is no longer in the inbox")
	assert.NoFileExists(t, p.sessionPath(p.escID, "reply-txn.json"), "the journal is gone")
}

func TestLockWait_Reply(t *testing.T) {
	t.Parallel()

	// Case A: the replier's own lock is held when the journal has to be written.
	t.Run("A: the replier's lock held longer than the wait", func(t *testing.T) {
		t.Parallel()
		p, askID := lwAsked(t)
		release := p.holdSessionLock(t, p.escID)

		_, errOut, exit := run(t, []string{"reply", "the answer"}, p.env(p.escID, lwReplyWait))

		assert.Equal(t, 1, exit, "stderr: %s", errOut)
		assert.Contains(t, errOut, "still held after waiting 300ms")
		assert.NoFileExists(t, p.sessionPath(p.escID, "reply-txn.json"), "no journal was written: the exchange never started")
		assert.Equal(t, []string{askID + ".json"}, jsonFiles(t, p.sessionPath(p.escID, "inbox")), "the ask is where it was")
		assert.Empty(t, jsonFiles(t, p.sessionPath(p.valID, "inbox")), "and nothing reached the asker")

		release()
		_, errOut, exit = run(t, []string{"reply", "the answer"}, p.env(p.escID, lwReplyWait))
		require.Equal(t, 0, exit, "after the release: %s", errOut)
		assertReplyCompleted(t, p, askID)
	})

	// Case B: the asker's lock is held when the response has to be delivered. The
	// journal exists by then, frozen, and a second ask arrives while the first reply
	// is stuck: the retry must finish the first and leave the second open.
	t.Run("B: the asker's lock held longer than the wait", func(t *testing.T) {
		t.Parallel()
		p, askID := lwAsked(t)
		release := p.holdSessionLock(t, p.valID)

		_, errOut, exit := run(t, []string{"reply", "the answer"}, p.env(p.escID, lwReplyWait))

		assert.Equal(t, 1, exit, "stderr: %s", errOut)
		assert.Contains(t, errOut, "still held after waiting 300ms")
		var journal struct {
			ResponseID string   `json:"responseId"`
			CloseIDs   []string `json:"closeIds"`
			State      string   `json:"state"`
		}
		lwReadJSON(t, p.sessionPath(p.escID, "reply-txn.json"), &journal)
		assert.Equal(t, session.ReplyTxnPending, journal.State, "the journal is pending: nothing was delivered")
		assert.Equal(t, session.DeterministicResponseID(p.escID, askID), journal.ResponseID, "and it recorded the response id")
		assert.Equal(t, []string{askID}, journal.CloseIDs, "and the one ask it will close")
		assert.Empty(t, jsonFiles(t, p.sessionPath(p.valID, "inbox")), "the asker has received nothing")

		// A second ask arrives after the journal was frozen.
		_, errOut, exit = run(t, []string{"ask", "ESC-lw", "the second question"}, p.env(p.valID, lwLongWait))
		require.Equal(t, 0, exit, "the second ask: %s", errOut)
		require.Len(t, jsonFiles(t, p.sessionPath(p.escID, "inbox")), 2, "premise: both asks are in the inbox")

		release()
		out, errOut, exit := run(t, []string{"reply", "the answer"}, p.env(p.escID, lwReplyWait))
		require.Equal(t, 0, exit, "after the release: %s", errOut)

		responseID := session.DeterministicResponseID(p.escID, askID)
		assert.Equal(t, []string{responseID + ".json"}, jsonFiles(t, p.sessionPath(p.valID, "inbox")), "the SAME response id, delivered once")
		archived := jsonFiles(t, p.sessionPath(p.escID, "processed"))
		require.Len(t, archived, 1, "only the frozen ask was archived")
		assert.True(t, hasMessage(archived, askID))
		remaining := jsonFiles(t, p.sessionPath(p.escID, "inbox"))
		require.Len(t, remaining, 1, "the second ask is still open")
		assert.False(t, hasMessage(remaining, askID))
		// The reply does not name the id of what it left open (F-109): it counts it, and
		// says that this reply closed none of it.
		assert.Contains(t, out+errOut, "1 message(s) arrived that you have not seen yet", "and the reply says so")
		assert.Contains(t, out+errOut, "none was closed by this reply")
		assert.NoFileExists(t, p.sessionPath(p.escID, "reply-txn.json"), "the journal is gone")
	})

	// Case C: the hold ends inside the wait. The reply goes through at the first
	// attempt, with the same final state as above.
	for _, tc := range []struct {
		name string
		who  func(lwPair) string
	}{
		{"C: the replier's lock released inside the wait", func(p lwPair) string { return p.escID }},
		{"C: the asker's lock released inside the wait", func(p lwPair) string { return p.valID }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, askID := lwAsked(t)
			release := p.holdSessionLock(t, tc.who(p))
			c := startCab(t, p.env(p.escID, lwLongWait), "reply", "the answer")
			const holdFor = 300 * time.Millisecond
			time.Sleep(holdFor)
			release()
			c.wait(t)
			require.Equal(t, 0, c.exit, "stderr: %s", c.stderr.String())
			// The command was started before the hold began and the lock was released
			// holdFor later, so it cannot have finished sooner unless it never needed
			// the lock: this is what makes the case a wait and not a plain reply.
			assert.GreaterOrEqual(t, c.finished.Sub(c.started), holdFor, "it waited for the lock")
			assertReplyCompleted(t, p, askID)
		})
	}
}
