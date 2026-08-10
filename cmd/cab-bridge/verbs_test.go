package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// --- helpers ---------------------------------------------------------------

const (
	replySelf = "escrpl01"
	replyPeer = "valrpl01"
)

// newReplyPair sets up a responder (self) and an asker (peer) sharing a scope.
func newReplyPair(t *testing.T) (*session.Manager, config.Config, string) {
	t.Helper()
	dataDir := t.TempDir()
	cfg := nextTestConfig(dataDir)
	plantOverviewSession(t, dataDir, replySelf, session.RoleEsc, "ESC-reply", "/repo/r", "", "working")
	plantOverviewSession(t, dataDir, replyPeer, session.RoleVal, "VAL-reply", "/repo/r", "", session.StateOrchestrating)
	return newSessionManager(cfg), cfg, dataDir
}

// deliverAndNotify plants a query and marks it NOTIFIED, i.e. an OPEN ASK.
func deliverAndNotify(t *testing.T, mgr *session.Manager, dataDir, id, content string, ts time.Time) {
	t.Helper()
	plantInboxAt(t, dataDir, replySelf, id, replyPeer, message.TypeQuery, content, ts)
	_, err := mgr.CommitWakeCursor(replySelf, []string{id}, time.Now().UTC(), nil, nil)
	require.NoError(t, err)
}

func peerInbox(t *testing.T, dataDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, "sessions", replyPeer, "inbox"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func readDelivered(t *testing.T, dataDir, responseID string) message.Message {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dataDir, "sessions", replyPeer, "inbox", responseID+".json"))
	require.NoError(t, err)
	var m message.Message
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}

func newTxn(sid string, closeIDs []string, content string) *session.ReplyTxn {
	return &session.ReplyTxn{
		SchemaVersion: session.ReplyTxnSchemaVersion,
		ResponseID:    session.DeterministicResponseID(sid, closeIDs[0]),
		To:            replyPeer,
		Anchor:        closeIDs[0],
		CloseIDs:      closeIDs,
		State:         session.ReplyTxnPending,
		Timestamp:     time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC),
		Content:       content,
	}
}

// --- the transaction: the three crash cases the brief asks for --------------

// TestReply_CrashAfterSentBeforeArchive_CompletesWithoutResending is the case
// the journal exists for: the response reached the recipient, the crash landed
// before archiving, and a retry must finish the job WITHOUT delivering twice.
func TestReply_CrashAfterSentBeforeArchive_CompletesWithoutResending(t *testing.T) {
	mgr, cfg, dataDir := newReplyPair(t)
	base := time.Now().UTC()
	deliverAndNotify(t, mgr, dataDir, "msg-aaaaaaaaaaaa", "brief", base)

	txn := newTxn(replySelf, []string{"msg-aaaaaaaaaaaa"}, "here is the report")

	// First attempt: delivers, then "crashes" before archiving.
	delivered, err := deliverResponse(cfg, mgr, replySelf, txn)
	require.NoError(t, err)
	require.True(t, delivered)
	txn.State = session.ReplyTxnSent
	require.NoError(t, mgr.WriteReplyTxn(replySelf, txn))

	require.Len(t, peerInbox(t, dataDir), 1, "exactly one response was delivered")
	firstBody := readDelivered(t, dataDir, txn.ResponseID)

	// The retry resumes from the journal.
	var stdout, stderr bytes.Buffer
	resumed, found, err := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, finishReplyTxn(mgr, cfg, replySelf, resumed, &stdout, &stderr))

	assert.Len(t, peerInbox(t, dataDir), 1, "the retry must NOT deliver a second response")
	assert.Equal(t, firstBody.Content, readDelivered(t, dataDir, txn.ResponseID).Content)

	// And it finished the archiving it had left undone.
	assert.NoFileExists(t, filepath.Join(dataDir, "sessions", replySelf, "inbox", "msg-aaaaaaaaaaaa.json"))
	processed, err := os.ReadDir(filepath.Join(dataDir, "sessions", replySelf, "processed"))
	require.NoError(t, err)
	assert.Len(t, processed, 1)

	_, stillThere, err := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, err)
	assert.False(t, stillThere, "journal removed once complete")
}

// TestReply_CrashBetweenArchives_ResumesFromIndex covers the second gap: two
// asks closed by one reply, crash after archiving the first.
func TestReply_CrashBetweenArchives_ResumesFromIndex(t *testing.T) {
	mgr, cfg, dataDir := newReplyPair(t)
	base := time.Now().UTC()
	deliverAndNotify(t, mgr, dataDir, "msg-aaaaaaaaaaaa", "brief", base)
	deliverAndNotify(t, mgr, dataDir, "msg-bbbbbbbbbbbb", "correction", base.Add(time.Minute))

	txn := newTxn(replySelf, []string{"msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb"}, "covers both")
	_, err := deliverResponse(cfg, mgr, replySelf, txn)
	require.NoError(t, err)

	// Archive only the first, then "crash" with the index at 1.
	inbox := filepath.Join(dataDir, "sessions", replySelf, "inbox")
	processedDir := filepath.Join(dataDir, "sessions", replySelf, "processed")
	require.NoError(t, os.MkdirAll(processedDir, 0o700))
	require.NoError(t, os.Rename(
		filepath.Join(inbox, "msg-aaaaaaaaaaaa.json"),
		filepath.Join(processedDir, "20260808T120000.000000000Z-msg-aaaaaaaaaaaa.json")))
	txn.State = session.ReplyTxnSent
	txn.ArchivedIndex = 1
	require.NoError(t, mgr.WriteReplyTxn(replySelf, txn))

	var stdout, stderr bytes.Buffer
	resumed, _, err := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, err)
	require.NoError(t, finishReplyTxn(mgr, cfg, replySelf, resumed, &stdout, &stderr))

	assert.NoFileExists(t, filepath.Join(inbox, "msg-bbbbbbbbbbbb.json"), "resumed from the index and archived the rest")
	assert.Len(t, peerInbox(t, dataDir), 1, "still exactly one response")
	entries, err := os.ReadDir(processedDir)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "both asks archived, neither twice")
}

