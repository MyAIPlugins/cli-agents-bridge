package main

import (
	"fmt"
	"io"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
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
func beginReply(mgr *session.Manager, sid string, sel replySelector) (*session.ReplyTxn, error) {
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
			txn = existing
			return nil
		}
		fresh, serr := sel()
		if serr != nil {
			return serr
		}
		txn = fresh
		return mgr.WriteReplyTxn(sid, txn)
	})
	if err != nil {
		return nil, err
	}
	return txn, nil
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
