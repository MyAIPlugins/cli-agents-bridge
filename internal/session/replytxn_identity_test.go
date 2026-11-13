package session

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-176: the journal of `reply` is written and deleted only under the session lock and only
// when the file on disk is still the transaction the caller is carrying. This file holds the
// rules of "still the same transaction" (SameTransaction) and of the two operations that use
// it (UpdateReplyTxn, CompleteReplyTxn).

func frozenTxn() *ReplyTxn {
	return &ReplyTxn{
		ResponseID: "msg-0123456789ab",
		To:         "valdemo01",
		Anchor:     "msg-aaaaaaaaaaaa",
		CloseIDs:   []string{"msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb"},
		State:      ReplyTxnPending,
		Timestamp:  time.Date(2026, 11, 12, 10, 0, 0, 123456789, time.UTC),
		Content:    "the frozen answer",
	}
}

// frozenFieldChanges is one change per frozen field of frozenTxn (and per way a list can differ):
// each one, applied alone, makes another transaction. It is shared by the test of the identity
// and by the two operations that use it, so that a field cannot be in one and forgotten in the
// other.
func frozenFieldChanges() map[string]func(*ReplyTxn) {
	return map[string]func(*ReplyTxn){
		"ResponseID": func(x *ReplyTxn) { x.ResponseID = "msg-ffffffffffff" },
		"To":         func(x *ReplyTxn) { x.To = "valother1" },
		"Anchor":     func(x *ReplyTxn) { x.Anchor = "msg-bbbbbbbbbbbb" },
		"CloseIDs, one id": func(x *ReplyTxn) {
			x.CloseIDs = []string{"msg-aaaaaaaaaaaa", "msg-cccccccccccc"}
		},
		"CloseIDs, another order": func(x *ReplyTxn) {
			x.CloseIDs = []string{"msg-bbbbbbbbbbbb", "msg-aaaaaaaaaaaa"}
		},
		"CloseIDs, shorter": func(x *ReplyTxn) { x.CloseIDs = x.CloseIDs[:1] },
		"CloseIDs, longer":  func(x *ReplyTxn) { x.CloseIDs = append(x.CloseIDs, "msg-cccccccccccc") },
		"Timestamp":         func(x *ReplyTxn) { x.Timestamp = x.Timestamp.Add(time.Nanosecond) },
		"Content":           func(x *ReplyTxn) { x.Content = "another answer" },
	}
}

func TestSameTransaction_EveryFrozenFieldIsPartOfTheIdentity(t *testing.T) {
	require.True(t, frozenTxn().SameTransaction(frozenTxn()), "premise: two identical transactions are the same")
	cases := frozenFieldChanges()
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			other := frozenTxn()
			change(other)
			assert.False(t, frozenTxn().SameTransaction(other), "a change to %s must make it another transaction", name)
			assert.False(t, other.SameTransaction(frozenTxn()), "and the answer does not depend on which side asks")
		})
	}
}

// Progress is what a driver changes while it works on ITS transaction: two copies at different
// points of the same transaction are the same transaction.
func TestSameTransaction_ProgressIsNotIdentity(t *testing.T) {
	ahead := frozenTxn()
	ahead.State = ReplyTxnSent
	ahead.ArchivedIndex = 2
	ahead.SchemaVersion = ReplyTxnSchemaVersion
	assert.True(t, frozenTxn().SameTransaction(ahead))
	assert.True(t, ahead.SameTransaction(frozenTxn()))
}

func TestSameTransaction_TheSameInstantIsTheSameTimestamp(t *testing.T) {
	base := frozenTxn()
	inZone := frozenTxn()
	inZone.Timestamp = base.Timestamp.In(time.FixedZone("UTC+1", 3600))
	require.NotEqual(t, base.Timestamp.Location(), inZone.Timestamp.Location(), "premise: the zones differ")
	assert.True(t, base.SameTransaction(inZone), "a zone is not an instant")

	withMono := frozenTxn()
	withMono.Timestamp = time.Now()
	withoutMono := frozenTxn()
	withoutMono.Timestamp = withMono.Timestamp.Round(0)
	assert.True(t, withMono.SameTransaction(withoutMono), "a monotonic reading is not an instant either")
}