// TestReply_AskArrivingAfterTheSnapshotStaysOpen: the set is frozen once, so a
// later ask is NOT swept away unseen — it waits for the next reply.
func TestReply_AskArrivingAfterTheSnapshotStaysOpen(t *testing.T) {
	mgr, cfg, dataDir := newReplyPair(t)
	base := time.Now().UTC()
	deliverAndNotify(t, mgr, dataDir, "msg-aaaaaaaaaaaa", "brief", base)

	txn := newTxn(replySelf, []string{"msg-aaaaaaaaaaaa"}, "answering the first")

	// A third message lands after the snapshot was frozen.
	deliverAndNotify(t, mgr, dataDir, "msg-cccccccccccc", "late correction", base.Add(2*time.Minute))

	var stdout, stderr bytes.Buffer
	require.NoError(t, finishReplyTxn(mgr, cfg, replySelf, txn, &stdout, &stderr))

	assert.FileExists(t, filepath.Join(dataDir, "sessions", replySelf, "inbox", "msg-cccccccccccc.json"),
		"an ask that arrived after the snapshot must stay open")
	// It is no longer open in the NOTIFIED sense, and that is the F-109 change:
	// the reply put it back in line, so the next `next` shows it again instead
	// of it sitting delivered-but-unanswered where nothing returns for it. What
	// this test has always guarded — it is not archived — is the line above.
	cursor, cerr := requireCursor(t, mgr)
	require.NoError(t, cerr)
	assert.True(t, cursor.WasReplayed("msg-cccccccccccc"), "queued for re-delivery, not closed")
	open, err := collectOpenAsks(mgr, cfg, replySelf)
	require.NoError(t, err)
	assert.Empty(t, open, "nothing is NOTIFIED any more: it is waiting to be shown again")
}

// TestReply_ClosesCarriesTheWholeSetAndInReplyToTheAnchor pins the schema
// choice: one inReplyTo (the anchor) plus the full list in closes.
func TestReply_ClosesCarriesTheWholeSetAndInReplyToTheAnchor(t *testing.T) {
	mgr, cfg, dataDir := newReplyPair(t)
	base := time.Now().UTC()
	deliverAndNotify(t, mgr, dataDir, "msg-aaaaaaaaaaaa", "brief", base)
	deliverAndNotify(t, mgr, dataDir, "msg-bbbbbbbbbbbb", "correction", base.Add(time.Minute))

	txn := newTxn(replySelf, []string{"msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb"}, "one answer for both")
	var stdout, stderr bytes.Buffer
	require.NoError(t, finishReplyTxn(mgr, cfg, replySelf, txn, &stdout, &stderr))

	got := readDelivered(t, dataDir, txn.ResponseID)
	require.NotNil(t, got.InReplyTo)
	assert.Equal(t, "msg-aaaaaaaaaaaa", *got.InReplyTo, "inReplyTo carries the anchor, the oldest open ask")
	assert.Equal(t, []string{"msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb"}, got.Closes, "closes carries the whole set")
	assert.Equal(t, message.TypeResponse, got.Type)
	assert.Contains(t, stdout.String(), "closed:", "the echo says what it closed")
}

func TestDeterministicResponseID_IsStableAndDistinct(t *testing.T) {
	t.Parallel()
	a := session.DeterministicResponseID("escaaaaa", "msg-aaaaaaaaaaaa")
	assert.Equal(t, a, session.DeterministicResponseID("escaaaaa", "msg-aaaaaaaaaaaa"), "same inputs, same id — this is what makes the retry idempotent")
	assert.NotEqual(t, a, session.DeterministicResponseID("escbbbbb", "msg-aaaaaaaaaaaa"), "different responder")
	assert.NotEqual(t, a, session.DeterministicResponseID("escaaaaa", "msg-bbbbbbbbbbbb"), "different anchor")
	assert.Regexp(t, `^msg-[a-f0-9]{12}$`, a, "must match the wire format")
}

// --- payload rule -----------------------------------------------------------

