//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHelperChild is the body of the child the tests below run, not a test.
func TestHelperChild(t *testing.T) {
	mode := os.Getenv("CAB_TEST_CHILD_MODE")
	if mode == "" {
		t.Skip("helper process only")
	}
	switch mode {
	case "describe":
		stdin, _ := readAllStdin()
		var open []int
		for fd := 3; fd < 64; fd++ {
			if _, err := os.NewFile(uintptr(fd), "probe").Stat(); err == nil {
				open = append(open, fd)
			}
		}
		out, _ := json.Marshal(map[string]any{"args": os.Args[1:], "env": os.Environ(), "stdin": stdin, "openFDs": open})
		fmt.Println(string(out))
		fmt.Fprintln(os.Stderr, "diagnostic on stderr")
		os.Exit(0)
	case "exit7":
		fmt.Fprintln(os.Stderr, "refused: for a reason")
		os.Exit(7)
	case "sleep":
		time.Sleep(6 * time.Second)
		os.Exit(0)
	case "flood":
		chunk := strings.Repeat("x", 4096)
		for i := 0; i < 512; i++ { // 2 MiB on each stream
			fmt.Fprint(os.Stdout, chunk)
			fmt.Fprint(os.Stderr, chunk)
		}
		os.Exit(0)
	}
}

func readAllStdin() (string, error) {
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			return sb.String(), nil
		}
	}
}

func helperSpec(mode string, extraArgs ...string) childSpec {
	return childSpec{
		exe:     os.Args[0],
		args:    append([]string{"-test.run=^TestHelperChild$", "--"}, extraArgs...),
		env:     []string{"CAB_TEST_CHILD_MODE=" + mode},
		timeout: 10 * time.Second,
		maxOut:  64 * 1024,
	}
}

func TestRunChild_TheMessageTravelsOnStdinAndTheArgvIsNotInterpreted(t *testing.T) {
	t.Parallel()
	hostile := "`rm -rf $HOME` $(id) \"q\" 'a' ; | & > /tmp/x\nline2 ☃"
	spec := helperSpec("describe", "VAL-demo", "$(touch /tmp/should-not-exist)", "a b")
	spec.stdin = hostile

	res, err := runChild(context.Background(), spec)
	require.NoError(t, err)
	var got struct {
		Args  []string
		Env   []string
		Stdin string
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(res.stdout)), &got))
	assert.Equal(t, hostile, got.Stdin, "the message arrives byte-identical")
	assert.Equal(t, []string{"--", "VAL-demo", "$(touch /tmp/should-not-exist)", "a b"}, got.Args[len(got.Args)-4:], "each argv element arrives as one argument, uninterpreted")
	assert.NoFileExists(t, "/tmp/should-not-exist")
	assert.Contains(t, res.stderr, "diagnostic on stderr", "stderr is captured apart from stdout")
	assert.NotContains(t, res.stdout, "diagnostic on stderr")
}

func TestRunChild_TheEnvironmentIsExactlyWhatWasGiven(t *testing.T) {
	t.Setenv("SENTINEL_FROM_THE_SERVER", "must-not-leak") // not parallel: it touches the process environment
	spec := helperSpec("describe")
	spec.env = append(childEnv("/Users/x", "/data/dir", "sess0001"), "CAB_TEST_CHILD_MODE=describe")

	res, err := runChild(context.Background(), spec)
	require.NoError(t, err)
	var got struct{ Env []string }
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(res.stdout)), &got))
	assert.ElementsMatch(t, []string{"HOME=/Users/x", "CAB_DATA_DIR=/data/dir", "CAB_SESSION_ID=sess0001", "CAB_TEST_CHILD_MODE=describe"}, got.Env,
		"nothing of the server's environment reaches the child, and the server's own is untouched")
	assert.Equal(t, "must-not-leak", os.Getenv("SENTINEL_FROM_THE_SERVER"))
}

// The child must not inherit the descriptor that carries the protocol, nor the
// identity lock: only 0, 1 and 2 are open in it.
func TestRunChild_OnlyTheThreeStandardDescriptorsAreInherited(t *testing.T) {
	t.Parallel()
	// A descriptor that stands for "the lock" or "the protocol": opened here, by the
	// parent, the normal way (close-on-exec).
	f, err := os.CreateTemp(t.TempDir(), "stands-for-the-lock")
	require.NoError(t, err)
	defer f.Close()

	res, err := runChild(context.Background(), helperSpec("describe"))
	require.NoError(t, err)
	var got struct{ OpenFDs []int }
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(res.stdout)), &got))
	assert.Empty(t, got.OpenFDs, "no descriptor above 2 is open in the child")
}

func TestRunChild_ANonZeroExitIsAResultNotAnError(t *testing.T) {
	t.Parallel()
	res, err := runChild(context.Background(), helperSpec("exit7"))
	require.NoError(t, err)
	assert.Equal(t, 7, res.exitCode)
	assert.Contains(t, res.stderr, "refused: for a reason", "the CLI's own message is what the model reads")
}

func TestRunChild_ADeadlineKillsTheChildAndSaysSo(t *testing.T) {
	t.Parallel()
	spec := helperSpec("sleep")
	spec.timeout = 300 * time.Millisecond
	started := time.Now()
	done := make(chan error, 1)
	go func() { _, err := runChild(context.Background(), spec); done <- err }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, errChildTimeout)
		assert.Less(t, time.Since(started), 8*time.Second, "killed at the deadline, not left to finish its 6 seconds")
	case <-time.After(4 * time.Second):
		t.Fatal("runChild did not return: the deadline does not bind")
	}
}

func TestRunChild_ACancelledContextStopsTheChild(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	done := make(chan error, 1)
	go func() { _, err := runChild(ctx, helperSpec("sleep")); done <- err }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(4 * time.Second):
		t.Fatal("runChild ignored the cancellation")
	}
}

func TestRunChild_AChattyChildIsCappedAndNeverBlocks(t *testing.T) {
	t.Parallel()
	spec := helperSpec("flood")
	spec.maxOut = 8 * 1024
	done := make(chan struct{})
	var res childResult
	var err error
	go func() { res, err = runChild(context.Background(), spec); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("a child writing 2 MiB blocked: the pipes are not drained")
	}
	require.NoError(t, err)
	assert.LessOrEqual(t, len(res.stdout), 8*1024)
	assert.LessOrEqual(t, len(res.stderr), 8*1024)
	assert.True(t, res.truncated, "and it says it cut")
}

func TestRunChild_RefusesAnIncompleteSpec(t *testing.T) {
	t.Parallel()
	for _, s := range []childSpec{{}, {exe: "x"}, {exe: "x", timeout: time.Second}, {exe: "x", maxOut: 1}} {
		_, err := runChild(context.Background(), s)
		require.Error(t, err, "%+v", s)
	}
}
