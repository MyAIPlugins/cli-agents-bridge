package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// F-176, criterion 8: a `reply` whose text is not valid UTF-8 is refused with an error BEFORE
// any effect. The journal is written as JSON, which turns every invalid byte into U+FFFD, so
// the text in memory and the text on disk would differ, and the identity check that guards the
// journal would then refuse the very journal this command had just created.
//
// The rule is "every text `reply` reads is checked, and what it does not read is not". An
// argument is always read, so it is checked always, even while a journal is in flight and the
// argument would be thrown away. Stdin is read only when a NEW reply has to be built; during a
// recovery it is not read at all (reading it would block a `reply` with no text that resumes
// the journal), so an invalid stdin is not a refusal that was missed.

const badText = "r\xffx" // 72 ff 78: not UTF-8

// askSenderName is the name the planted ask carries as its sender (plantInboxAt), which is the
// recipient a `reply NAME ...` has to name; it is not the name in the sender's manifest.
const askSenderName = "ESC-y"

// countingReader is a stdin that counts how many times it is read.
type countingReader struct {
	data  string
	reads int
	r     *strings.Reader
}

func (c *countingReader) Read(p []byte) (int, error) {
	c.reads++
	if c.r == nil {
		c.r = strings.NewReader(c.data)
	}
	return c.r.Read(p)
}

// openAskNoJournal is the scene of a first creation: one open ask, no journal.
func openAskNoJournal(t *testing.T) (data string) {
	t.Helper()
	mgr, _, data := newReplyPair(t)
	t.Setenv("CAB_SESSION_ID", replySelf)
	deliverAndNotify(t, mgr, data, rcvAskA, "ask", newTxn(replySelf, []string{rcvAskA}, "x").Timestamp)
	return data
}

// journalInFlight is the scene of a recovery: the ask is open and a pending journal for it
// is on disk, nothing delivered yet.
func journalInFlight(t *testing.T) (data string, journalBytes []byte) {
	t.Helper()
	mgr, _, data := newReplyPair(t)
	t.Setenv("CAB_SESSION_ID", replySelf)
	j := newTxn(replySelf, []string{rcvAskA}, rcvFrozen)
	deliverAndNotify(t, mgr, data, rcvAskA, "ask", j.Timestamp)
	require.NoError(t, mgr.WriteReplyTxn(replySelf, j))
	raw, err := os.ReadFile(replyJournalPath(data))
	require.NoError(t, err)
	return data, raw
}

// assertNothingHappened is the oracle of a refusal: no response was delivered, the ask is
// where it was, and nothing was archived.
func assertNothingHappened(t *testing.T, data string) {
	t.Helper()
	assert.Empty(t, peerInbox(t, data), "nothing was delivered")
	assert.FileExists(t, filepath.Join(data, "sessions", replySelf, "inbox", rcvAskA+".json"), "the ask is still open in the inbox")
	assert.Equal(t, 0, askArchivedCount(t, data, rcvAskA), "the ask was not archived")
}

func TestReplyRun_ATextThatIsNotUTF8IsRefusedBeforeAnyEffect(t *testing.T) {
	require.False(t, utf8.ValidString(badText), "premise: the text is not valid UTF-8")
	cases := []struct {
		name  string
		args  []string
		stdin string
	}{
		{"as the argument", []string{badText}, ""},
		{"on stdin", nil, badText},
		{"as the argument after the name of the recipient", []string{askSenderName, badText}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := openAskNoJournal(t)
			var stdout, stderr bytes.Buffer
			err := replyRun(tc.args, strings.NewReader(tc.stdin), &stdout, &stderr)
			require.Error(t, err, "the reply must be refused")
			assert.Contains(t, err.Error(), "UTF-8")
			assert.Contains(t, err.Error(), "offset 1", "it says where the first bad byte is")
			assert.NoFileExists(t, replyJournalPath(data), "no journal was created")
			assertNothingHappened(t, data)
		})
	}
}

