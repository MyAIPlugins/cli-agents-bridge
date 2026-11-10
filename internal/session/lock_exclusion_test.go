package session

// F-160: the session lock must exclude real processes, and the test that says so
// must be able to fail.
//
// THIS FILE USES ONLY API THAT EXISTS ON c0649ad (NewManager, Register,
// WithSessionLock, ErrLockHeld). That is the point: the same file, copied onto the
// tree before the fix, is the proof that the defect was real and that the detector
// sees it (criterion 1 of the BAR). Anything that needs the new API lives in another
// file.
//
// THE DETECTOR. The critical section is os.Mkdir(guard), a 100 us wait, and
// os.Remove(guard). Mkdir of an existing directory fails, so a process that finds
// the guard already there has met another one inside the section: it writes a byte
// to ovl.log. A holder that must die removes the guard first and dies while still
// holding the lock (a dead process leaves no marker), so a death never looks like
// an overlap.
//
// THE POSITIVE CONTROL. A detector that counts 0 proves nothing until it is shown
// to count when there IS an overlap. TestSessionLock_TheDetectorSeesAForcedOverlap
// runs the same guard with NO lock and a handshake that forces exactly one overlap
// per round, so the count is K by construction and not by luck of the scheduler.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	exclHelperEnv   = "CAB_SESSION_LOCK_EXCL_HELPER"
	exclModeLoop    = "loop"
	exclModeForced  = "forced"
	exclModeHold    = "hold"
	exclEnvDataDir  = exclHelperEnv + "_DATADIR"
	exclEnvSID      = exclHelperEnv + "_SID"
	exclEnvDir      = exclHelperEnv + "_DIR"
	exclEnvTotal    = exclHelperEnv + "_TOTAL"
	exclEnvDieEvery = exclHelperEnv + "_DIE_EVERY"
	exclEnvSection  = exclHelperEnv + "_SECTION_US"
	exclEnvPause    = exclHelperEnv + "_PAUSE_US"
	exclEnvID       = exclHelperEnv + "_ID"
	exclEnvRounds   = exclHelperEnv + "_ROUNDS"
	exclEnvStall    = exclHelperEnv + "_STALL"
	exclEnvHoldMs   = exclHelperEnv + "_HOLD_MS"

	// Child exit codes. exclExitDied is the deliberate death of the holder.
	exclExitDied      = 3
	exclExitAcquire   = 5
	exclExitHandshake = 6
	exclExitIO        = 7

	// Bounds. They are limits of the TEST: a wait without one is a test that hangs
	// instead of failing.
	exclPollStep     = 5 * time.Millisecond
	exclHandshakeMax = 10 * time.Second
	exclAcquireCap   = 2 * time.Second
	exclRunBound     = 5 * time.Minute
	exclMaxRespawns  = 1000
	exclHoldBound    = 60 * time.Second
	exclGuardName    = "guard"
	exclAcqLog       = "acq.log"
	exclOvlLog       = "ovl.log"
)

// exclVolume is the number of acquisitions the BAR asks for, and the one used under
// -short (same assertions, a tenth of the work).
func exclVolume() int {
	if testing.Short() {
		return 1000
	}
	return 10000
}

type exclRun struct {
	procs     int
	total     int
	dieEvery  int // 0 = nobody dies
	sectionUS int
	pauseUS   int // outside the lock, so the others get a turn
	// helper is the name of the test function the children re-execute. Empty means
	// the one in this file; the control on the old algorithm names its own.
	helper string
}

func (r exclRun) helperTest() string {
	if r.helper == "" {
		return "TestHelperProcess_SessionLock"
	}
	return r.helper
}

type exclResult struct {
	acquisitions int
	overlaps     int
	deaths       int
	failures     []string
}

