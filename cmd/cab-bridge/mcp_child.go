package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Running `cab-bridge ask` and `cab-bridge tell` for Claude Desktop (F-72, D4').
//
// The MCP server does not reimplement the two sending verbs. It runs this very binary
// as a child, so what a message goes through (recipient resolution, routing, dedup
// warning, the delivery under the target's lock) is the code the CLI has already been
// through, byte for byte. What the server owns is everything around the child:
//
//   - argv is fixed by the caller, an array handed to the kernel, never a shell line;
//   - the message travels on the child's STDIN, so no quoting rule exists to get wrong;
//   - the child's environment is built from nothing: HOME, CAB_DATA_DIR and the
//     CAB_SESSION_ID of this identity. It does not inherit the server's, and nothing is
//     changed in the server's own environment or working directory, so two calls for
//     two projects in flight cannot step on each other;
//   - stdout and stderr are captured separately, each capped, and never forwarded to
//     the server's own stderr (which Claude Desktop keeps in its log): a CLI error can
//     quote the start of the message;
//   - a deadline kills the child, and the call returns an explicit error;
//   - the child inherits no descriptor beyond 0, 1 and 2, in particular not the one
//     that carries the protocol and not the identity lock (both are close-on-exec).

// childWaitDelay bounds how long we wait for a killed child's pipes to drain.
const childWaitDelay = 2 * time.Second

var errChildTimeout = errors.New("the command did not finish within the time allowed")

type childSpec struct {
	exe     string
	args    []string
	env     []string // the child's WHOLE environment
	stdin   string
	timeout time.Duration
	maxOut  int // cap for stdout and for stderr, each
}

type childResult struct {
	stdout    string
	stderr    string
	exitCode  int
	truncated bool
}

// cappedBuffer keeps the first limit bytes and drops the rest while still accepting
// every write, so a chatty child can never block on a full pipe.
type cappedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	room := c.limit - c.buf.Len()
	if room > 0 {
		if len(p) <= room {
			c.buf.Write(p)
		} else {
			c.buf.Write(p[:room])
			c.truncated = true
		}
	} else if len(p) > 0 {
		c.truncated = true
	}
	return len(p), nil
}

func (c *cappedBuffer) snapshot() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String(), c.truncated
}

// childEnv is the environment of a loop-verb child, and nothing else. baseDir is
// the BASE (CAB_DATA_DIR): the child derives <base>/layout-2 itself, so handing it
// the state dir would make it look for <state>/layout-2 (F-160).
func childEnv(home, baseDir, sessionID string) []string {
	return []string{"HOME=" + home, "CAB_DATA_DIR=" + baseDir, "CAB_SESSION_ID=" + sessionID}
}

// runChild runs spec and waits for it. A non-zero exit is NOT an error: it is a
// result (the CLI's messages are written for an LLM and the caller shows them). An
// error means the child could not be run or did not finish.
func runChild(ctx context.Context, spec childSpec) (childResult, error) {
	if spec.exe == "" || spec.timeout <= 0 || spec.maxOut <= 0 {
		return childResult{}, errors.New("runChild: exe, timeout and maxOut are required")
	}
	ctx, cancel := context.WithTimeout(ctx, spec.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, spec.exe, spec.args...)
	cmd.Env = append([]string{}, spec.env...) // non-nil even when empty: never the parent's
	cmd.Stdin = strings.NewReader(spec.stdin)
	out := &cappedBuffer{limit: spec.maxOut}
	errBuf := &cappedBuffer{limit: spec.maxOut}
	cmd.Stdout, cmd.Stderr = out, errBuf
	cmd.WaitDelay = childWaitDelay

	err := cmd.Run()
	stdout, t1 := out.snapshot()
	stderr, t2 := errBuf.snapshot()
	res := childResult{stdout: stdout, stderr: stderr, truncated: t1 || t2}

	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return res, nil
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return res, fmt.Errorf("%w (%s)", errChildTimeout, spec.timeout)
	case ctx.Err() != nil:
		return res, ctx.Err()
	case errors.As(err, &exitErr):
		res.exitCode = exitErr.ExitCode()
		return res, nil
	default:
		return res, fmt.Errorf("could not run the command: %w", err)
	}
}
