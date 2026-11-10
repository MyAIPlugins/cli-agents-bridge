package session

// The holder side of the exclusion tests: a child process that takes the lock of a
// session and keeps it until told to let go. Like lock_exclusion_test.go it uses
// only API that exists on c0649ad (NewManager, Register, WithSessionLock), so the
// two files together are what gets copied onto the tree before the fix.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// syncBuffer collects a child's stderr. exec copies it from its own goroutine while
// the test may read it, and a plain bytes.Buffer is a data race under -race even
// when the child wrote nothing.
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

// runHoldChild takes the session lock, says HELD, and keeps it until the parent
// writes RELEASE (or closes stdin, or the hold time given in HOLD_MS is up).
func runHoldChild() int {
	mgr := NewManager(os.Getenv(exclEnvDataDir), time.Second)
	holdMs := exclEnvInt(exclEnvHoldMs)
	err := mgr.WithSessionLock(os.Getenv(exclEnvSID), func() error {
		fmt.Println("HELD")
		if holdMs > 0 {
			time.Sleep(time.Duration(holdMs) * time.Millisecond)
			return nil
		}
		released := make(chan struct{})
		go func() {
			_, _ = bufio.NewReader(os.Stdin).ReadString('\n') // a line, or EOF when the parent is gone
			close(released)
		}()
		select {
		case <-released:
		case <-time.After(exclHoldBound):
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "hold failed: %v\n", err)
		return exclExitAcquire
	}
	return 0
}

// exclSession registers a session in a fresh data dir and gives its lock back, so
// that children can take the lock of an existing session.
func exclSession(t *testing.T) (dataDir, sid string) {
	t.Helper()
	dataDir = t.TempDir()
	mgr := NewManager(dataDir, time.Second)
	mf, release, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: t.TempDir(), AgentName: "VAL-excl", Role: RoleVal,
	})
	require.NoError(t, err)
	require.NoError(t, release())
	return dataDir, mf.SessionID
}

// exclHolder is a child process that holds the lock of a session.
type exclHolder struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

// release tells the child to let go and waits for it to exit.
func (h exclHolder) release(t *testing.T) {
	t.Helper()
	_, err := h.stdin.Write([]byte("RELEASE\n"))
	require.NoError(t, err)
	require.NoError(t, h.cmd.Wait())
}

// startExclHolder starts a child that takes the lock of the session, and returns
// when the child has said HELD. holdMs > 0 makes it let go by itself after that
// long; 0 keeps it until release.
func startExclHolder(t *testing.T, dataDir, sid string, holdMs int) exclHolder {
	t.Helper()
	cmd := exclChildCmd(context.Background(),
		exclHelperEnv+"="+exclModeHold, exclEnvDataDir+"="+dataDir, exclEnvSID+"="+sid,
		exclEnvHoldMs+"="+strconv.Itoa(holdMs))
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(stdout).ReadString('\n')
		line <- strings.TrimSpace(s)
	}()
	select {
	case got := <-line:
		require.Equal(t, "HELD", got, "the child did not get the lock: %s", stderr.String())
	case <-time.After(exclHandshakeMax):
		require.FailNow(t, "the holder child never said HELD", stderr.String())
	}
	return exclHolder{cmd: cmd, stdin: stdin}
}
