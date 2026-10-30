//go:build !windows

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHelperBrokenStdout is the body of a child process, not a test: it writes to a
// standard output whose reader has gone, with or without swallowSIGPIPE.
func TestHelperBrokenStdout(t *testing.T) {
	mode := os.Getenv("CAB_TEST_BROKEN_STDOUT")
	if mode == "" {
		t.Skip("helper process only")
	}
	if mode == "swallow" {
		stop := swallowSIGPIPE()
		defer stop()
	}
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n') // wait until the parent closed the reader
	_, err := os.Stdout.WriteString("x\n")
	if errors.Is(err, syscall.EPIPE) {
		fmt.Fprintln(os.Stderr, "EPIPE-RETURNED")
		// Straight out: the test framework would print its summary on the same
		// broken stdout once the guard is released, and that write is not under test.
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, "unexpected write result:", err)
	os.Exit(5)
}

func runBrokenStdout(t *testing.T, mode string) (stderr string, waitErr error) {
	t.Helper()
	pr, pw, err := os.Pipe()
	require.NoError(t, err)
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperBrokenStdout$")
	cmd.Env = append(os.Environ(), "CAB_TEST_BROKEN_STDOUT="+mode)
	cmd.Stdout = pw
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	require.NoError(t, pw.Close()) // the child holds the only writer now
	require.NoError(t, pr.Close()) // and nobody reads: its stdout is a broken pipe
	_, err = io.WriteString(stdin, "go\n")
	require.NoError(t, err)
	_ = stdin.Close()
	waitErr = cmd.Wait()
	return errBuf.String(), waitErr
}

// Without the guard Go kills the process on a write to a closed stdout, before the
// server can mark its sessions done or release its identity. With it the write
// just fails and the server can stop in order.
func TestSwallowSIGPIPE_AWriteToAClosedStdoutReturnsEPIPEInsteadOfKillingTheProcess(t *testing.T) {
	stderr, err := runBrokenStdout(t, "swallow")
	require.NoError(t, err, "the process must survive; stderr: %s", stderr)
	assert.Contains(t, stderr, "EPIPE-RETURNED")
}

// The control that gives the test above its meaning: the same write, no guard.
func TestSwallowSIGPIPE_WithoutItTheProcessDiesOfSIGPIPE(t *testing.T) {
	_, err := runBrokenStdout(t, "plain")
	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr), "expected the child to die, got %v", err)
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	require.True(t, ok)
	assert.True(t, status.Signaled() && status.Signal() == syscall.SIGPIPE, "died of %v", status)
}
