// Package integration runs end-to-end scenarios against a freshly-built
// cab-bridge binary driven via os/exec subprocesses. Mirrors the actual
// invocation path a Claude Code session would use, so any regression here
// would be visible to a real user.
//
// All scenarios share a fresh tempdir per test (CAB_DATA_DIR override) and
// a single binary build cached per package (see buildBinary).
package integration

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

var (
	binPath     string
	binBuildErr error
	binOnce     sync.Once
)

// buildBinary compiles cab-bridge once per package run and returns the path.
// Subsequent tests reuse the same binary — `go build` is the slowest step
// of any integration test, this caches ~3s per test.
func buildBinary(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		repoRoot, err := filepath.Abs("../..")
		if err != nil {
			binBuildErr = err
			return
		}
		dir, err := os.MkdirTemp("", "cab-int-bin-*")
		if err != nil {
			binBuildErr = err
			return
		}
		binPath = filepath.Join(dir, "cab-bridge")
		cmd := exec.Command("go", "build", "-o", binPath, "./cmd/cab-bridge")
		cmd.Dir = repoRoot
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			binBuildErr = errors.New("go build failed: " + stderr.String())
		}
	})
	require.NoError(t, binBuildErr)
	return binPath
}

// run executes the binary with args and the supplied env overrides, returning
// stdout/stderr captured + exit code. Never fails the test directly — caller
// asserts on the return values to allow negative-path tests.
func run(t *testing.T, args []string, env []string) (stdout, stderr string, exit int) {
	t.Helper()
	bin := buildBinary(t)
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	var outB, errB bytes.Buffer
	cmd.Stdout = &outB
	cmd.Stderr = &errB

	err := cmd.Run()
	if err == nil {
		return outB.String(), errB.String(), 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return outB.String(), errB.String(), exitErr.ExitCode()
	}
	t.Fatalf("subprocess invocation error (not an ExitError): %v\nstderr: %s", err, errB.String())
	return outB.String(), errB.String(), -1
}

// dataDirEnv returns the env override slice for CAB_DATA_DIR + optional
// extras (e.g. CAB_POLL_INTERVAL_MS=50 for accelerated tests).
func dataDirEnv(dataDir string, extra ...string) []string {
	env := []string{"CAB_DATA_DIR=" + dataDir}
	env = append(env, extra...)
	return env
}

// mustJSONField extracts a top-level field from a JSON stdout payload using
// a tiny dependency-free parser substring match. Sufficient for the small
// integer/string fields we assert on (sessionId, msg-..., counters).
func mustJSONField(t *testing.T, jsonOut, fieldName string) string {
	t.Helper()
	needle := `"` + fieldName + `":`
	idx := strings.Index(jsonOut, needle)
	if idx < 0 {
		t.Fatalf("field %q not found in JSON: %s", fieldName, jsonOut)
	}
	tail := strings.TrimSpace(jsonOut[idx+len(needle):])
	// Strip leading quote if string-typed
	if strings.HasPrefix(tail, `"`) {
		end := strings.Index(tail[1:], `"`)
		if end < 0 {
			t.Fatalf("malformed string field %q in JSON: %s", fieldName, jsonOut)
		}
		return tail[1 : 1+end]
	}
	// Numeric / boolean — read up to comma or newline
	for i, r := range tail {
		if r == ',' || r == '\n' || r == '}' || r == ' ' {
			return tail[:i]
		}
	}
	return tail
}
