package cleanup

// F-160, CRI diff-gate blocker: a manifest that comes back after the retirement.
//
// The retirement rests on "the manifest is gone for good": a process that opened the
// lock file just before it was unlinked locks an inode nobody contends for, finds the
// manifest missing and refuses (session.ErrSessionGone). Several writers loaded the
// manifest, changed one field and saved it with only an in-process mutex, no session
// lock. One of them in flight across the retirement brought the manifest back, and
// then the retirement could not remove the directory, the lock file was recreated by
// the next arrival, and two processes held "the" lock of one session on two inodes.
//
// This test is the race with real processes. The child is a SetState stopped, by the
// public clock of the Manager, exactly between its load and its save of the manifest.
// The parent retires the session meanwhile. Either outcome the rule allows is
// accepted, and only these two: the writer finished BEFORE the retirement (cleanup
// finds the session busy and leaves it alone), or the writer is refused AFTER it.
// The forbidden one is the writer saving after the retirement.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

const (
	stallEnv        = "CAB_CLEANUP_TEST_STALLED_SETSTATE"
	stallEnvData    = stallEnv + "_DATA"
	stallEnvSID     = stallEnv + "_SID"
	stallEnvMarkers = stallEnv + "_MARKERS"
	stallEnvStall   = stallEnv + "_STALL"
	stallBound      = 20 * time.Second // every wait of this file has this limit
	stallPoll       = 10 * time.Millisecond
)

func waitForFile(path string, bound time.Duration) bool {
	deadline := time.Now().Add(bound)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(stallPoll)
	}
	return false
}

// TestHelperProcess_StalledSetState is the child process, not a test: without its
// environment variable it returns at once. It runs SetState on the session and, when
// asked to stall, stops inside the Manager's clock, which SetState reads between the
// load and the save of the manifest, until the parent lets it go.
func TestHelperProcess_StalledSetState(t *testing.T) {
	if os.Getenv(stallEnv) != "1" {
		return
	}
	markers := os.Getenv(stallEnvMarkers)
	mgr := session.NewManager(os.Getenv(stallEnvData), time.Second)
	if os.Getenv(stallEnvStall) == "1" {
		var once sync.Once
		mgr.Now = func() time.Time {
			once.Do(func() {
				if err := os.WriteFile(filepath.Join(markers, "loaded"), nil, 0o600); err != nil {
					fmt.Fprintf(os.Stderr, "child: cannot write the loaded marker: %v\n", err)
					os.Exit(2)
				}
				if !waitForFile(filepath.Join(markers, "go"), stallBound) {
					fmt.Fprintln(os.Stderr, "child: the parent never let me go")
					os.Exit(3)
				}
			})
			return time.Now().UTC()
		}
	}
	result := "ok"
	if err := mgr.SetState(os.Getenv(stallEnvSID), session.StateWorking); err != nil {
		result = "error: " + err.Error()
	}
	if err := os.WriteFile(filepath.Join(markers, "result"), []byte(result), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "child: cannot write the result: %v\n", err)
		os.Exit(2)
	}
	os.Exit(0)
}

// stderrBuf collects what the child says on stderr; exec copies it from its own
// goroutine while the test may read it.
type stderrBuf struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *stderrBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *stderrBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// helperChild is a child process of this test binary, driven through files in a
// markers directory: it says "loaded" when it is in position, waits for "go", and
// leaves "result". Every wait on either side has a limit.
type helperChild struct {
	markers string
	done    chan struct{}
	stderr  *stderrBuf
}

func startSetStateChild(t *testing.T, dataDir, sid string, stall bool) *helperChild {
	t.Helper()
	stallFlag := "0"
	if stall {
		stallFlag = "1"
	}
	return startHelperChild(t, "TestHelperProcess_StalledSetState", func(markers string) []string {
		return []string{stallEnv + "=1", stallEnvData + "=" + dataDir, stallEnvSID + "=" + sid,
			stallEnvMarkers + "=" + markers, stallEnvStall + "=" + stallFlag}
	})
}