func TestSessionLock_ExcludesAcrossProcesses(t *testing.T) {
	total := exclVolume()

	t.Run("nobody_dies", func(t *testing.T) {
		res := runExclusion(t, exclRun{procs: 8, total: total, sectionUS: 100, pauseUS: 50})
		assert.Empty(t, res.failures, "every child must finish its acquisitions")
		assert.Equal(t, total, res.acquisitions)
		assert.Zero(t, res.overlaps, "two processes were inside the critical section together")
	})

	t.Run("the_holder_dies", func(t *testing.T) {
		const dieEvery = 50
		res := runExclusion(t, exclRun{procs: 8, total: total, dieEvery: dieEvery, sectionUS: 100, pauseUS: 50})
		assert.Empty(t, res.failures, "every child must finish its acquisitions or die on purpose")
		assert.Equal(t, total, res.acquisitions)
		assert.Zero(t, res.overlaps, "two processes were inside the critical section together")
		assert.GreaterOrEqual(t, res.deaths, total/dieEvery*3/4,
			"the scenario is only worth something if holders really died while holding the lock")
	})

	t.Run("handoff", func(t *testing.T) {
		// The shape that favours passing the lock from one process to the next: two
		// processes and a section with no wait in it.
		res := runExclusion(t, exclRun{procs: 2, total: total, sectionUS: 0})
		assert.Empty(t, res.failures)
		assert.Equal(t, total, res.acquisitions)
		assert.Zero(t, res.overlaps, "two processes were inside the critical section together")
	})
}

func TestSessionLock_TheDetectorSeesAForcedOverlap(t *testing.T) {
	const rounds = 10
	ovl, err := runForcedRounds(t, rounds, exclHandshakeMax, "")
	require.NoError(t, err)
	assert.Equal(t, rounds, ovl, "one forced overlap per round, exactly: the detector counts what it is shown")
}

// A child that never answers must turn into an error that says where, and not into
// a different count that looks like a result.
func TestSessionLock_AStalledChildIsATimeoutNotACount(t *testing.T) {
	_, err := runForcedRounds(t, 3, time.Second, "1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
	assert.Contains(t, err.Error(), "round 0")
}

// Window 5 of the BAR: a killed process that nobody has waited for yet is a zombie,
// and kill(pid, 0) still succeeds on a zombie. A lock decided by "is the holder
// alive?" stays held for as long as the parent does not reap; a kernel lock goes
// with the descriptors, which the kernel closes when the process exits.
func TestSessionLock_KillingTheHolderFreesItBeforeTheCorpseIsReaped(t *testing.T) {
	dataDir, sid := exclSession(t)
	holder := startExclHolder(t, dataDir, sid, 0)

	require.NoError(t, holder.cmd.Process.Kill())
	// No Wait here: the process is dead but not reaped.

	mgr := NewManager(dataDir, time.Second)
	acquired := false
	var lastErr error
	for attempt := 0; attempt < 100 && !acquired; attempt++ {
		lastErr = mgr.WithSessionLock(sid, func() error { acquired = true; return nil })
		if lastErr == nil {
			break
		}
		require.ErrorIs(t, lastErr, ErrLockHeld, "the only acceptable failure is that it is still held")
		time.Sleep(10 * time.Millisecond)
	}
	assert.True(t, acquired, "the lock must be free right after the holder died, reaped or not: last error %v", lastErr)
	assert.Nil(t, holder.cmd.ProcessState, "the child was never waited for: the test did not give the kernel a hand")
}

// --- the loop child ---------------------------------------------------------

type exclLoop struct {
	dir      string
	total    int
	dieEvery int
	section  time.Duration
	pause    time.Duration
}

func exclLoopFromEnv() exclLoop {
	return exclLoop{
		dir:      os.Getenv(exclEnvDir),
		total:    exclEnvInt(exclEnvTotal),
		dieEvery: exclEnvInt(exclEnvDieEvery),
		section:  time.Duration(exclEnvInt(exclEnvSection)) * time.Microsecond,
		pause:    time.Duration(exclEnvInt(exclEnvPause)) * time.Microsecond,
	}
}

func exclEnvInt(name string) int {
	n, err := strconv.Atoi(os.Getenv(name))
	if err != nil {
		return 0
	}
	return n
}

// run takes the lock through `with` until the shared count reaches the total.
// ErrLockHeld is retried until a cap: the code before the fix returns it at once,
// the code after waits inside the call, and both must work with this loop.
func (c exclLoop) run(with func(func() error) error) int {
	for {
		finished := false
		deadline := time.Now().Add(exclAcquireCap)
		var err error
		for {
			err = with(func() error {
				var serr error
				finished, serr = c.criticalSection()
				return serr
			})
			if !errors.Is(err, ErrLockHeld) || time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "acquisition failed: %v\n", err)
			return exclExitAcquire
		}
		if finished {
			return 0
		}
		if c.pause > 0 {
			time.Sleep(c.pause)
		}
	}
}

