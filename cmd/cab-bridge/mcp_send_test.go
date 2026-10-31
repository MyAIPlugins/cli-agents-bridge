//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// fakeCLI is a stand-in for the binary the tools run as a child: it records what it
// was handed (argv, environment, stdin) and answers from the message, so the contract
// between the server and the child can be pinned without the real command. The real
// one is run against the same tools in TestMCPWithTheRealBinary.
const fakeCLI = `#!/bin/sh
log="$CAB_DATA_DIR/fake-cli.log"
{ echo "ARGC:$#"; for a in "$@"; do echo "ARG:$a"; done; echo "---ENV"; env | sort; echo "---STDIN"; cat; } > "$log.tmp"
mv "$log.tmp" "$log"
case "$(sed -n '/^---STDIN/,$p' "$log")" in
  *EXIT3*) echo "no agent named x - registered here: ESC-SECRET-NAME, CRI-other" >&2; exit 3;;
  *SLEEP*) sleep 30;;
  *DUP*) echo "$1: warning: an identical message to VAL-demo went out 3s ago as msg-0123456789ab - sending anyway" >&2;;
  *NOISE*) echo "cab-bridge: send: mkdir sender outbox (non-fatal): mkdir /Users/someone/.claude/cli-agents-bridge/sessions/28286c50/outbox: not a directory" >&2;;
esac
echo "sent via the fake"
echo "note: a warning about the recipient" >&2
`

func (e *mcpEnv) useFakeCLI() string {
	e.t.Helper()
	path := filepath.Join(e.t.TempDir(), "cab-bridge-fake")
	require.NoError(e.t, os.WriteFile(path, []byte(fakeCLI), 0o755))
	e.exe = path
	return path
}

func (e *mcpEnv) fakeLog() string {
	data, err := os.ReadFile(filepath.Join(e.dataDir, "fake-cli.log"))
	if err != nil {
		return ""
	}
	return string(data)
}

func newSendDemo(t *testing.T) *joinedDemo {
	t.Helper()
	e := newMCPEnv(t)
	e.useFakeCLI()
	e.cfg.MCPChildTimeoutSeconds = 2
	e.val("valdemo01", "VAL-demo", "/repo/demo", 8*time.Hour)
	plantOverviewSession(t, e.dataDir, "escsecret", session.RoleEsc, "ESC-SECRET-NAME", "/repo/demo", "", "working")
	app, c := e.start(coworkClient)
	require.False(t, c.call("join", map[string]any{"project": "demo"}).IsError)
	arcs := architects(t, e)
	require.Len(t, arcs, 1)
	return &joinedDemo{e: e, app: app, c: c, valSID: "valdemo01", arcSID: arcs[0].SessionID,
		inbox: filepath.Join(e.dataDir, "sessions", arcs[0].SessionID, "inbox"), project: "demo"}
}

func TestMCPSend_RunsTheVerbAsAChildWithTheMessageOnStdinAndANothingElseEnvironment(t *testing.T) {
	t.Setenv("MCP_LEAK_SENTINEL", "leaked-from-the-server")
	j := newSendDemo(t)
	msg := "-rf $(touch /tmp/pwned) `id` \"quoted\" 'single'\nline two\n\n-- leading dashes\n"

	res := j.c.call("ask", map[string]any{"to": "VAL-demo", "message": msg})
	require.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "sent via the fake", "the echo on stdout is the result")
	assert.NotContains(t, res.Text(), "a warning about the recipient", "and stderr is not: the server writes what Desktop is told")
	assert.NotContains(t, j.e.logs.all(), "a warning about the recipient", "nor verbatim in the log, which gets sizes")
	assert.Contains(t, j.e.logs.all(), "stderr 1 lines")

	log := j.e.fakeLog()
	assert.Contains(t, log, "ARGC:2\nARG:ask\nARG:VAL-demo\n", "argv is the verb and the recipient, nothing else")
	assert.True(t, strings.HasSuffix(log, "---STDIN\n"+msg), "the message arrives on stdin byte for byte")
	assert.Contains(t, log, "CAB_SESSION_ID="+j.arcSID+"\n")
	assert.Contains(t, log, "CAB_DATA_DIR="+j.e.dataDir+"\n")
	assert.Contains(t, log, "HOME="+j.e.home+"\n")
	assert.NotContains(t, log, "MCP_LEAK_SENTINEL", "the child's environment is built from nothing, not inherited")
	assert.NoFileExists(t, "/tmp/pwned")

	res = j.c.call("tell", map[string]any{"to": "VAL-demo", "message": "fyi"})
	require.False(t, res.IsError, res.Text())
	assert.Contains(t, j.e.fakeLog(), "ARG:tell\n")
}

// The recipient comes from a closed list, so nothing that is not an open val of this
// project ever becomes an argument of the command.
func TestMCPSend_TheRecipientMustBeAnOpenValOfThisProject(t *testing.T) {
	j := newSendDemo(t)
	for _, to := range []string{"ESC-SECRET-NAME", "escsecret", "-h", "--help", "VAL-demo@elsewhere", "VAL demo", "", "VAL-nobody"} {
		res := j.c.call("ask", map[string]any{"to": to, "message": "hello"})
		require.True(t, res.IsError, "to %q", to)
		assert.Contains(t, res.Text(), "VAL-demo", "the refusal offers the open vals")
	}
	assert.Empty(t, j.e.fakeLog(), "the command was never run")

	// A val that predates the name grammar is on the open list and still not a name
	// the command can be safely handed: the grammar is checked on top of the list.
	j.e.val("valodd0001", "VAL space", "/repo/demo", time.Hour)
	odd := j.c.call("tell", map[string]any{"to": "VAL space", "message": "hello"})
	require.True(t, odd.IsError)
	assert.Contains(t, odd.Text(), "`to` must be the name of a val")
	assert.Empty(t, j.e.fakeLog())

	// The refusal is no oracle: an executor that exists and a name that does not get
	// the same answer, word for word, and the executor's name appears only because the
	// model typed it.
	real := j.c.call("ask", map[string]any{"to": "ESC-SECRET-NAME", "message": "hello"}).Text()
	absent := j.c.call("ask", map[string]any{"to": "ESC-NOT-THERE", "message": "hello"}).Text()
	assert.Equal(t, strings.ReplaceAll(absent, "ESC-NOT-THERE", "ESC-SECRET-NAME"), real)
	nameless := j.c.call("ask", map[string]any{"to": "-h", "message": "hello"})
	assert.NotContains(t, nameless.Raw, "ESC-SECRET-NAME", "a name nobody typed is never offered")
}