func startHelperChild(t *testing.T, helperTest string, env func(markers string) []string) *helperChild {
	t.Helper()
	child := &helperChild{markers: t.TempDir(), done: make(chan struct{}), stderr: &stderrBuf{}}
	cmd := exec.Command(os.Args[0], "-test.run=^"+helperTest+"$")
	cmd.Env = append(os.Environ(), env(child.markers)...)
	cmd.Stderr = child.stderr
	require.NoError(t, cmd.Start())
	go func() {
		_ = cmd.Wait()
		close(child.done)
	}()
	t.Cleanup(func() {
		select {
		case <-child.done:
		default:
			_ = cmd.Process.Kill()
			<-child.done
		}
	})
	return child
}

func (c *helperChild) waitLoaded() bool {
	return waitForFile(filepath.Join(c.markers, "loaded"), stallBound)
}

func (c *helperChild) letGo(t *testing.T) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(c.markers, "go"), nil, 0o600))
}

func (c *helperChild) running() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

func (c *helperChild) waitExit(t *testing.T) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(stallBound):
		t.Fatalf("the child did not exit within %v; stderr: %s", stallBound, c.stderr.String())
	}
}

func (c *helperChild) result(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(c.markers, "result"))
	require.NoError(t, err, "the child left no result; stderr: %s", c.stderr.String())
	return string(raw)
}

func fixedNow() time.Time { return failuresBase }

func TestRetirement_ASetStateInFlightIsNeverFollowedByAResurrectedManifest(t *testing.T) {
	const id = "aaaaaa01"
	dataDir := t.TempDir()
	plantStale(t, dataDir, id)
	manifest := sessionPath(dataDir, id, "manifest.json")

	child := startSetStateChild(t, dataDir, id, true)
	require.True(t, child.waitLoaded(), "premise: the child is between the load and the save of its SetState; stderr: %s", child.stderr.String())
	require.True(t, child.running(), "premise: and it is still running")
	require.FileExists(t, manifest, "premise: the manifest is there while the child holds its copy")

	afterReleaseRan := false
	err := archiveAndRemoveSession(dataDir, id, fixedNow, func() {
		// Only reached if cleanup got the lock and retired the session with the
		// writer still in flight. Let the writer finish now, which is the order of
		// the defect: it saves AFTER the retirement.
		afterReleaseRan = true
		child.letGo(t)
		child.waitExit(t)
	})

	if afterReleaseRan {
		assert.NoFileExists(t, manifest,
			"the manifest came back after the retirement: a SetState that loaded it before saved it after (cleanup said: %v; the child said: %s)",
			err, child.result(t))
		return
	}

	// The allowed order: the writer was in flight, so cleanup found the session busy
	// and left it whole.
	require.ErrorIs(t, err, ErrSessionBusy, "a writer in flight holds the session lock, so cleanup must find it busy")
	assert.FileExists(t, manifest, "and nothing of the session was touched")
	child.letGo(t)
	child.waitExit(t)
	assert.Equal(t, "ok", child.result(t), "the writer finished its write")
	loaded, lerr := session.NewManager(dataDir, time.Second).LoadManifest(id)
	require.NoError(t, lerr)
	assert.Equal(t, session.StateWorking, loaded.State, "and its write is on disk")

	// With nobody in flight the retirement goes through, and leaves nothing.
	require.NoError(t, archiveAndRemoveSession(dataDir, id, fixedNow, nil))
	assert.NoDirExists(t, sessionPath(dataDir, id))
}

// The other order of the same race: the retirement is done and only the lock file and
// the directory are left to remove when the writer arrives. It is refused and creates
// nothing, so the removal that follows finds an empty directory.
func TestRetirement_ASetStateAfterTheRetirementIsRefusedAndCreatesNothing(t *testing.T) {
	const id = "aaaaaa02"
	dataDir := t.TempDir()
	plantStale(t, dataDir, id)

	var child *helperChild
	err := archiveAndRemoveSession(dataDir, id, fixedNow, func() {
		child = startSetStateChild(t, dataDir, id, false)
		child.waitExit(t)
	})

	require.NoError(t, err, "the writer left nothing behind, so the directory goes")
	require.NotNil(t, child, "premise: the seam ran")
	assert.True(t, strings.HasPrefix(child.result(t), "error:"), "the writer was refused: %q", child.result(t))
	assert.NoDirExists(t, sessionPath(dataDir, id), "and nothing was recreated")
}