// criticalSection is the critical section. It runs with the lock held and returns
// finished=true, doing nothing, once the total is reached.
func (c exclLoop) criticalSection() (finished bool, err error) {
	done, err := exclFileSize(filepath.Join(c.dir, exclAcqLog))
	if err != nil {
		return false, err
	}
	if done >= c.total {
		return true, nil
	}
	guard := filepath.Join(c.dir, exclGuardName)
	owner := true
	if mkErr := os.Mkdir(guard, 0o700); mkErr != nil {
		if !errors.Is(mkErr, fs.ErrExist) {
			return false, mkErr
		}
		owner = false
		if err := exclAppendByte(filepath.Join(c.dir, exclOvlLog)); err != nil {
			return false, err
		}
	}
	if c.section > 0 {
		time.Sleep(c.section)
	}
	if owner {
		if err := os.Remove(guard); err != nil {
			return false, err
		}
	}
	if err := exclAppendByte(filepath.Join(c.dir, exclAcqLog)); err != nil {
		return false, err
	}
	if c.dieEvery > 0 && (done+1)%c.dieEvery == 0 {
		os.Exit(exclExitDied) // still inside WithSessionLock: the lock goes with the process
	}
	return false, nil
}

func exclFileSize(path string) (int, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return int(info.Size()), nil
}

func exclAppendByte(path string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte{'x'}); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// --- the helper process -----------------------------------------------------

// TestHelperProcess_SessionLock is the child of the tests in this file, not a test:
// it runs only when re-executed with the helper env set.
func TestHelperProcess_SessionLock(t *testing.T) {
	mode := os.Getenv(exclHelperEnv)
	if mode == "" {
		t.Skip("helper process: runs only when re-executed by another test")
	}
	switch mode {
	case exclModeLoop:
		mgr := NewManager(os.Getenv(exclEnvDataDir), time.Second)
		sid := os.Getenv(exclEnvSID)
		os.Exit(exclLoopFromEnv().run(func(fn func() error) error { return mgr.WithSessionLock(sid, fn) }))
	case exclModeForced:
		os.Exit(runForcedChild())
	case exclModeHold:
		os.Exit(runHoldChild())
	}
	fmt.Fprintf(os.Stderr, "unknown helper mode %q\n", mode)
	os.Exit(exclExitIO)
}

// runForcedChild is the positive control: the same guard, NO lock, and a handshake
// that makes exactly one of the two children meet the other inside the section in
// every round. Both children create done-<id> in every round, the one who lost the
// race included, so the coordinator knows both really went through.
func runForcedChild() int {
	dir := os.Getenv(exclEnvDir)
	id := os.Getenv(exclEnvID)
	rounds := exclEnvInt(exclEnvRounds)
	stall := os.Getenv(exclEnvStall) == id
	for r := 0; r < rounds; r++ {
		rd := filepath.Join(dir, fmt.Sprintf("r%d", r))
		if !exclWaitForFile(filepath.Join(rd, "go")) {
			fmt.Fprintf(os.Stderr, "child %s: round %d never started\n", id, r)
			return exclExitHandshake
		}
		if stall {
			time.Sleep(exclHandshakeMax)
			return exclExitHandshake
		}
		guard := filepath.Join(rd, exclGuardName)
		tried := filepath.Join(rd, "tried")
		mkErr := os.Mkdir(guard, 0o700)
		switch {
		case mkErr == nil:
			// The winner stays in the section until the other has tried to enter.
			if !exclWaitForFile(tried) {
				fmt.Fprintf(os.Stderr, "child %s: round %d: the other never tried\n", id, r)
				return exclExitHandshake
			}
			if err := os.Remove(guard); err != nil {
				fmt.Fprintf(os.Stderr, "child %s: %v\n", id, err)
				return exclExitIO
			}
		case errors.Is(mkErr, fs.ErrExist):
			if err := exclAppendByte(filepath.Join(dir, exclOvlLog)); err != nil {
				fmt.Fprintf(os.Stderr, "child %s: %v\n", id, err)
				return exclExitIO
			}
			if err := os.WriteFile(tried, nil, 0o600); err != nil {
				fmt.Fprintf(os.Stderr, "child %s: %v\n", id, err)
				return exclExitIO
			}
		default:
			fmt.Fprintf(os.Stderr, "child %s: %v\n", id, mkErr)
			return exclExitIO
		}
		if err := os.WriteFile(filepath.Join(rd, "done-"+id), nil, 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "child %s: %v\n", id, err)
			return exclExitIO
		}
	}
	return 0
}

