//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
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
		out, _ := json.Marshal(map[string]any{
			"args": os.Args[1:], "env": os.Environ(), "stdin": stdin,
			"openFDs": openDescriptors(), "markerHits": markerHits(),
		})
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

// descriptor is an open descriptor above 2 as the child found it, named by what it
// IS (device and inode) because its number says nothing about who opened it: a Go
// process may open descriptors of its own (the runtime's poller on Linux), and the
// environment that started the test may have left one behind.
type descriptor struct {
	FD   int    `json:"fd"`
	ID   string `json:"id"`
	Link string `json:"link,omitempty"` // where /proc says it points, on Linux; empty elsewhere
}

func fileID(st *syscall.Stat_t) string { return fmt.Sprintf("%d:%d", st.Dev, st.Ino) }

// openDescriptors lists 3..63 with fstat and nothing else: os.NewFile would hand the
// descriptor to a finalizer that closes it, and could register it with the poller.
func openDescriptors() []descriptor {
	var open []descriptor
	for fd := 3; fd < 64; fd++ {
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err != nil {
			continue
		}
		link, _ := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd)) // a path lookup, it opens nothing
		open = append(open, descriptor{FD: fd, ID: fileID(&st), Link: link})
	}
	return open
}

// markerHits is which of the files named in CAB_TEST_MARKERS (comma separated) the
// child holds open under some descriptor above 2: the files the parent holds and must
// not have handed over.
func markerHits() []int {
	var hits []int
	open := map[string]bool{}
	for _, d := range openDescriptors() {
		open[d.ID] = true
	}
	for i, path := range strings.Split(os.Getenv("CAB_TEST_MARKERS"), ",") {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && open[fileID(st)] {
			hits = append(hits, i)
		}
	}
	return hits
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

type childDescriptors struct {
	OpenFDs    []descriptor
	MarkerHits []int
}

func describeChildWith(t *testing.T, markers ...string) childDescriptors {
	t.Helper()
	spec := helperSpec("describe")
	spec.env = append(spec.env, "CAB_TEST_MARKERS="+strings.Join(markers, ","))
	res, err := runChild(context.Background(), spec)
	require.NoError(t, err)
	var got childDescriptors
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(res.stdout)), &got))
	return got
}

// The child must not inherit what the server holds: the identity lock above all. The
// first version of this test asked for "no descriptor above 2 is open in the child",
// which measures the whole environment: on the Linux runner of CI the child had one
// descriptor the test had not put there, and the test failed for a reason that said
// nothing about runChild. What matters is that the files the PARENT holds do not
// reach the child, so the parent holds the real lock (taken the way the server takes
// it, same file, same open flags) and a plain file opened the normal way, and the child
// reports whether either is open in it. Whatever else the child has is listed in the
// message and not judged.
func TestRunChild_WhatTheParentHoldsIsNotInheritedByTheChild(t *testing.T) {
	t.Parallel()
	lock, err := newMCPEnv(t).manager().AcquireIdentityLock("child-test-identity")
	require.NoError(t, err)
	defer func() { require.NoError(t, lock.Release()) }()
	plain, err := os.CreateTemp(t.TempDir(), "held-by-the-parent")
	require.NoError(t, err)
	defer plain.Close()

	got := describeChildWith(t, lock.Path(), plain.Name())
	t.Logf("descriptors above 2 in the child (not judged; read them with -v): %+v", got.OpenFDs)
	assert.Empty(t, got.MarkerHits, "marker 0 is the identity lock, marker 1 a file opened the normal way; descriptors the child has: %+v", got.OpenFDs)
}

// The premise of the test above, asserted instead of assumed: the probe does see a
// descriptor that IS inherited. Same child, same markers, but the lock file is handed
// over on purpose. Without this, an empty MarkerHits could be a probe that sees nothing.
func TestRunChild_TheDescriptorProbeSeesAFileThatIsInherited(t *testing.T) {
	t.Parallel()
	lock, err := newMCPEnv(t).manager().AcquireIdentityLock("child-probe-control")
	require.NoError(t, err)
	defer func() { require.NoError(t, lock.Release()) }()
	handed, err := os.Open(lock.Path())
	require.NoError(t, err)
	defer handed.Close()
	plain, err := os.CreateTemp(t.TempDir(), "held-by-the-parent")
	require.NoError(t, err)
	defer plain.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperChild$", "--")
	cmd.Env = []string{"CAB_TEST_CHILD_MODE=describe", "CAB_TEST_MARKERS=" + lock.Path() + "," + plain.Name()}
	cmd.ExtraFiles = []*os.File{handed}
	out, err := cmd.Output()
	require.NoError(t, err)
	var got childDescriptors
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(out))), &got))
	assert.Equal(t, []int{0}, got.MarkerHits, "the lock file handed over is found, the plain file that was not is not; descriptors: %+v", got.OpenFDs)
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
