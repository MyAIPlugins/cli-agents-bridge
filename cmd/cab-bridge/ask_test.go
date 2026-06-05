package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// captureStderr redirects os.Stderr for the duration of fn and returns what was
// written. Symmetric to captureStdout (register_test.go); ask writes its
// warnings (F-34, F-43) and the A-4 replying_to echo to stderr, so the
// integration tests below assert on the captured stderr while letting stdout
// (the captured msg-id) be discarded.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	defer func() { os.Stderr = old }()

	fn()
	require.NoError(t, w.Close())
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(data)
}

// runAskCapture invokes runAsk with args while capturing both streams: stdout
// (the emitted msg-id) is swallowed so it does not pollute test logs, stderr is
// returned for assertion. The nesting works because captureStdout redirects
// os.Stdout around the inner closure that itself redirects os.Stderr.
func runAskCapture(t *testing.T, args []string) (stderr string, runErr error) {
	t.Helper()
	captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			runErr = runAsk(args)
		})
	})
	return stderr, runErr
}

// TestRunAsk_UnreadWarning_SuggestsExecutableReadCommand is the A-1 (F-34)
// check: when the recipient has a still-unread message from --to, the warning
// must suggest a command that is EXECUTABLE as-is in a shared scope — i.e.
// `cab-bridge read --session-id=<sid> <msg-id>` with the --session-id flag
// BEFORE the positional (Go flag parsing rejects flags after positionals). sid
// is the sender's own id (the unread message lives in the sender's inbox).
func TestRunAsk_UnreadWarning_SuggestsExecutableReadCommand(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	const sender = "escsnd01"
	const target = "valtgt01"
	const unreadID = "msg-aaaaaaaaaaaa"
	scope := "/repo/x"

	plantOverviewSession(t, dataDir, sender, session.RoleEsc, "ESC-x", scope, "", "working")
	plantOverviewSession(t, dataDir, target, session.RoleVal, "VAL-x", scope, "", session.StateOrchestrating)
	// An unread non-ack message from the target sits in the SENDER's inbox; with
	// no prior send to the target the cutoff is the zero time, so this is unread.
	plantInboxAt(t, dataDir, sender, unreadID, target, message.TypeQuery, "brief", time.Now().UTC())

	stderr, err := runAskCapture(t, []string{"--session-id=" + sender, "--to=" + target, "--content=reply"})
	require.NoError(t, err)

	// The exact executable command: --session-id (the SENDER's id) BEFORE the
	// positional msg-id, as one contiguous substring. The contiguity is the
	// proof of the flag-before-positional order (the F-89/state gotcha) — a bare
	// `read <id>` or a trailing flag would not match.
	assert.Contains(t, stderr, "cab-bridge read --session-id="+sender+" "+unreadID,
		"warning must suggest an executable read command with --session-id before the msg-id")
}

// TestNormalizeAskType is the A-2 pure-function table: the "question" alias (any
// case) → "query"; a near-miss → the same input plus a "query" suggestion; a
// real type or an unrelated unknown → unchanged with no suggestion.
func TestNormalizeAskType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, input, wantNorm, wantSuggest string
	}{
		{"exact alias", "question", message.TypeQuery, ""},
		{"alias mixed case", "Question", message.TypeQuery, ""},
		{"alias upper", "QUESTION", message.TypeQuery, ""},
		{"near-miss plural", "questions", "questions", message.TypeQuery},
		{"near-miss stem", "quest", "quest", message.TypeQuery},
		{"valid query untouched", "query", "query", ""},
		{"valid response untouched", "response", "response", ""},
		{"unrelated unknown", "foobar", "foobar", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotNorm, gotSuggest := normalizeAskType(tc.input)
			assert.Equal(t, tc.wantNorm, gotNorm, "normalized type")
			assert.Equal(t, tc.wantSuggest, gotSuggest, "suggestion")
		})
	}
}

// TestRunAsk_QuestionTypeAlias_NormalizedToQueryAndSent is the A-2 integration
// happy path: `--type=question` must NOT be lost — the message is delivered and
// its wire type is the canonical "query", not "question".
func TestRunAsk_QuestionTypeAlias_NormalizedToQueryAndSent(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	const sender = "escq0001"
	const target = "valq0001"
	plantOverviewSession(t, dataDir, sender, session.RoleEsc, "ESC-x", "/repo/x", "", "working")
	plantOverviewSession(t, dataDir, target, session.RoleVal, "VAL-x", "/repo/x", "", session.StateOrchestrating)

	var runErr error
	out := captureStdout(t, func() {
		_ = captureStderr(t, func() {
			runErr = runAsk([]string{"--session-id=" + sender, "--to=" + target, "--type=question", "--content=hi"})
		})
	})
	require.NoError(t, runErr, "the question alias must not reject the send")

	msgID := firstLine(out)
	require.NotEmpty(t, msgID, "ask must print the delivered msg-id on stdout")
	m, _, err := findMessage(filepath.Join(dataDir, "sessions", target), msgID, 65536)
	require.NoError(t, err, "the message must be delivered to the target inbox")
	assert.Equal(t, message.TypeQuery, m.Type, "the wire type must be normalized to query, not question")
}

// TestRunAsk_UnknownTypeWithSuggestion_RejectedNotSent is the A-2 error path: a
// near-miss type is rejected with an actionable error (valid list + did-you-mean)
// and nothing is delivered. The reject fires before any FS access.
func TestRunAsk_UnknownTypeWithSuggestion_RejectedNotSent(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	const sender = "escq0002"
	const target = "valq0002"
	plantOverviewSession(t, dataDir, sender, session.RoleEsc, "ESC-x", "/repo/x", "", "working")
	plantOverviewSession(t, dataDir, target, session.RoleVal, "VAL-x", "/repo/x", "", session.StateOrchestrating)

	err := runAsk([]string{"--session-id=" + sender, "--to=" + target, "--type=questions", "--content=hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid --type "questions"`)
	assert.Contains(t, err.Error(), `did you mean "query"`)

	entries, _ := os.ReadDir(filepath.Join(dataDir, "sessions", target, "inbox"))
	assert.Empty(t, entries, "an invalid type must not deliver a message")
}
