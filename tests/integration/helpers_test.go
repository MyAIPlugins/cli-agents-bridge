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
	"encoding/json"
	"errors"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

// plantQuery writes a query straight into the recipient's inbox and returns its
// id, without going through any sending command.
//
// Tests that exercise the CONSUME side (auto-ack, inbox observability) only
// needed `ask` as a way to produce an inbound message; coupling them to the
// sending surface made them fail whenever that surface changed, which says
// nothing about the behaviour under test. The v0.8 verbs resolve recipients by
// agent name within a shared scope, a setup these tests have no reason to
// build just to obtain one file on disk.
func plantQuery(t *testing.T, dataDir, toSID, fromSID, fromRole, fromName, content string) string {
	t.Helper()
	inbox := filepath.Join(dataDir, "sessions", toSID, "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	id, err := message.GenerateMessageID()
	require.NoError(t, err)

	m := message.Message{
		ID:            id,
		SchemaVersion: message.SchemaVersionV2,
		From:          fromSID,
		FromRole:      fromRole,
		FromAgentName: fromName,
		To:            toSID,
		ToRole:        "esc",
		Type:          message.TypeQuery,
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		Status:        message.StatusPending,
		Content:       content,
		Metadata:      message.Metadata{FromProject: "itest", ProcessingState: message.StatusPending},
	}
	data, err := json.Marshal(&m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(inbox, id+".json"), data, 0o600))
	return id
}

// runInDir is run() with an explicit working directory, needed by the v0.8
// verbs: they resolve the caller's session from the cwd and take no
// --session-id.
func runInDir(t *testing.T, dir string, args []string, env []string) (stdout, stderr string, exit int) {
	t.Helper()
	bin := buildBinary(t)
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
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

// sharedScopeDirs creates a base holding a git marker plus one working
// directory per name, so every session registered there shares ONE scope while
// keeping a distinct projectPath — the same shape real git worktrees have, and
// the shape the v0.8 verbs need to resolve a recipient by agent name.
func sharedScopeDirs(t *testing.T, names ...string) []string {
	t.Helper()
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, ".git"), 0o700))
	dirs := make([]string, 0, len(names))
	for _, n := range names {
		d := filepath.Join(base, n)
		require.NoError(t, os.MkdirAll(d, 0o700))
		dirs = append(dirs, d)
	}
	return dirs
}

// registerIn registers a session from dir and returns its id.
func registerIn(t *testing.T, dataDir, dir, role, agentName string) string {
	t.Helper()
	out, errOut, exit := runInDir(t, dir, []string{"register", "--role=" + role, "--agent-name=" + agentName}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "register %s: %s", agentName, errOut)
	return mustJSONField(t, out, "sessionId")
}

// extractMsgID pulls the message id out of a verb's echo line
// ("→ ESC-int (msg-abc123...)").
func extractMsgID(out string) string {
	if i := strings.Index(out, "msg-"); i >= 0 {
		rest := out[i:]
		if j := strings.IndexAny(rest, " )\n\t,"); j > 0 {
			return rest[:j]
		}
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(out)
}