func TestResolveMessagePayload(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		arg     string
		hasArg  bool
		stdin   string
		want    string
		wantErr bool
	}{
		{"argument is the message", "hello", true, "", "hello", false},
		{"argument wins and stdin is not read", "hello", true, "SHOULD NOT BE READ", "hello", false},
		{"no argument reads stdin", "", false, "from a pipe", "from a pipe", false},
		{"multiline stdin", "", false, "line1\nline2\n", "line1\nline2\n", false},
		{"empty argument refused", "   ", true, "", "", true},
		{"empty stdin refused", "", false, "  \n", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveMessagePayload(tc.arg, tc.hasArg, strings.NewReader(tc.stdin))
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// --- reply target resolution ------------------------------------------------

func TestResolveReplyTarget(t *testing.T) {
	t.Parallel()
	asks := []openAsk{
		{id: "msg-aaaaaaaaaaaa", from: "valaaaaa", fromName: "VAL-one", when: "2026-08-08T10:00:00Z"},
	}
	twoSenders := append([]openAsk{}, asks...)
	twoSenders = append(twoSenders, openAsk{id: "msg-bbbbbbbbbbbb", from: "cribbbbb", fromName: "CRI-two", when: "2026-08-08T11:00:00Z"})

	t.Run("no_args_infers_the_sole_sender_and_reads_stdin", func(t *testing.T) {
		to, content, err := resolveReplyTarget(nil, asks, nil, strings.NewReader("piped answer"))
		require.NoError(t, err)
		assert.Equal(t, "valaaaaa", to)
		assert.Equal(t, "piped answer", content)
	})

	t.Run("one_arg_is_the_message_when_it_is_not_a_sender_name", func(t *testing.T) {
		to, content, err := resolveReplyTarget([]string{"the answer"}, asks, nil, strings.NewReader(""))
		require.NoError(t, err)
		assert.Equal(t, "valaaaaa", to)
		assert.Equal(t, "the answer", content)
	})

	// The payload rule has no exceptions when there is nothing to disambiguate.
	// This test used to assert the opposite — that an argument matching the only
	// asker's name is a RECIPIENT — which made `reply OK` to a val called `OK`
	// exit 1 with "empty message" while the message sat in argv. A test that
	// pins the behaviour instead of the contract does not notice.
	t.Run("one_arg_is_the_message_even_when_it_matches_the_only_askers_name", func(t *testing.T) {
		to, content, err := resolveReplyTarget([]string{"VAL-one"}, asks, nil, strings.NewReader("from stdin"))
		require.NoError(t, err)
		assert.Equal(t, "valaaaaa", to)
		assert.Equal(t, "VAL-one", content, "one asker, so the argument can only be the message")
	})

	// With SEVERAL open askers the name earns its meaning back: there the
	// argument really is choosing between them, and the message comes on stdin.
	t.Run("one_arg_naming_an_asker_disambiguates_only_when_there_are_several", func(t *testing.T) {
		to, content, err := resolveReplyTarget([]string{"CRI-two"}, twoSenders, nil, strings.NewReader("from stdin"))
		require.NoError(t, err)
		assert.Equal(t, "cribbbbb", to)
		assert.Equal(t, "from stdin", content)
	})

	t.Run("two_args_are_recipient_and_message", func(t *testing.T) {
		to, content, err := resolveReplyTarget([]string{"CRI-two", "answer"}, twoSenders, nil, strings.NewReader(""))
		require.NoError(t, err)
		assert.Equal(t, "cribbbbb", to)
		assert.Equal(t, "answer", content)
	})

	t.Run("bare_reply_with_two_open_askers_is_fail_closed", func(t *testing.T) {
		_, _, err := resolveReplyTarget(nil, twoSenders, nil, strings.NewReader("answer"))
		require.Error(t, err, "never pick silently: the report would reach the wrong agent with no error")
		assert.Contains(t, err.Error(), "VAL-one")
		assert.Contains(t, err.Error(), "CRI-two")
	})

	t.Run("unknown_name_with_two_args_is_refused", func(t *testing.T) {
		_, _, err := resolveReplyTarget([]string{"NOBODY", "answer"}, asks, nil, strings.NewReader(""))
		assert.Error(t, err)
	})
}

// TestCollectOpenAsks_OnlyNotifiedQueries: a tell is never "open", and an
// UNREAD ask is not open either — the tool's state must match what the agent
// has actually been shown.
func TestCollectOpenAsks_OnlyNotifiedQueries(t *testing.T) {
	mgr, cfg, dataDir := newReplyPair(t)
	base := time.Now().UTC()

	deliverAndNotify(t, mgr, dataDir, "msg-aaaaaaaaaaaa", "an ask", base)
	plantInboxAt(t, dataDir, replySelf, "msg-bbbbbbbbbbbb", replyPeer, message.TypeNotify, "a tell", base)
	_, err := mgr.CommitWakeCursor(replySelf, []string{"msg-bbbbbbbbbbbb"}, time.Now().UTC(), nil, nil)
	require.NoError(t, err)
	plantInboxAt(t, dataDir, replySelf, "msg-cccccccccccc", replyPeer, message.TypeQuery, "never shown", base)

	open, err := collectOpenAsks(mgr, cfg, replySelf)
	require.NoError(t, err)
	require.Len(t, open, 1, "only the NOTIFIED query counts")
	assert.Equal(t, "msg-aaaaaaaaaaaa", open[0].id)
}

// --- surface ----------------------------------------------------------------

func TestVerbs_RejectFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func([]string) error
	}{
		{"ask", runAskVerb},
		{"tell", runTell},
		{"reply", runReply},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run([]string{"--to=abc12345"})
			require.Error(t, err, "the verb carries the type; no flags belong here")
			assert.Contains(t, err.Error(), "flags")
		})
	}
}

// TestErrorsDeclareTheAssumedIdentity is F-97: a command that resolves itself
// from the cwd must say WHO it thought it was when it fails, or a caller in the
// wrong directory silently becomes somebody else and reads an error that makes
// no sense from the identity they assumed.
func TestErrorsDeclareTheAssumedIdentity(t *testing.T) {
	mgr, _, dataDir := newReplyPair(t)
	_ = dataDir

	label := whoIThoughtIWas(mgr, replySelf)
	assert.Contains(t, label, replySelf, "the session id must be stated")
	assert.Contains(t, label, "ESC-reply", "and the agent name, which is what makes it readable")

	// Unknown session: still says the id rather than losing the context.
	assert.Contains(t, whoIThoughtIWas(mgr, "nosuch01"), "nosuch01")
}

