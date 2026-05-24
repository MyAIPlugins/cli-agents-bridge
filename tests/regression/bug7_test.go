package regression

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBUG7_ReceiveTimeout_StderrAndExit124 reproduces BUG-7 (Patil
// bridge-receive.sh:41 wrote "No response received after Ns" to STDOUT
// instead of stderr — callers using command substitution would capture
// this error string as if it were the JSON response payload).
//
// cli-agents-bridge fix:
//   - cmd/cab-bridge/main.go routes the receive subcommand error through
//     fmt.Fprintln(os.Stderr, ...) — never stdout.
//   - ErrTimeout sentinel is mapped to exit code 124 (coreutils timeout(1)
//     convention), distinguishing transient timeout from config/validation
//     failures (exit 1).
//
// This is an end-to-end test: it compiles cab-bridge into a temp binary,
// runs `cab-bridge receive` against an empty inbox with a 1-second
// max-deadline, and asserts:
//
//   - exit code == 124
//   - stdout is empty
//   - stderr contains the literal "timeout" substring
func TestBUG7_ReceiveTimeout_StderrAndExit124(t *testing.T) {
	t.Parallel()

	binary := buildCabBridge(t)

	dataDir := t.TempDir()
	sid := "abcd1234"
	inbox := filepath.Join(dataDir, "sessions", sid, "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	cmd := exec.Command(binary, "receive",
		"--msg-id=msg-aaaaaaaaaaaa",
		"--session-id="+sid,
		"--max-deadline=1",
	)
	cmd.Env = append(os.Environ(),
		"CAB_DATA_DIR="+dataDir,
		"CAB_POLL_INTERVAL_MS=50",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	require.Error(t, err, "BUG-7 regression: missing reply must produce error exit")

	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr),
		"expected *exec.ExitError, got %T (%v)", err, err)
	assert.Equal(t, 124, exitErr.ExitCode(),
		"BUG-7 regression: timeout must exit 124 (coreutils timeout(1) convention), got %d", exitErr.ExitCode())

	assert.Empty(t, strings.TrimSpace(stdout.String()),
		"BUG-7 regression: error message must NOT be written to stdout (Patil wrote 'No response received' to stdout)")
	assert.Contains(t, strings.ToLower(stderr.String()), "timeout",
		"BUG-7 regression: stderr must contain the timeout error message; got: %q", stderr.String())
}

// TestBUG7_ReceiveBadFlag_Exit1NotStdout verifies that config/validation
// errors (missing --msg-id, invalid session ID, etc.) exit 1 with stderr
// output, NOT exit 124 (which is reserved for ErrTimeout). This is the
// other half of the BUG-7 fix: distinct exit codes for distinct failure
// modes so callers can branch.
func TestBUG7_ReceiveBadFlag_Exit1NotStdout(t *testing.T) {
	t.Parallel()

	binary := buildCabBridge(t)

	cmd := exec.Command(binary, "receive") // no --msg-id
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	require.Error(t, err)

	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr))
	assert.Equal(t, 1, exitErr.ExitCode(),
		"missing required flag must exit 1, not 124 (reserved for ErrTimeout)")

	assert.Empty(t, strings.TrimSpace(stdout.String()),
		"validation errors must NOT pollute stdout")
	assert.NotEmpty(t, stderr.String(), "validation errors must surface on stderr")
}

// buildCabBridge compiles cab-bridge into t.TempDir() and returns the path.
// Shared helper for any regression test that needs to drive the actual
// binary as a subprocess.
func buildCabBridge(t *testing.T) string {
	t.Helper()

	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	binPath := filepath.Join(t.TempDir(), "cab-bridge-test")
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/cab-bridge")
	cmd.Dir = repoRoot

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Run(), "go build failed: %s", stderr.String())

	return binPath
}