// "The permission closes something", on the sending side: with the bridge closed or
// expired an architect cannot write to that val, even from a session that joined while
// it was open.
func TestMCPSend_AClosedOrExpiredBridgeCannotBeWrittenTo(t *testing.T) {
	j := newSendDemo(t)
	require.NoError(t, j.e.manager().CloseDesktopGrant(j.valSID))
	res := j.c.call("tell", map[string]any{"to": "VAL-demo", "message": "hello"})
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "none")
	assert.Empty(t, j.e.fakeLog())

	_, err := j.e.manager().OpenDesktopGrant(j.valSID, time.Hour)
	require.NoError(t, err)
	require.False(t, j.c.call("tell", map[string]any{"to": "VAL-demo", "message": "hello"}).IsError, "reopened, it works again")
	j.e.clock.advance(2 * time.Hour)
	res = j.c.call("tell", map[string]any{"to": "VAL-demo", "message": "again"})
	require.True(t, res.IsError, "and expired, it does not")
}

func TestMCPSend_AFailureOfTheCommandNamesNobodyAndSaysWhatToDo(t *testing.T) {
	j := newSendDemo(t)
	res := j.c.call("ask", map[string]any{"to": "VAL-demo", "message": "EXIT3"})
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "not sent")
	assert.Contains(t, res.Text(), "exit 3")
	for _, leaked := range []string{"ESC-SECRET-NAME", "CRI-other", "registered here"} {
		assert.NotContains(t, res.Raw, leaked, "the CLI's own refusal is not forwarded")
		assert.NotContains(t, j.e.logs.all(), leaked)
	}
}

func TestMCPSend_ACommandThatDoesNotFinishIsKilledAndTheUncertaintyIsSaid(t *testing.T) {
	j := newSendDemo(t)
	j.app.cfg.MCPChildTimeoutSeconds = 1
	start := time.Now()
	res := j.c.call("tell", map[string]any{"to": "VAL-demo", "message": "SLEEP"})
	assert.Less(t, time.Since(start), 5*time.Second, "the child was killed at its deadline")
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "not known whether the message was sent")
}

func TestMCPSend_RefusesAnEmptyOrOversizedMessageBeforeRunningAnything(t *testing.T) {
	j := newSendDemo(t)
	for _, msg := range []string{"", "   \n\t "} {
		assert.True(t, j.c.call("ask", map[string]any{"to": "VAL-demo", "message": msg}).IsError, "message %q", msg)
	}
	big := j.c.call("ask", map[string]any{"to": "VAL-demo", "message": strings.Repeat("x", j.app.cfg.MaxMessageBytes+1)})
	require.True(t, big.IsError)
	assert.Contains(t, big.Text(), "the most one message may carry")
	assert.Empty(t, j.e.fakeLog())
}

func TestMCPSend_TwoOpenValsOfTheSameNameAreNotGuessedBetween(t *testing.T) {
	j := newSendDemo(t)
	j.e.val("valtwin002", "VAL-demo", "/repo/demo", time.Hour)
	res := j.c.call("ask", map[string]any{"to": "VAL-demo", "message": "hello"})
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "nothing was sent")
	assert.Empty(t, j.e.fakeLog())
}

// Success is the ramo accanto of failure, and it had the same leak: whatever the
// command wrote on stderr went to the model. A non-fatal complaint names paths on this
// machine; only the one warning a client needs is said, in the server's own words.
func TestMCPSend_OnSuccessOnlyTheEchoAndAFixedDuplicateNoteReachTheModel(t *testing.T) {
	j := newSendDemo(t)

	res := j.c.call("ask", map[string]any{"to": "VAL-demo", "message": "NOISE: a message whose send complains about the disk"})
	require.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "sent via the fake")
	for _, leaked := range []string{"/Users/someone", "28286c50", "non-fatal", "outbox", "not a directory"} {
		assert.NotContains(t, res.Raw, leaked)
	}
	assert.Contains(t, j.e.logs.all(), "stderr 2 lines", "the log keeps the sizes")
	assert.NotContains(t, j.e.logs.all(), "/Users/someone", "and not the text")

	res = j.c.call("tell", map[string]any{"to": "VAL-demo", "message": "DUP: the same thing again"})
	require.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), duplicateNote)
	assert.NotContains(t, res.Raw, "msg-0123456789ab", "the command's own sentence, with its id and age, is not forwarded")
	assert.NotContains(t, res.Raw, "went out 3s ago")

	res = j.c.call("tell", map[string]any{"to": "VAL-demo", "message": "an ordinary message"})
	assert.NotContains(t, res.Text(), duplicateNote, "no duplicate warning, no note")
}

// The fake prints the CLI's warning from a copy of its lead; this keeps the copy and the
// constant the server recognises from drifting apart.
func TestFakeCLI_ItsDuplicateWarningUsesTheRealLead(t *testing.T) {
	assert.Contains(t, fakeCLI, dedupWarningLead)
}