// TestVerbs_RefuseAnEmptyMessage is the check the VAL's own empty message asks
// for: a send with nothing in it must be refused, not delivered.
//
// An empty message that arrives looks like a delivery failure and costs the
// recipient a round trip to find out it was never a message at all.
func TestVerbs_RefuseAnEmptyMessage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		arg    string
		hasArg bool
		stdin  string
	}{
		{"empty_argument", "", true, ""},
		{"whitespace_argument", "   \n\t ", true, ""},
		{"empty_stdin", "", false, ""},
		{"whitespace_stdin", "", false, "\n\n  \n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveMessagePayload(tc.arg, tc.hasArg, strings.NewReader(tc.stdin))
			require.Error(t, err, "an empty send must be refused explicitly")
			assert.Contains(t, err.Error(), "empty")
		})
	}
}

// TestReply_DoesNotCloseWhatWasNeverShown is the F-34 verification the VAL asked
// for: reply archives only NOTIFIED asks, so a message that arrived after the
// last next cannot be closed without ever having been seen.
func TestReply_DoesNotCloseWhatWasNeverShown(t *testing.T) {
	mgr, cfg, dataDir := newReplyPair(t)
	base := time.Now().UTC()
	deliverAndNotify(t, mgr, dataDir, "msg-aaaaaaaaaaaa", "the brief I read", base)
	// Arrived after the last next: UNREAD, never shown to the agent.
	plantInboxAt(t, dataDir, replySelf, "msg-eeeeeeeeeeee", replyPeer, message.TypeQuery, "urgent correction", base.Add(time.Minute))

	open, err := collectOpenAsks(mgr, cfg, replySelf)
	require.NoError(t, err)
	require.Len(t, open, 1, "an unseen ask is not open")
	assert.Equal(t, "msg-aaaaaaaaaaaa", open[0].id)

	txn := newTxn(replySelf, []string{"msg-aaaaaaaaaaaa"}, "my answer")
	var stdout, stderr bytes.Buffer
	require.NoError(t, finishReplyTxn(mgr, cfg, replySelf, txn, &stdout, &stderr))

	assert.FileExists(t, filepath.Join(dataDir, "sessions", replySelf, "inbox", "msg-eeeeeeeeeeee.json"),
		"the unseen message must survive the reply untouched")
	// And the agent is told it exists, because it answered without knowing.
	assert.Contains(t, stderr.String(), "have not seen yet")
}

// --- CRI diff-gate 1b fixes -------------------------------------------------

// TestReply_SecondInitializerResumesTheFirstJournal is the P0-1 regression.
//
// Two concurrent replies both seeing "no journal" used to build two journals for
// the same anchor and overwrite each other. If the first had already delivered
// its response and died before persisting SENT, recovery read the SECOND journal
// while the response id on disk carried the FIRST one's bytes: create-if-absent
// refused it forever and the ask stayed open with no way back.
func TestReply_SecondInitializerResumesTheFirstJournal(t *testing.T) {
	mgr, _, dataDir := newReplyPair(t)
	// replyRun re-loads the config and resolves the session from the REAL cwd,
	// so without this the call below sends a real message from whatever session
	// owns this directory — and closes that peer's open asks. It did.
	t.Setenv("CAB_DATA_DIR", dataDir)
	deliverAndNotify(t, mgr, dataDir, "msg-aaaaaaaaaaaa", "brief", time.Now().UTC())

	first := newTxn(replySelf, []string{"msg-aaaaaaaaaaaa"}, "the first answer")
	require.NoError(t, mgr.WriteReplyTxn(replySelf, first))

	// A second reply, with entirely different arguments, must NOT replace it.
	var stdout, stderr bytes.Buffer
	err := replyRun([]string{"a completely different answer"}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		// Resolution can legitimately fail here (no session owns the temp dir);
		// the invariant below is what matters and holds either way. What must
		// NEVER happen is that it SUCCEEDS against a real session — the earlier
		// version of this comment tolerated the failure and never considered the
		// success, which is the case that did the damage.
		t.Logf("replyRun returned: %v", err)
	}

	got, found, rerr := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, rerr)
	if found {
		assert.Equal(t, first.ResponseID, got.ResponseID, "the journal must never be replaced by a second initializer")
		assert.Equal(t, "the first answer", got.Content, "the frozen content wins over the retry's arguments")
	} else {
		// Completed: it must have completed the FIRST transaction.
		assert.FileExists(t, filepath.Join(dataDir, "sessions", replyPeer, "inbox", first.ResponseID+".json"))
	}
}