func exclWaitForFile(path string) bool {
	deadline := time.Now().Add(exclHandshakeMax)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(exclPollStep)
	}
	return false
}

// --- the parent side --------------------------------------------------------

func exclChildCmd(ctx context.Context, env ...string) *exec.Cmd {
	return exclChildCmdFor(ctx, "TestHelperProcess_SessionLock", env...)
}

func exclChildCmdFor(ctx context.Context, testName string, env ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+testName+"$")
	cmd.Env = append(os.Environ(), env...)
	return cmd
}

func runExclusion(t *testing.T, r exclRun) exclResult {
	t.Helper()
	dataDir, sid := exclSession(t)
	work := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), exclRunBound)
	defer cancel()

	var deaths atomic.Int64
	var mu sync.Mutex
	var failures []string
	var wg sync.WaitGroup
	for slot := 0; slot < r.procs; slot++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			for lap := 0; lap < exclMaxRespawns; lap++ {
				var stderr bytes.Buffer
				cmd := exclChildCmdFor(ctx, r.helperTest(),
					exclHelperEnv+"="+exclModeLoop,
					exclEnvDataDir+"="+dataDir, exclEnvSID+"="+sid, exclEnvDir+"="+work,
					exclEnvTotal+"="+strconv.Itoa(r.total), exclEnvDieEvery+"="+strconv.Itoa(r.dieEvery),
					exclEnvSection+"="+strconv.Itoa(r.sectionUS), exclEnvPause+"="+strconv.Itoa(r.pauseUS))
				cmd.Stderr = &stderr
				err := cmd.Run()
				code := 0
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					code = exitErr.ExitCode()
				} else if err != nil {
					code = -1
				}
				switch code {
				case 0:
					return
				case exclExitDied:
					deaths.Add(1)
				default:
					mu.Lock()
					failures = append(failures, fmt.Sprintf("slot %d: exit %d: %s", slot, code, strings.TrimSpace(stderr.String())))
					mu.Unlock()
					return
				}
			}
			mu.Lock()
			failures = append(failures, fmt.Sprintf("slot %d: more than %d children needed", slot, exclMaxRespawns))
			mu.Unlock()
		}(slot)
	}
	wg.Wait()

	acq, err := exclFileSize(filepath.Join(work, exclAcqLog))
	require.NoError(t, err)
	ovl, err := exclFileSize(filepath.Join(work, exclOvlLog))
	require.NoError(t, err)
	t.Logf("procs=%d acquisitions=%d overlaps=%d deaths=%d failures=%d", r.procs, acq, ovl, deaths.Load(), len(failures))
	return exclResult{acquisitions: acq, overlaps: ovl, deaths: int(deaths.Load()), failures: failures}
}

// runForcedRounds drives the positive control. stallID, if not empty, is the id of
// a child that stops answering after the first "go". It returns the number of
// overlaps counted, or an error naming the round and the file that never appeared.
func runForcedRounds(t *testing.T, rounds int, bound time.Duration, stallID string) (int, error) {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), exclRunBound)
	defer cancel()

	cmds := make([]*exec.Cmd, 2)
	for id := range cmds {
		cmds[id] = exclChildCmd(ctx,
			exclHelperEnv+"="+exclModeForced, exclEnvDir+"="+dir, exclEnvID+"="+strconv.Itoa(id),
			exclEnvRounds+"="+strconv.Itoa(rounds), exclEnvStall+"="+stallID)
		require.NoError(t, cmds[id].Start())
	}
	defer func() {
		for _, cmd := range cmds {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	for r := 0; r < rounds; r++ {
		rd := filepath.Join(dir, fmt.Sprintf("r%d", r))
		if err := os.Mkdir(rd, 0o700); err != nil {
			return 0, err
		}
		if err := os.WriteFile(filepath.Join(rd, "go"), nil, 0o600); err != nil {
			return 0, err
		}
		for id := range cmds {
			done := filepath.Join(rd, "done-"+strconv.Itoa(id))
			deadline := time.Now().Add(bound)
			for {
				if _, err := os.Stat(done); err == nil {
					break
				}
				if time.Now().After(deadline) {
					return 0, fmt.Errorf("timed out after %s waiting for done-%d in round %d", bound, id, r)
				}
				time.Sleep(exclPollStep)
			}
		}
	}
	ovl, err := exclFileSize(filepath.Join(dir, exclOvlLog))
	return ovl, err
}