func TestSameTransaction_NilAndEmptyCloseIDsAreTheSame(t *testing.T) {
	a, b := frozenTxn(), frozenTxn()
	a.CloseIDs = nil
	b.CloseIDs = []string{}
	assert.True(t, a.SameTransaction(b))
}

// The premise of the identity check is that a transaction read back from the disk is the
// transaction that was written. Everything that goes through encoding/json is covered here;
// the one input that is not (text that is not UTF-8) is refused before a journal exists.
func TestReplyTxn_IsTheSameTransactionAfterARoundTripThroughTheDisk(t *testing.T) {
	big := strings.Repeat("0123456789abcdef", 200*1024/16)
	texts := map[string]string{
		"plain":                   "a plain answer",
		"emoji":                   "done \U0001F680 and \U0001F44D",
		"html-looking":            "<b>5 & 6</b> > 4",
		"line separator":          "a b c",
		"newlines and tabs":       "line1\nline2\r\n\tindented",
		"a real U+FFFD":           "r�x",
		"quotes and backslashes":  `he said "x" and \y\`,
		"200 KB":                  big,
		"control characters":      "bell\x07 and nul-adjacent \x01\x1f",
		"combining and non-latin": "é è 中文 ال",
	}
	for name, text := range texts {
		t.Run(name, func(t *testing.T) {
			mgr, sid := registeredSession(t)
			txn := frozenTxn()
			txn.Content = text
			require.NoError(t, mgr.WriteReplyTxn(sid, txn))
			got, found, err := mgr.ReadReplyTxn(sid)
			require.NoError(t, err)
			require.True(t, found)
			assert.True(t, txn.SameTransaction(got), "what was written is what is read back")
			assert.Equal(t, text, got.Content)
		})
	}

	t.Run("a timestamp with a zone and a monotonic reading", func(t *testing.T) {
		mgr, sid := registeredSession(t)
		txn := frozenTxn()
		txn.Timestamp = time.Now().In(time.FixedZone("UTC-5", -5*3600))
		require.NoError(t, mgr.WriteReplyTxn(sid, txn))
		got, _, err := mgr.ReadReplyTxn(sid)
		require.NoError(t, err)
		assert.True(t, txn.SameTransaction(got))
	})
}

// A journal written by the version before this one has the same format: it is read, judged
// and completed like any other. The JSON below is what WriteReplyTxn writes on 57bd93c.
func TestReplyTxn_AJournalOfThePreviousVersionIsResumedAndCompleted(t *testing.T) {
	mgr, sid := registeredSession(t)
	require.NoError(t, os.WriteFile(mgr.replyTxnPath(sid), []byte(v1JournalLiteral), 0o600))

	onDisk, found, err := mgr.ReadReplyTxn(sid)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, ReplyTxnPending, onDisk.State)
	assert.Equal(t, "the frozen answer", onDisk.Content)

	mine := *onDisk
	mine.State = ReplyTxnSent
	require.NoError(t, mgr.UpdateReplyTxn(sid, &mine))
	require.NoError(t, mgr.CompleteReplyTxn(sid, &mine))
	_, found, err = mgr.ReadReplyTxn(sid)
	require.NoError(t, err)
	assert.False(t, found)
}

// A journal that an older version wrote with a text that had already been converted to U+FFFD
// is a valid journal: the comparison is on the form it has on disk, which is the form it is
// read back in.
func TestReplyTxn_AJournalWithATextAlreadyConvertedToReplacementCharactersIsAccepted(t *testing.T) {
	mgr, sid := registeredSession(t)
	raw := strings.Replace(v1JournalLiteral, "the frozen answer", "r�x", 1)
	require.NotEqual(t, v1JournalLiteral, raw, "premise: the text was replaced")
	require.NoError(t, os.WriteFile(mgr.replyTxnPath(sid), []byte(raw), 0o600))

	onDisk, found, err := mgr.ReadReplyTxn(sid)
	require.NoError(t, err)
	require.True(t, found)
	mine := *onDisk
	mine.State = ReplyTxnSent
	require.NoError(t, mgr.UpdateReplyTxn(sid, &mine))
	require.NoError(t, mgr.CompleteReplyTxn(sid, &mine))
}

const v1JournalLiteral = `{
  "schemaVersion": 1,
  "responseId": "msg-0123456789ab",
  "to": "valdemo01",
  "anchor": "msg-aaaaaaaaaaaa",
  "closeIds": [
    "msg-aaaaaaaaaaaa"
  ],
  "state": "pending",
  "archivedIndex": 0,
  "timestamp": "2026-11-12T10:00:00.123456789Z",
  "content": "the frozen answer"
}`

func TestUpdateReplyTxn_WritesTheProgressOfItsOwnTransaction(t *testing.T) {
	mgr, sid := registeredSession(t)
	txn := frozenTxn()
	require.NoError(t, mgr.WriteReplyTxn(sid, txn))

	txn.State = ReplyTxnSent
	txn.ArchivedIndex = 1
	require.NoError(t, mgr.UpdateReplyTxn(sid, txn))

	got, found, err := mgr.ReadReplyTxn(sid)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, ReplyTxnSent, got.State)
	assert.Equal(t, 1, got.ArchivedIndex)
	assert.True(t, txn.SameTransaction(got))
}

// Two drivers of the SAME transaction can be at different points. The one that is behind
// overwrites the progress of the one that is ahead; that is declared and harmless, because
// archiving an ask that is already archived is a success (cmd/cab-bridge, F-176), and it is
// pinned here so that nobody takes it for an accident.
func TestUpdateReplyTxn_ADriverBehindOverwritesTheProgressOfADriverAhead(t *testing.T) {
	mgr, sid := registeredSession(t)
	ahead := frozenTxn()
	ahead.State = ReplyTxnSent
	ahead.ArchivedIndex = 2
	require.NoError(t, mgr.WriteReplyTxn(sid, ahead))

	behind := frozenTxn()
	behind.State = ReplyTxnSent
	behind.ArchivedIndex = 1
	require.NoError(t, mgr.UpdateReplyTxn(sid, behind))

	got, _, err := mgr.ReadReplyTxn(sid)
	require.NoError(t, err)
	assert.Equal(t, 1, got.ArchivedIndex)
}

func TestCompleteReplyTxn_RemovesItsOwnJournal(t *testing.T) {
	mgr, sid := registeredSession(t)
	txn := frozenTxn()
	require.NoError(t, mgr.WriteReplyTxn(sid, txn))
	require.NoError(t, mgr.CompleteReplyTxn(sid, txn))
	assert.NoFileExists(t, mgr.replyTxnPath(sid))
}

// Both operations, for the two ways the file can fail to be the caller's: it is not there,
// and it is another transaction's. In neither case is anything written: not a new file, not
// a changed byte, not a removal.
func TestReplyTxnOperations_RefuseAJournalThatIsNotTheirsAndTouchNothing(t *testing.T) {
	ops := map[string]func(*Manager, string, *ReplyTxn) error{
		"UpdateReplyTxn":   (*Manager).UpdateReplyTxn,
		"CompleteReplyTxn": (*Manager).CompleteReplyTxn,
	}
	for opName, op := range ops {
		t.Run(opName+"/gone", func(t *testing.T) {
			mgr, sid := registeredSession(t)
			err := op(mgr, sid, frozenTxn())
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrReplyTxnGone), "got: %v", err)
			assert.NoFileExists(t, mgr.replyTxnPath(sid), "the journal was not recreated")
		})
		// refusesAJournalOnDisk writes `other` as the journal on disk, asks the operation to act
		// as the owner of frozenTxn(), and checks it is refused and the file is untouched.
		refusesAJournalOnDisk := func(t *testing.T, other *ReplyTxn) {
			mgr, sid := registeredSession(t)
			require.NoError(t, mgr.WriteReplyTxn(sid, other))
			before, rerr := os.ReadFile(mgr.replyTxnPath(sid))
			require.NoError(t, rerr)

			err := op(mgr, sid, frozenTxn())
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrReplyTxnNotYours), "got: %v", err)
			assert.False(t, errors.Is(err, ErrReplyTxnGone))

			after, rerr := os.ReadFile(mgr.replyTxnPath(sid))
			require.NoError(t, rerr, "the other transaction's journal is still there")
			assert.Equal(t, string(before), string(after), "and byte for byte what it was")
		}
		t.Run(opName+"/another transaction", func(t *testing.T) {
			other := frozenTxn()
			other.ResponseID = "msg-ffffffffffff"
			other.Anchor = "msg-cccccccccccc"
			other.CloseIDs = []string{"msg-cccccccccccc"}
			refusesAJournalOnDisk(t, other)
		})
		// F-176 (CRI2, N1): the BAR says two journals for the same question have the same id, so
		// the id alone is not the identity. A journal that differs from the caller's in ONE
		// frozen field, whichever, is another transaction: the operation must see it.
		for field, change := range frozenFieldChanges() {
			t.Run(opName+"/another transaction, differing only in "+field, func(t *testing.T) {
				other := frozenTxn()
				change(other)
				refusesAJournalOnDisk(t, other)
			})
		}
		t.Run(opName+"/unreadable", func(t *testing.T) {
			mgr, sid := registeredSession(t)
			require.NoError(t, os.WriteFile(mgr.replyTxnPath(sid), []byte("{not json"), 0o600))
			err := op(mgr, sid, frozenTxn())
			require.Error(t, err)
			assert.False(t, errors.Is(err, ErrReplyTxnGone) || errors.Is(err, ErrReplyTxnNotYours),
				"an unreadable journal is not a verdict about whose it is: %v", err)
			after, rerr := os.ReadFile(mgr.replyTxnPath(sid))
			require.NoError(t, rerr)
			assert.Equal(t, "{not json", string(after), "and it is left as it was, for a person to look at")
		})
	}
}

// The words of the two errors are read by a person who has to decide what to do next, so they
// are part of the contract: they say what this check did (nothing), they do not claim the
// command as a whole did nothing, and they do not say who removed the file.
func TestReplyTxnErrors_SayWhatTheCheckDidAndNothingElse(t *testing.T) {
	for name, err := range map[string]error{"gone": ErrReplyTxnGone, "not yours": ErrReplyTxnNotYours} {
		text := err.Error()
		assert.Contains(t, text, "this check", "%s: the missing write is attributed to the check", name)
		assert.Contains(t, text, "may already have been delivered", "%s: it does not deny the effects that came before", name)
		assert.NotContains(t, text, "nothing else", "%s", name)
		assert.NotContains(t, text, "nothing was sent", "%s", name)
		assert.NotContains(t, strings.ToLower(text), "removed by", "%s: it does not say who removed the file", name)
		assert.NotContains(t, strings.ToLower(text), "another run", "%s", name)
	}
}

// The wait for the lock is the lock's, not the operations': neither of the two sleeps or polls
// on its own, so a normal reply does not get longer (criterion 4). Declared structurally, the
// way the repository declares such rules, so that a later edit cannot add a wait unseen.
func TestReplyTxnOperations_DoNotWaitOnTheirOwn(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "replytxn.go", nil, parser.SkipObjectResolution)
	require.NoError(t, err)
	var sleeps []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" && (sel.Sel.Name == "Sleep" || sel.Sel.Name == "After" || sel.Sel.Name == "Tick" || sel.Sel.Name == "NewTimer") {
			sleeps = append(sleeps, "time."+sel.Sel.Name)
		}
		return true
	})
	assert.Empty(t, sleeps, "replytxn.go waits on its own: the only wait of a reply is the session lock's")
}

// Whoever holds the lock decides. A child process holds the session lock (the lock is
// re-entrant inside one process, so only another process proves exclusion); both operations
// fail with the lock's error, within the wait of the manager, and leave the journal exactly as
// it was; once the child lets go they succeed.
func TestReplyTxnOperations_WaitForTheSessionLockAndChangeNothingWithoutIt(t *testing.T) {
	dataDir, sid := exclSession(t)
	mgr := NewManager(dataDir, time.Second)
	mgr.LockWait = 300 * time.Millisecond // the second argument is the heartbeat, not the wait for the lock
	txn := frozenTxn()
	require.NoError(t, mgr.WriteReplyTxn(sid, txn))
	before, err := os.ReadFile(mgr.replyTxnPath(sid))
	require.NoError(t, err)

	holder := startExclHolder(t, dataDir, sid, 0)

	progressed := frozenTxn()
	progressed.State = ReplyTxnSent
	progressed.ArchivedIndex = 1
	for name, op := range map[string]func() error{
		"UpdateReplyTxn":   func() error { return mgr.UpdateReplyTxn(sid, progressed) },
		"CompleteReplyTxn": func() error { return mgr.CompleteReplyTxn(sid, txn) },
	} {
		start := time.Now()
		opErr := op()
		require.Error(t, opErr, "%s must not act without the lock", name)
		assert.Contains(t, opErr.Error(), "session lock", "%s: it is the lock that refused", name)
		assert.Less(t, time.Since(start), 5*time.Second, "%s: it waited for the manager's LockWait and no longer", name)
		assert.ErrorIs(t, opErr, ErrLockHeld, "%s: the refusal is the lock's own", name)
		after, rerr := os.ReadFile(mgr.replyTxnPath(sid))
		require.NoError(t, rerr, "%s: the journal is still there", name)
		assert.Equal(t, string(before), string(after), "%s: and byte for byte what it was", name)
	}

	holder.release(t)
	require.NoError(t, settleLock(mgr.lockBase(sid)))

	require.NoError(t, mgr.UpdateReplyTxn(sid, progressed), "with the lock free the update goes through")
	got, _, err := mgr.ReadReplyTxn(sid)
	require.NoError(t, err)
	assert.Equal(t, 1, got.ArchivedIndex)
	require.NoError(t, mgr.CompleteReplyTxn(sid, progressed))
	assert.NoFileExists(t, mgr.replyTxnPath(sid))
}

// F-178: the error of a journal that is not the caller's says WHAT differs. When the response id is
// the caller's own, "the journal on disk is for response <that id>" pointed at nothing: the id was
// the same. Now it says the id is the same and names the fields that were frozen differently under
// it, and never shows what they hold: an error goes to a log and, on the command line, to a
// terminal, and a response text may be sensitive.
func TestChecksOfTheJournal_TheErrorNamesWhatDiffersAndNeverShowsIt(t *testing.T) {
	label := map[string]string{
		"To":                      "recipient",
		"Anchor":                  "anchor",
		"CloseIDs, one id":        "asks to close",
		"CloseIDs, another order": "asks to close",
		"CloseIDs, shorter":       "asks to close",
		"CloseIDs, longer":        "asks to close",
		"Timestamp":               "instant",
		"Content":                 "text",
	}
	for name, change := range frozenFieldChanges() {
		t.Run(name, func(t *testing.T) {
			mgr, sid := registeredSession(t)
			onDisk := frozenTxn()
			change(onDisk)
			require.NoError(t, mgr.WriteReplyTxn(sid, onDisk))

			for op, run := range map[string]func() error{
				"UpdateReplyTxn":   func() error { return mgr.UpdateReplyTxn(sid, frozenTxn()) },
				"CompleteReplyTxn": func() error { return mgr.CompleteReplyTxn(sid, frozenTxn()) },
			} {
				err := run()
				require.ErrorIs(t, err, ErrReplyTxnNotYours, op)
				text := err.Error()
				if name == "ResponseID" {
					assert.Contains(t, text, "the journal on disk is for response msg-ffffffffffff", "%s: another id is said as it always was", op)
					continue
				}
				want, ok := label[name]
				require.True(t, ok, "a change in frozenFieldChanges with no label here: %q", name)
				assert.Contains(t, text, "has the same response id "+frozenTxn().ResponseID, "%s", op)
				assert.Contains(t, text, "differs in: "+want, "%s: it names the field that differs", op)
				assert.NotContains(t, text, "is for response", "%s: that line sends the reader looking for another reply", op)
				for _, value := range []string{onDisk.Content, onDisk.To, onDisk.Anchor, "msg-cccccccccccc"} {
					assert.NotContains(t, text, value, "%s: a field is named, its value is never shown", op)
				}
			}
		})
	}
}

// More than one field can differ; the error lists them all, once each, in a fixed order.
func TestChecksOfTheJournal_TheErrorListsEveryFieldThatDiffers(t *testing.T) {
	mgr, sid := registeredSession(t)
	onDisk := frozenTxn()
	onDisk.Content = "another answer"
	onDisk.Timestamp = onDisk.Timestamp.Add(time.Second)
	onDisk.To = "valother1"
	require.NoError(t, mgr.WriteReplyTxn(sid, onDisk))

	err := mgr.UpdateReplyTxn(sid, frozenTxn())
	require.ErrorIs(t, err, ErrReplyTxnNotYours)
	assert.Contains(t, err.Error(), "differs in: recipient, instant, text")
}