// TestSoleSessionNamed_FailsClosedOnHomonyms is the P1-4 regression: a
// map[name]sessionID silently kept the last writer, and the remediation
// suggested by bare `reply` pointed straight at that silent pick.
func TestSoleSessionNamed_FailsClosedOnHomonyms(t *testing.T) {
	t.Parallel()
	senders := map[string]string{"aaaaaaa1": "VAL-same", "bbbbbbb2": "VAL-same"}

	t.Run("two_sessions_one_name_is_refused", func(t *testing.T) {
		byName := map[string][]string{"VAL-same": {"aaaaaaa1", "bbbbbbb2"}}
		_, err := soleSessionNamed(byName, "VAL-same", senders)
		require.Error(t, err, "the remediation must not be the trap")
		assert.Contains(t, err.Error(), "aaaaaaa1")
		assert.Contains(t, err.Error(), "bbbbbbb2")
	})

	t.Run("single_session_resolves", func(t *testing.T) {
		byName := map[string][]string{"VAL-one": {"aaaaaaa1"}}
		got, err := soleSessionNamed(byName, "VAL-one", senders)
		require.NoError(t, err)
		assert.Equal(t, "aaaaaaa1", got)
	})

	t.Run("unknown_name_is_refused", func(t *testing.T) {
		_, err := soleSessionNamed(map[string][]string{}, "NOBODY", senders)
		assert.Error(t, err)
	})
}

// TestResolveReplyTarget_HomonymsAreAmbiguousInBothForms: neither the bare form
// nor the named form may pick silently.
func TestResolveReplyTarget_HomonymsAreAmbiguousInBothForms(t *testing.T) {
	t.Parallel()
	asks := []openAsk{
		{id: "msg-aaaaaaaaaaaa", from: "aaaaaaa1", fromName: "VAL-same", when: "2026-08-08T10:00:00Z"},
		{id: "msg-bbbbbbbbbbbb", from: "bbbbbbb2", fromName: "VAL-same", when: "2026-08-08T11:00:00Z"},
	}
	_, _, err := resolveReplyTarget(nil, asks, nil, strings.NewReader("answer"))
	assert.Error(t, err, "bare reply with two senders is ambiguous")

	_, _, err = resolveReplyTarget([]string{"VAL-same", "answer"}, asks, nil, strings.NewReader(""))
	assert.Error(t, err, "naming the shared name is ambiguous too — it was the suggested form")
}

// TestReply_CrashWithMultipleAsksAndOneArrivingMidTransaction composes the three
// cases that are covered separately and never together: several open asks from
// one sender, a crash between SENT and archiving, and a NEW ask landing from the
// same sender while the transaction is open.
//
// Each piece is tested on its own; a defect in their interaction would show in
// none of them. What must hold: the retry closes exactly the frozen set, the
// late ask survives untouched, no second response is delivered, and a following
// reply can still close the late one.
func TestReply_CrashWithMultipleAsksAndOneArrivingMidTransaction(t *testing.T) {
	mgr, cfg, dataDir := newReplyPair(t)
	base := time.Now().UTC()

	deliverAndNotify(t, mgr, dataDir, "msg-aaaaaaaaaaaa", "first brief", base)
	deliverAndNotify(t, mgr, dataDir, "msg-bbbbbbbbbbbb", "a correction", base.Add(time.Minute))

	// The set is frozen on the two, the response is delivered, then a crash
	// lands before any archiving.
	txn := newTxn(replySelf, []string{"msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb"}, "one answer for both")
	delivered, err := deliverResponse(cfg, mgr, replySelf, txn)
	require.NoError(t, err)
	require.True(t, delivered)
	txn.State = session.ReplyTxnSent
	require.NoError(t, mgr.WriteReplyTxn(replySelf, txn))

	// A THIRD ask arrives from the same sender while the journal is open.
	deliverAndNotify(t, mgr, dataDir, "msg-cccccccccccc", "urgent third", base.Add(2*time.Minute))

	// The retry resumes the frozen transaction.
	resumed, found, err := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, err)
	require.True(t, found)
	var stdout, stderr bytes.Buffer
	require.NoError(t, finishReplyTxn(mgr, cfg, replySelf, resumed, &stdout, &stderr))

	inbox := filepath.Join(dataDir, "sessions", replySelf, "inbox")
	assert.NoFileExists(t, filepath.Join(inbox, "msg-aaaaaaaaaaaa.json"), "the frozen set is archived")
	assert.NoFileExists(t, filepath.Join(inbox, "msg-bbbbbbbbbbbb.json"))
	assert.FileExists(t, filepath.Join(inbox, "msg-cccccccccccc.json"),
		"an ask that arrived after the snapshot must NOT be closed by a transaction that never saw it")
	assert.Len(t, peerInbox(t, dataDir), 1, "still exactly one response — the retry must not resend")

	// The late ask survives and is back in line for the next `next` (F-109):
	// before that fix it stayed NOTIFIED, which no later delivery ever shows.
	cursor, cerr := requireCursor(t, mgr)
	require.NoError(t, cerr)
	assert.True(t, cursor.WasReplayed("msg-cccccccccccc"))
	open, err := collectOpenAsks(mgr, cfg, replySelf)
	require.NoError(t, err)
	assert.Empty(t, open)

	second := newTxn(replySelf, []string{"msg-cccccccccccc"}, "answering the third")
	assert.NotEqual(t, txn.ResponseID, second.ResponseID, "a different anchor means a different response id")
	var out2, err2 bytes.Buffer
	require.NoError(t, finishReplyTxn(mgr, cfg, replySelf, second, &out2, &err2))

	assert.NoFileExists(t, filepath.Join(inbox, "msg-cccccccccccc.json"), "the late ask is closed by the NEXT reply")
	assert.Len(t, peerInbox(t, dataDir), 2, "two replies, two responses — never one, never three")
}