// TestReplyRun_AnArgumentThatIsNotUTF8IsRefusedEvenDuringARecovery: the argument was passed, so
// it is checked, although the recovery would deliver the frozen answer and drop it. The journal
// is byte for byte what it was.
func TestReplyRun_AnArgumentThatIsNotUTF8IsRefusedEvenDuringARecovery(t *testing.T) {
	data, before := journalInFlight(t)
	var stdout, stderr bytes.Buffer
	err := replyRun([]string{badText}, strings.NewReader(""), &stdout, &stderr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "UTF-8")

	after, rerr := os.ReadFile(replyJournalPath(data))
	require.NoError(t, rerr)
	assert.Equal(t, string(before), string(after), "the journal in flight is untouched")
	assertNothingHappened(t, data)
}

// TestReplyRun_AnInvalidStdinIsNotReadDuringARecovery: stdin is not read while a journal is in
// flight, so what it holds is not a refusal that was missed. The recovery delivers the frozen
// answer.
func TestReplyRun_AnInvalidStdinIsNotReadDuringARecovery(t *testing.T) {
	data, _ := journalInFlight(t)
	stdin := &countingReader{data: badText}
	var stdout, stderr bytes.Buffer
	require.NoError(t, replyRun(nil, stdin, &stdout, &stderr))

	assert.Equal(t, 0, stdin.reads, "stdin was not read")
	assert.Equal(t, rcvFrozen, readDelivered(t, data, session.DeterministicResponseID(replySelf, rcvAskA)).Content, "the frozen answer was delivered")
	assert.NoFileExists(t, replyJournalPath(data))
	assert.Equal(t, 1, askArchivedCount(t, data, rcvAskA))
}

// TestReplyRun_NoTextDuringARecoveryDoesNotWaitForStdin: a `reply` with no text that resumes a
// journal never touches stdin.
func TestReplyRun_NoTextDuringARecoveryDoesNotWaitForStdin(t *testing.T) {
	data, _ := journalInFlight(t)
	stdin := &countingReader{}
	var stdout, stderr bytes.Buffer
	require.NoError(t, replyRun(nil, stdin, &stdout, &stderr))

	assert.Equal(t, 0, stdin.reads, "stdin was not read")
	assert.Equal(t, rcvFrozen, readDelivered(t, data, session.DeterministicResponseID(replySelf, rcvAskA)).Content)
	assert.NoFileExists(t, replyJournalPath(data))
}

// TestReplyRun_AValidArgumentFollowsTheJournalPrecedence: the check does not change who wins. A
// valid argument is dropped during a recovery and used by a first creation, as before.
func TestReplyRun_AValidArgumentFollowsTheJournalPrecedence(t *testing.T) {
	t.Run("recovery: the frozen answer", func(t *testing.T) {
		data, _ := journalInFlight(t)
		var stdout, stderr bytes.Buffer
		require.NoError(t, replyRun([]string{"typed after the crash"}, strings.NewReader(""), &stdout, &stderr))
		assert.Equal(t, rcvFrozen, readDelivered(t, data, session.DeterministicResponseID(replySelf, rcvAskA)).Content)
	})
	t.Run("first creation, recipient named: the typed answer", func(t *testing.T) {
		data := openAskNoJournal(t)
		var stdout, stderr bytes.Buffer
		require.NoError(t, replyRun([]string{askSenderName, "typed answer, valid"}, strings.NewReader(""), &stdout, &stderr))
		assert.Equal(t, "typed answer, valid", readDelivered(t, data, session.DeterministicResponseID(replySelf, rcvAskA)).Content)
	})
	t.Run("first creation: the typed answer", func(t *testing.T) {
		data := openAskNoJournal(t)
		var stdout, stderr bytes.Buffer
		require.NoError(t, replyRun([]string{"typed answer, valid"}, strings.NewReader(""), &stdout, &stderr))
		assert.Equal(t, "typed answer, valid", readDelivered(t, data, session.DeterministicResponseID(replySelf, rcvAskA)).Content)
	})
}
