package findings

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Death WITHOUT cleanup, at three named points of `add`. The child is this test
// binary re-executed; at the point it leaves a marker and kills itself (Kill is
// SIGKILL on Unix and TerminateProcess on Windows: no defer runs, no release).
// The parent then checks what is on disk and runs an add of its own.
//
// What these do NOT prove, so as not to suggest it: TestKill_MidPublish uses a
// Publish that dies halfway, so it shows that Store changes the registry only
// through Publish and that a Publish interrupted before the rename leaves the
// registry as it was. It does not show that the rename of the real
// AtomicWriteBytes is atomic: that is the operating system's and atomic.go's, and
// its own tests cover it. TestKill_AfterPublishBeforeRelease uses the real
// AtomicWriteBytes.

const (
	helperKillPoint = "kill-point"

	pointAfterLock    = "after-lock-before-read"
	pointMidPublish   = "mid-publish"
	pointAfterPublish = "after-publish-before-release"

	// Bounds of the test, not promises of the product.
	parentLockAttempts  = 500 // the parent's add after the death: proves the lock frees, not that it frees inside the production budget
	survivedSleepBudget = 10 * time.Second
)

func TestKill_AfterLockBeforeRead(t *testing.T) {
	s, before := storeWithOneRow(t)
	killedAt(t, s, pointAfterLock)

	assert.Equal(t, before, readFile(t, s.RegistryPath()), "the child died holding the lock before reading: the registry is as it was")
	assertWhole(t, s, 1)
	assertParentCanAdd(t, s, before, 2)
}

func TestKill_MidPublish(t *testing.T) {
	s, before := storeWithOneRow(t)
	killedAt(t, s, pointMidPublish)

	assert.Equal(t, before, readFile(t, s.RegistryPath()), "an interrupted publish leaves the registry byte for byte as it was")
	assert.True(t, strings.HasSuffix(string(before), "\n"))
	assertWhole(t, s, 1)
	assert.FileExists(t, filepath.Join(s.Dir, ".tmp.partial"), "the half-written temporary file is what a kill leaves behind (declared residue)")
	assertParentCanAdd(t, s, before, 2)
}

func TestKill_AfterPublishBeforeRelease(t *testing.T) {
	s, before := storeWithOneRow(t)
	killedAt(t, s, pointAfterPublish)

	after := readFile(t, s.RegistryPath())
	require.True(t, strings.HasPrefix(string(after), string(before)), "the old rows are untouched")
	assertWhole(t, s, 2)
	assert.Contains(t, string(after), "\tchild-key\t", "the child's row is there: its publish had completed")
	assertParentCanAdd(t, s, after, 3)
}

func storeWithOneRow(t *testing.T) (*Store, []byte) {
	t.Helper()
	s, _ := newTestStore(t)
	mustAdd(t, s, inputFor("parent-one"))
	return s, readFile(t, s.RegistryPath())
}

// killedAt runs the child to the named point and checks that it got there and
// went no further: a marker before the death, none after.
func killedAt(t *testing.T, s *Store, point string) {
	t.Helper()
	markers := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess_KillPoint$")
	cmd.Env = append(os.Environ(),
		helperEnv+"="+helperKillPoint,
		helperEnv+"_POINT="+point,
		helperEnv+"_DIR="+s.Dir,
		helperEnv+"_MARKERS="+markers,
	)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	_ = cmd.Wait() // a killed process reports an error: that is the expected end
	require.NotNil(t, cmd.ProcessState)
	assert.False(t, cmd.ProcessState.Success(), "the child must not have ended normally")

	assert.FileExists(t, filepath.Join(markers, "reached-"+point), "the child reached the point")
	assert.NoFileExists(t, filepath.Join(markers, "survived-"+point), "and did not go past it")
}

// assertWhole: the registry on disk is a header and rows, all whole.
func assertWhole(t *testing.T, s *Store, wantRows int) {
	t.Helper()
	data := readFile(t, s.RegistryPath())
	require.True(t, strings.HasSuffix(string(data), "\n"))
	rows, err := parseRegistry(data)
	require.NoError(t, err)
	assert.Len(t, rows, wantRows)
	assert.Equal(t, 1, strings.Count(string(data), header))
}

// assertParentCanAdd: after the death an add goes through (the kernel freed the
// lock; nothing to recover), keeps the old bytes, and Stale can still read it.
func assertParentCanAdd(t *testing.T, s *Store, old []byte, wantRows int) {
	t.Helper()
	s.LockAttempts = parentLockAttempts
	s.Sleep = nil // real waits, so that the kernel has time to tear the dead child down
	mustAdd(t, s, inputFor("parent-two"))

	data := readFile(t, s.RegistryPath())
	require.True(t, strings.HasPrefix(string(data), string(old)))
	assertWhole(t, s, wantRows)
	_, err := s.Stale(7)
	require.NoError(t, err)
}

// TestHelperProcess_KillPoint is the child of the tests above, not a test: it
// runs only when re-executed with the helper env set. Its name does not contain
// "TestKill", so `-run TestKill` selects the three tests above and not this.
func TestHelperProcess_KillPoint(t *testing.T) {
	if os.Getenv(helperEnv) != helperKillPoint {
		t.Skip("helper process: runs only when re-executed by another test")
	}
	point := os.Getenv(helperEnv + "_POINT")
	markers := os.Getenv(helperEnv + "_MARKERS")
	s := &Store{Dir: os.Getenv(helperEnv + "_DIR")}

	die := func() {
		if err := os.WriteFile(filepath.Join(markers, "reached-"+point), nil, 0o600); err != nil {
			os.Exit(4)
		}
		self, err := os.FindProcess(os.Getpid())
		if err != nil {
			os.Exit(5)
		}
		_ = self.Kill()
		// Still here: the kill did not land. Say so, bounded, and leave.
		time.Sleep(survivedSleepBudget)
		_ = os.WriteFile(filepath.Join(markers, "survived-"+point), nil, 0o600)
		os.Exit(0)
	}

	switch point {
	case pointAfterLock:
		s.TryLock = func(path string) (func() error, error) {
			release, err := TryLockFile(path)
			if err != nil {
				return nil, err
			}
			die()
			return release, nil
		}
	case pointMidPublish:
		s.Publish = func(path string, data []byte) error {
			// What AtomicWriteBytes would have in flight: a temporary file next to
			// the registry, half written. The rename never happens.
			if err := os.WriteFile(filepath.Join(filepath.Dir(path), ".tmp.partial"), data[:len(data)/2], 0o600); err != nil {
				return err
			}
			die()
			return nil
		}
	case pointAfterPublish:
		s.Publish = func(path string, data []byte) error {
			if err := defaultPublish(path, data); err != nil {
				return err
			}
			die()
			return nil
		}
	default:
		os.Exit(6)
	}

	_, _ = s.Add(inputFor("child-key"))
	os.Exit(3) // the add returned: the child was never killed
}
