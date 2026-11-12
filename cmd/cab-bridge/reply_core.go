package main

import (
	"errors"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/routing"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// replySelector builds a NEW reply transaction: it decides what is being closed,
// who is being answered and with what. It is called by beginReply only when no
// journal exists, and always inside the session lock, so whatever it reads is the
// state the journal will freeze.
//
// The CLI selector (cliReplySelector) works from the NOTIFIED asks and keeps the
// whole disambiguation of the verb. Another front-end supplies its own: the
// journal, its precedence and its persistence are what they share, and nothing
// else.
type replySelector func() (*session.ReplyTxn, error)

// replyGate decides whether a transaction that still has to DELIVER may go on.
// It is handed the EFFECTIVE transaction: the one already in flight if there is
// one, not whatever the caller asked for this time. A nil gate allows everything.
type replyGate func(txn *session.ReplyTxn) error

// errReplyBlocked marks a reply that a gate refused. The transaction, if one was
// already persisted, is left exactly as it was: blocking is not discarding.
var errReplyBlocked = errors.New("reply blocked")

// beginReply returns the transaction `reply` must drive to completion: the one
// already in flight if there is one, otherwise a fresh one built by sel and
// persisted before it returns.
//
// EVERYTHING up to and including the journal's creation happens inside ONE
// critical section (CRI diff-gate P0-1).
//
// Reading the journal outside the lock and writing it with an overwrite let
// two concurrent replies both see "no journal", build J1 and J2 for the same
// anchor, and write them in sequence. If the first then delivered its
// response and died before persisting SENT, recovery would read J2 while the
// response id on disk carries J1's bytes: create-if-absent refuses it — as it
// should — and every retry fails identically. The ask stays open forever and
// exactly-once stops being recoverable.
//
// So a second initializer must RESUME J1, never replace it. The set is also
// frozen in here, not before: a snapshot taken outside the lock could be
// stale by the time it is persisted.
//
// THE GATE DEPENDS ON THE STATE OF THE JOURNAL (F-72 C2). What a gate protects
// is a NEW DELIVERY, so it applies to exactly the transactions that still have
// one to make:
//
//   - a fresh transaction is gated BEFORE it is written, so a forbidden reply
//     never becomes a journal. A pending journal outranks every argument and
//     retries its delivery forever: persisting a refused reply would block every
//     later reply of the session behind it.
//   - a pending journal is gated before it is finished; if refused it stays
//     intact and nothing is delivered.
//   - a SENT journal is not gated: the response is already there and only the
//     local archiving is left. Refusing it would leave the ask half-closed and
//     jam the session, for a delivery that cannot be undone anyway.
//   - any other state is an error. It is never read as "sent".
func beginReply(mgr *session.Manager, sid string, sel replySelector, gate replyGate) (*session.ReplyTxn, error) {
	var txn *session.ReplyTxn
	err := mgr.WithSessionLock(sid, func() error {
		existing, found, terr := mgr.ReadReplyTxn(sid)
		if terr != nil {
			return terr
		}
		if found {
			// An in-flight journal outranks anything the arguments say: finishing
			// a delivered reply must not depend on the retry being spelled the
			// same way.
			switch existing.State {
			case session.ReplyTxnPending:
				if gerr := runGate(gate, existing); gerr != nil {
					return fmt.Errorf("%w: %w (the pending reply stays on record, untouched)", errReplyBlocked, gerr)
				}
			case session.ReplyTxnSent:
				// Archive-only: nothing is delivered from here.
			default:
				return fmt.Errorf("reply journal is in state %q, which this version does not know — refusing to guess, inspect it by hand", existing.State)
			}
			txn = existing
			return nil
		}
		fresh, serr := sel()
		if serr != nil {
			return serr
		}
		if gerr := runGate(gate, fresh); gerr != nil {
			return fmt.Errorf("%w: %w", errReplyBlocked, gerr)
		}
		txn = fresh
		return mgr.WriteReplyTxn(sid, txn)
	})
	if err != nil {
		return nil, err
	}
	return txn, nil
}

// checkReplyText refuses a text that is not valid UTF-8 (F-176). The journal is JSON, and
// JSON turns every invalid byte into U+FFFD: the text a transaction holds in memory and the
// text it reads back from disk would differ, so the identity check that protects the journal
// (session.SameTransaction) would refuse the very journal this command had just written.
// Refusing the text up front, before any effect, is the only way that cannot happen.
//
// `what` names where the text came from, so the error points at the argument or the stream
// the caller has to fix. The offset is relative to that text.
func checkReplyText(what, s string) error {
	off := invalidUTF8Offset(s)
	if off < 0 {
		return nil
	}
	return fmt.Errorf("reply: %s is not valid UTF-8 (first invalid byte at offset %d): this call wrote and sent nothing — send the text as UTF-8", what, off)
}

// invalidUTF8Offset is the byte offset of the first byte of s that does not belong to a valid
// UTF-8 sequence, or -1 when s is valid. A real U+FFFD is valid: it decodes with size 3.
func invalidUTF8Offset(s string) int {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			return i
		}
		i += size
	}
	return -1
}