// The branch next door. The previous fix freed the argument from the RECIPIENT
// branch and it landed in the lookalike guardrail instead — which fires on an
// exact match too, since an exact match is trivially also a resemblance. The
// defect moved rather than closing, and neither the author nor the ratifier saw
// it, because both were looking at the branch the finding named.
//
// Neither of the two tests written with that fix passes through the guardrail,
// which is why they stayed green while the command still failed. This one does.
func TestResolveReplyTarget_ExactNameOfTheOnlyAskerIsTheMessage(t *testing.T) {
	t.Parallel()
	// The asker is literally called "OK", and "OK" is the whole answer.
	asks := []openAsk{{id: "msg-aaaaaaaaaaaa", from: "valaaaaa", fromName: "OK", when: "2026-08-09T10:00:00Z"}}
	known := []string{"OK", "ESC-x"}

	to, content, err := resolveReplyTarget([]string{"OK"}, asks, known, strings.NewReader(""))
	require.NoError(t, err, "an exact match with the only open asker is not a typo")
	assert.Equal(t, "valaaaaa", to)
	assert.Equal(t, "OK", content, "the payload rule holds: the argument IS the message")
}

// And the guardrail still does its job for the case it was written for.
func TestResolveReplyTarget_TypoIsStillCaught(t *testing.T) {
	t.Parallel()
	asks := []openAsk{{id: "msg-aaaaaaaaaaaa", from: "valaaaaa", fromName: "VAL-bridge", when: "2026-08-09T10:00:00Z"}}
	// Case only: nameLookalike is EqualFold, so what it actually catches is a
	// difference in capitals — NOT the `VAL-brige` transposition its own comment
	// cites as the motivating example. Same family as the other three today: the
	// comment describes a behaviour the code does not have.
	_, _, err := resolveReplyTarget([]string{"val-bridge"}, asks, []string{"VAL-bridge"}, strings.NewReader("report"))
	require.Error(t, err, "a near-miss must not silently become the message")
	assert.Contains(t, err.Error(), "looks like")
}

// An exact name that has NO open ask is a third situation, and it used to be
// told "X ... looks like X" — a sentence that contradicts itself, after which
// the reader distrusts the half that was right.
func TestResolveReplyTarget_KnownNameWithoutAnOpenAskSaysSo(t *testing.T) {
	t.Parallel()
	asks := []openAsk{{id: "msg-aaaaaaaaaaaa", from: "valaaaaa", fromName: "VAL-one", when: "2026-08-09T10:00:00Z"}}
	_, _, err := resolveReplyTarget([]string{"CRI-two"}, asks, []string{"VAL-one", "CRI-two"}, strings.NewReader(""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no open ask")
	assert.NotContains(t, err.Error(), "looks like", "it is not a resemblance: it is the name, exactly")
}

// F-105: the guardrail did not cover the case it was written for. Executed, the
// exact line from its own comment sent the typo as the answer, closed the ask
// and exited 0 — the report on stdin never read.
func TestNameLookalike_CatchesTyposNotJustCapitals(t *testing.T) {
	t.Parallel()
	known := []string{"VAL-bridge", "CRI-payload", "OK"}

	for _, tc := range []struct{ in, want, why string }{
		{"VAL-brige", "VAL-bridge", "transposition — the example in the comment"},
		{"VAL-bridg", "VAL-bridge", "a missing letter"},
		{"VAL-bridgee", "VAL-bridge", "one letter too many"},
		{"val-bridge", "VAL-bridge", "capitals, which is all it used to catch"},
	} {
		got, ok := nameLookalike(known, tc.in)
		require.True(t, ok, "%s: %q should be caught", tc.why, tc.in)
		assert.Equal(t, tc.want, got, tc.why)
	}

	// And it must NOT fire on ordinary messages, or people learn to route around
	// it — which is how a guardrail stops being used.
	for _, msg := range []string{"done", "ok thanks", "the report is ready", "no"} {
		_, ok := nameLookalike(known, msg)
		assert.False(t, ok, "%q is a message, not a near-miss", msg)
	}

	// Short names take a tighter threshold: at two edits almost anything matches
	// a two-letter name.
	_, ok := nameLookalike([]string{"OK"}, "no")
	assert.False(t, ok, "on a short name, two edits away is a different word")
}

// End to end on the real path: the typo must NOT become the payload.
func TestResolveReplyTarget_TypoDoesNotBecomeTheAnswer(t *testing.T) {
	t.Parallel()
	asks := []openAsk{{id: "msg-aaaaaaaaaaaa", from: "valaaaaa", fromName: "VAL-bridge", when: "2026-08-09T10:00:00Z"}}
	_, _, err := resolveReplyTarget([]string{"VAL-brige"}, asks, []string{"VAL-bridge"}, strings.NewReader("the report"))
	require.Error(t, err, "nine bytes of typo must not close an ask in place of a report")
	assert.Contains(t, err.Error(), "looks like")
	assert.Contains(t, err.Error(), "VAL-bridge")
}

// --- F-109: a reply closes ONE delivery -------------------------------------

type plantedAsk struct{ id, content string }

// deliverPage plants queries and marks them NOTIFIED in ONE commit — a single
// page of `next`, which is what CommitWakeCursor produces for real
// (wakecursor.go: one instant across the whole page).
//
// deliverAndNotify commits one id at a time, i.e. a DIFFERENT delivery each
// time, and that difference is the entire subject here. The page instant is
// passed in rather than taken from the clock: two commits microseconds apart
// happen to differ, and a test that depends on happening to is not a test.
func deliverPage(t *testing.T, mgr *session.Manager, dataDir string, sent, page time.Time, asks ...plantedAsk) {
	t.Helper()
	ids := make([]string, 0, len(asks))
	for i, a := range asks {
		plantInboxAt(t, dataDir, replySelf, a.id, replyPeer, message.TypeQuery, a.content, sent.Add(time.Duration(i)*time.Second))
		ids = append(ids, a.id)
	}
	_, err := mgr.CommitWakeCursor(replySelf, ids, page, nil, nil)
	require.NoError(t, err)
}

// TestReply_DoesNotCloseAnAskFromALaterDelivery is F-109 as reproduced: a
// "stop, do NOT do A" that arrives while the answer is being written used to be
// archived by the "done A as asked" that never considered it.
func TestReply_DoesNotCloseAnAskFromALaterDelivery(t *testing.T) {
	mgr, cfg, dataDir := newReplyPair(t)
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_SESSION_ID", replySelf)
	base := time.Now().UTC().Add(-time.Hour)

	deliverPage(t, mgr, dataDir, base, base, plantedAsk{"msg-aaaaaaaaaaaa", "do A"})
	// The re-armed waiter hands this one over while the agent is writing.
	deliverPage(t, mgr, dataDir, base.Add(time.Minute), base.Add(time.Minute), plantedAsk{"msg-bbbbbbbbbbbb", "stop, do NOT do A"})

	var stdout, stderr bytes.Buffer
	require.NoError(t, replyRun([]string{"done A as asked"}, strings.NewReader(""), &stdout, &stderr))

	assert.FileExists(t, filepath.Join(dataDir, "sessions", replySelf, "inbox", "msg-bbbbbbbbbbbb.json"),
		"an ask from a later delivery must survive the reply")
	assert.NoFileExists(t, filepath.Join(dataDir, "sessions", replySelf, "inbox", "msg-aaaaaaaaaaaa.json"),
		"the delivery being answered is still closed")

	// Said on both sides: here to the responder, and in `closes` to the asker.
	assert.Contains(t, stdout.String(), "still open")
	assert.Contains(t, stdout.String(), "msg-bbbbbbbbbbbb")
	assert.Contains(t, stdout.String(), "cab-bridge read <id>")

	// And PUT BACK IN LINE, which is what makes the sentence above a mechanism
	// instead of an instruction: a NOTIFIED ask is shown by no later `next`, so
	// leaving it there would move the defect from "archived unread" to "parked
	// where nothing returns for it".
	cursor, cerr := requireCursor(t, mgr)
	assert.False(t, cursor.IsNotified("msg-bbbbbbbbbbbb"), "no longer NOTIFIED: it is queued again")
	assert.True(t, cursor.WasReplayed("msg-bbbbbbbbbbbb"), "and marked so the re-delivery says it is one")
	require.NoError(t, cerr)

	// The mechanism, end to end: the very next collection has it, flagged.
	res, ready, nerr := collectNextPage(mgr, cfg, replySelf, filepath.Join(dataDir, "sessions", replySelf, "inbox"), 1)
	require.NoError(t, nerr)
	require.True(t, ready, "there is something to deliver again")
	require.Len(t, res.page.Messages, 1)
	assert.Equal(t, "msg-bbbbbbbbbbbb", res.page.Messages[0].ID)
	assert.True(t, res.page.Messages[0].Redelivered, "it says it is a re-delivery")
}

func requireCursor(t *testing.T, mgr *session.Manager) (*session.WakeCursor, error) {
	t.Helper()
	c, _, err := mgr.ReadWakeCursor(replySelf)
	return c, err
}

// The cumulative answer must survive: several asks handed over TOGETHER are one
// delivery, and one reply closes all of them.
func TestReply_ClosesTheWholeDeliveryItAnswers(t *testing.T) {
	mgr, cfg, dataDir := newReplyPair(t)
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_SESSION_ID", replySelf)
	base := time.Now().UTC().Add(-time.Hour)

	deliverPage(t, mgr, dataDir, base, base,
		plantedAsk{"msg-aaaaaaaaaaaa", "first"},
		plantedAsk{"msg-bbbbbbbbbbbb", "second"})

	var stdout, stderr bytes.Buffer
	require.NoError(t, replyRun([]string{"one answer for both"}, strings.NewReader(""), &stdout, &stderr))

	open, err := collectOpenAsks(mgr, cfg, replySelf)
	require.NoError(t, err)
	assert.Empty(t, open, "both were shown at once, so both are answered")
	assert.NotContains(t, stdout.String(), "still open")
}

// A join replay MERGES pages, and the contract says so out loud: every open ask
// goes back to UNREAD and the following `next` re-delivers them together, so
// they were seen together and close together. Executable, so the sentence in
// oldestPage's comment cannot drift away from the behaviour.
func TestReply_AfterAJoinReplayTheMergedPageClosesTogether(t *testing.T) {
	mgr, cfg, dataDir := newReplyPair(t)
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_SESSION_ID", replySelf)
	base := time.Now().UTC().Add(-time.Hour)

	deliverPage(t, mgr, dataDir, base, base, plantedAsk{"msg-aaaaaaaaaaaa", "first"})
	deliverPage(t, mgr, dataDir, base.Add(time.Minute), base.Add(time.Minute), plantedAsk{"msg-bbbbbbbbbbbb", "second"})

	// The compact: join puts both back to UNREAD...
	n, err := replayOpenAsks(mgr, cfg, replySelf)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	// ...and the next `next` re-delivers them in one page.
	_, err = mgr.CommitWakeCursor(replySelf, []string{"msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb"}, base.Add(2*time.Minute), nil, nil)
	require.NoError(t, err)

	var stdout, stderr bytes.Buffer
	require.NoError(t, replyRun([]string{"answering what I was shown"}, strings.NewReader(""), &stdout, &stderr))

	open, err := collectOpenAsks(mgr, cfg, replySelf)
	require.NoError(t, err)
	assert.Empty(t, open, "the replay showed them together, so they close together")
}

func TestOldestPage(t *testing.T) {
	t.Parallel()
	p1 := time.Date(2026, 8, 10, 10, 0, 0, 0, time.UTC)
	p2 := p1.Add(time.Minute)

	cases := []struct {
		name   string
		asks   []openAsk
		target string
		want   []string
		why    string
	}{
		{
			name:   "one delivery closes whole",
			asks:   []openAsk{{id: "a", from: "val", page: p1}, {id: "b", from: "val", page: p1}},
			target: "val",
			want:   []string{"a", "b"},
			why:    "shown together, answered together",
		},
		{
			name:   "two deliveries close only the oldest",
			asks:   []openAsk{{id: "a", from: "val", page: p1}, {id: "b", from: "val", page: p2}},
			target: "val",
			want:   []string{"a"},
			why:    "F-109: the later delivery may never have been read",
		},
		{
			name: "the oldest PAGE, not the first in order",
			// Sent first, delivered later: ordering is by the sender's timestamp,
			// so the head of the slice is not necessarily the oldest delivery.
			asks:   []openAsk{{id: "early-sent", from: "val", page: p2}, {id: "late-sent", from: "val", page: p1}},
			target: "val",
			want:   []string{"late-sent"},
			why:    "the page is a minimum, never asks[0]",
		},
		{
			name:   "other senders are untouched",
			asks:   []openAsk{{id: "a", from: "val", page: p1}, {id: "x", from: "cri", page: p1}},
			target: "val",
			want:   []string{"a"},
			why:    "a reply goes to one agent",
		},
		{
			name:   "nobody to answer",
			asks:   []openAsk{{id: "x", from: "cri", page: p1}},
			target: "val",
			want:   nil,
			why:    "no panic on an empty set — the caller turns this into an error",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, oldestPage(tc.asks, tc.target), tc.why)
		})
	}
}