// completeJournal removes the journal at the end of driveReplyTxn. Getting here means every
// step of this reply is done: delivered, and every ask archived with its index recorded. So a
// journal that is already gone, or that now belongs to another reply, is not a failure of THIS
// reply, the end state it wanted is there; it is said and never silent, and another reply's
// journal is not touched (CompleteReplyTxn leaves it as it found it).
func completeJournal(mgr *session.Manager, sid string, txn *session.ReplyTxn, note func(format string, args ...any)) error {
	err := mgr.CompleteReplyTxn(sid, txn)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, session.ErrReplyTxnGone):
		note("reply: response %s is delivered and its asks are archived; its journal was already gone at the final check, so there was nothing left to remove\n", txn.ResponseID)
		return nil
	case errors.Is(err, session.ErrReplyTxnNotYours):
		note("reply: response %s is delivered and its asks are archived; the journal now on disk belongs to another reply and was left as it was\n", txn.ResponseID)
		return nil
	}
	return fmt.Errorf("reply: %w", err)
}

func runGate(gate replyGate, txn *session.ReplyTxn) error {
	if gate == nil {
		return nil
	}
	return gate(txn)
}

// routingReplyGate is the gate every front-end shares: the routing policy applies
// to an answer exactly as it applies to an ask (F-147). sendMessage always
// checked it; deliverResponse did not, so an observer's reply was delivered
// although an observer may never send, and a critic's reached an executor.
// The roles are read from the two manifests, never from a message, and allowMesh
// is false: answering has no flag.
func routingReplyGate(mgr *session.Manager, sid string) replyGate {
	return func(txn *session.ReplyTxn) error {
		sender, err := mgr.LoadManifest(sid)
		if err != nil {
			return fmt.Errorf("load sender manifest: %w", err)
		}
		target, err := mgr.LoadManifest(txn.To)
		if err != nil {
			return fmt.Errorf("load target manifest %q: %w", txn.To, err)
		}
		return routing.ValidateSendPair(sender.Role, target.Role, false)
	}
}

// cliReplySelector is the selection `cab-bridge reply` has always done, moved
// out of replyRun unchanged: the NOTIFIED asks, the recipient inferred or named,
// and the oldest delivery of that sender's asks.
func cliReplySelector(mgr *session.Manager, cfg config.Config, sid string, args []string, stdin io.Reader) replySelector {
	return func() (*session.ReplyTxn, error) {
		asks, aerr := collectOpenAsks(mgr, cfg, sid)
		if aerr != nil {
			return nil, aerr
		}
		if len(asks) == 0 {
			// The state the agent is actually in matters here (CRI2 P1-3): with an
			// ask sitting UNREAD, "no ask of yours is open" is FALSE from where the
			// agent stands — it knows it was asked something — and the tool
			// contradicts its memory with no bridge across. Say which state it is.
			if n := countUnseenInbound(mgr, cfg, sid); n > 0 {
				return nil, fmt.Errorf("%w: %d message(s) are waiting to be delivered — run next first, then reply", errUndeliveredWaiting, n)
			}
			return nil, errNothingToReplyTo
		}
		// The names that could plausibly be the RECIPIENT of this reply: the
		// senders with an open ask, and nobody else.
		//
		// It used to be every peer in my scope, and the plan for F-116 proposed
		// widening it to the whole data dir — which would have made `reply GO`
		// stop working the day somebody in an unrelated project registered an
		// agent called `GO`: the meaning of a valid payload changing because of a
		// session that has nothing to do with this conversation.
		//
		// Narrowing is the correct move and it also fixes today: this list only
		// ever answers "did they mean to name a recipient, or is this the
		// message?", and only a sender with an open ask can be the recipient of a
		// reply. A typo aimed at a peer with no open ask is not a near-miss — it
		// is a message. (CRI design-gate P1-3.)
		known := senderNames(asks)
		target, content, rerr := resolveReplyTarget(args, asks, known, stdin)
		if rerr != nil {
			return nil, rerr
		}
		// The text of an ARGUMENT was already checked by replyRun, before this selector
		// could be skipped by a journal in flight. This is the check of the text read from
		// stdin, which only happens here: a recovery never reads stdin, so it never needs it.
		if cerr := checkReplyText("the message", content); cerr != nil {
			return nil, cerr
		}

		closeIDs := oldestPage(asks, target)
		if len(closeIDs) == 0 {
			return nil, fmt.Errorf("no open ask from %s to answer", target)
		}
		return &session.ReplyTxn{
			ResponseID: session.DeterministicResponseID(sid, closeIDs[0]),
			To:         target,
			Anchor:     closeIDs[0],
			CloseIDs:   closeIDs,
			State:      session.ReplyTxnPending,
			Timestamp:  time.Now().UTC(),
			Content:    content,
		}, nil
	}
}