// --- F-113: an argument AND redirected input is a contradiction -------------

// TestStdinIsRedirected covers every shape stdin actually takes, measured with a
// probe before the guard was written. The harness case is the one that decides
// the design: there stdin is a SOCKET, so "is it a tty" answers nothing and the
// question has to be "did somebody point content at it".
func TestStdinIsRedirected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	withContent := filepath.Join(dir, "report.md")
	require.NoError(t, os.WriteFile(withContent, []byte("the real report"), 0o600))
	empty := filepath.Join(dir, "empty.md")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))

	openFile := func(p string) *os.File {
		f, err := os.Open(p)
		require.NoError(t, err)
		t.Cleanup(func() { _ = f.Close() })
		return f
	}

	assert.True(t, stdinIsRedirected(openFile(withContent)), "a file with bytes is content somebody meant to send")
	assert.False(t, stdinIsRedirected(openFile(empty)), "an empty file has nothing to throw away")

	devNull := openFile(os.DevNull)
	assert.False(t, stdinIsRedirected(devNull), "/dev/null is a char device, not content")

	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	assert.True(t, stdinIsRedirected(r), "a pipe counts whatever its buffer says right now: a slow producer would read as empty")

	assert.False(t, stdinIsRedirected(strings.NewReader("injected")), "an injected reader is not a redirection")
}

// The F-113 regression at the level where every verb passes through.
func TestResolveMessagePayload_ArgumentPlusRedirectedInputIsRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	report := filepath.Join(dir, "report.md")
	require.NoError(t, os.WriteFile(report, []byte("IL REPORT VERO, lungo e importante"), 0o600))
	f, err := os.Open(report)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	_, err = resolveMessagePayload("VAL-x", true, f)
	require.Error(t, err, "five bytes of name must not replace the report and exit 0")
	assert.Contains(t, err.Error(), "nothing was sent")
	assert.Contains(t, err.Error(), "< file.md", "and it names the form that works")

	// The complement, which is the whole reason the guard is narrow: with NO
	// argument the same redirected file is exactly what the caller meant.
	_, err = f.Seek(0, 0)
	require.NoError(t, err)
	got, err := resolveMessagePayload("", false, f)
	require.NoError(t, err)
	assert.Equal(t, "IL REPORT VERO, lungo e importante", got)
}

// F-105 must not come back: with nothing redirected, the argument IS the
// message, even when it happens to be a peer's name. The two fixes are the two
// readings of one input, and this pins that the second did not undo the first.
func TestResolveMessagePayload_ArgumentStillWinsWithoutRedirection(t *testing.T) {
	t.Parallel()
	got, err := resolveMessagePayload("VAL-x", true, strings.NewReader(""))
	require.NoError(t, err)
	assert.Equal(t, "VAL-x", got, "a val called VAL-x can still be answered with the word VAL-x")

	devNull, err := os.Open(os.DevNull)
	require.NoError(t, err)
	defer func() { _ = devNull.Close() }()
	got, err = resolveMessagePayload("OK", true, devNull)
	require.NoError(t, err)
	assert.Equal(t, "OK", got, "and /dev/null is not content either")
}
