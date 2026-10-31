package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/myAIPlugins/cli-agents-bridge/internal/mcp"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// The `reply` tool: answering one named ask.
//
// It runs the very transaction the CLI's `reply` runs (beginReply and driveReplyTxn:
// the journal, the deterministic response id, send-then-archive), and brings three
// things of its own. The selector: the ask is named by its id, loaded from the inbox,
// and its author judged like every other door; there is no NOTIFIED state to look for
// because `next` over MCP marks nothing. The gate: the routing policy AND the grant of
// the recipient. And what is said about a journal it finds already in flight.
//
// A journal outranks the arguments (that is what makes finishing a delivered reply
// independent of how the retry is spelled), so a call can end up finishing a reply that
// is not the one it asked for. Then it finishes it, says so, and does NOT send the new
// one: the caller calls again. What a recovered journal holds is never shown.

type replyArgs struct {
	Project string `json:"project"`
	AskID   string `json:"ask_id"`
	Message string `json:"message"`
}

var (
	errReplyNoSuchAsk   = mcp.Refuse("there is no such ask in your inbox")
	errReplyNotAnAsk    = mcp.Refuse("only an ask is answered with `reply`; a tell or a response is handled with `ack`")
	errReplyAskArchived = mcp.Refuse("this ask is already answered or archived")
	errReplyAskHeld     = mcp.Refuse("this ask is held back, so it cannot be answered from here")
	errReplyBadID       = mcp.Refuse("this is not a valid message id")
	errRecipientClosed  = mcp.Refuse("the recipient's bridge is not open")
	errReplyUnreadable  = mcp.Refuse("the ask could not be read")
)

func (a *mcpApp) toolReply(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
	var args replyArgs
	if err := mcp.DecodeArgs(raw, &args); err != nil {
		return mcp.Result{}, err
	}
	p, err := a.projectFor(args.Project)
	if err != nil {
		return mcp.Result{}, err
	}
	if strings.TrimSpace(args.Message) == "" {
		return mcp.Result{}, mcp.Refuse("the reply is empty")
	}
	if len(args.Message) > a.cfg.MaxMessageBytes {
		return mcp.Result{}, mcp.Refusef("the reply is %d bytes; the most one message may carry is %d", len(args.Message), a.cfg.MaxMessageBytes)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	auth := newAuthorizer(a.mgr, p.scope, a.mgr.NowUTC())
	selected := false
	selector := a.replySelector(p, auth, args.AskID, args.Message, &selected)
	gate := a.replyGate(p, auth)
	txn, err := beginReply(a.mgr, p.sid, selector, gate)
	if err != nil {
		return mcp.Result{}, a.explainReplyRefusal(err)
	}

	resumed := !selected // a journal was already in flight: it outranked this call's arguments
	wasSent := txn.State == session.ReplyTxnSent
	// A fresh reply passed the gate, so its recipient is an open val and may be named.
	// A recovered one is never named: it may be addressed to a val this call knows
	// nothing about.
	toName := ""
	if !resumed {
		if mf, lerr := a.mgr.LoadManifest(txn.To); lerr == nil {
			toName = mf.AgentName
		}
	}
	// The notes of the shared code carry response ids and nothing of any message, so
	// they may go to the log; they are not part of what Desktop is told.
	note := func(format string, v ...any) { a.logf(strings.TrimSuffix(format, "\n"), v...) }
	if derr := driveReplyTxn(a.mgr, a.cfg, p.sid, txn, note); derr != nil {
		a.logf("reply could not be completed: %v", derr)
		return mcp.Result{}, mcp.Refuse("the reply could not be completed. It is on record and the next `reply` finishes it without sending it twice")
	}
	return mcp.Result{Content: []mcp.Block{mcp.Text(describeReply(txn, args.AskID, toName, resumed, wasSent))}}, nil
}

// replySelector builds the transaction of a NEW reply. selected is set when it runs,
// which is how the caller tells a fresh transaction from one that was already in flight.
func (a *mcpApp) replySelector(p *mcpProject, auth *authorizer, askID, content string, selected *bool) replySelector {
	return func() (*session.ReplyTxn, error) {
		*selected = true
		loc, err := locateMessage(filepath.Join(a.cfg.DataDir, "sessions", p.sid), askID)
		switch {
		case errors.Is(err, errMessageNotFound):
			return nil, errReplyNoSuchAsk
		case err != nil:
			return nil, errReplyBadID
		case loc.archived:
			return nil, errReplyAskArchived
		}
		m, err := loadMessageAt(loc.path, a.cfg.MaxMessageBytes)
		if err != nil {
			return nil, errReplyUnreadable
		}
		if m.Type != message.TypeQuery {
			return nil, errReplyNotAnAsk
		}
		if _, held := auth.authorize(m); held != nil {
			return nil, errReplyAskHeld
		}
		return &session.ReplyTxn{
			ResponseID: session.DeterministicResponseID(p.sid, askID),
			To:         m.From,
			Anchor:     askID,
			CloseIDs:   []string{askID},
			State:      session.ReplyTxnPending,
			Timestamp:  a.mgr.NowUTC(),
			Content:    content,
		}, nil
	}
}

// replyGate is the routing policy and the grant of the recipient. It is handed the
// EFFECTIVE transaction, so a journal recovered from an earlier life is judged on the
// val it is addressed to, not on the ask this call named.
func (a *mcpApp) replyGate(p *mcpProject, auth *authorizer) replyGate {
	routing := routingReplyGate(a.mgr, p.sid)
	return func(txn *session.ReplyTxn) error {
		if err := routing(txn); err != nil {
			return err
		}
		if !auth.allows(txn.To) {
			return errRecipientClosed
		}
		return nil
	}
}

// explainReplyRefusal turns what beginReply returned into words that name nobody. A
// blocked reply says only that its recipient's bridge is not open (or that routing
// refuses), because the journal that was found may be addressed to a val other than the
// one this call is about.
func (a *mcpApp) explainReplyRefusal(err error) error {
	switch {
	case errors.Is(err, errRecipientClosed):
		return mcp.Refuse("the reply was not sent: the bridge of the val it is addressed to is not open (it was closed or it expired). " +
			"If an earlier reply of yours was waiting, it stays on record untouched, and nothing new was processed. The val reopens the bridge with `cab-bridge desktop open`")
	case errors.Is(err, errReplyBlocked):
		return mcp.Refuse("the reply was not sent: the routing policy does not let this session write to that recipient. " +
			"If an earlier reply of yours was waiting, it stays on record untouched, and nothing new was processed")
	case errors.Is(err, errReplyNoSuchAsk), errors.Is(err, errReplyNotAnAsk), errors.Is(err, errReplyAskArchived),
		errors.Is(err, errReplyAskHeld), errors.Is(err, errReplyBadID), errors.Is(err, errReplyUnreadable):
		return err
	}
	a.logf("reply could not start: %v", err)
	return mcp.Refuse("the reply could not be started; nothing was sent")
}

func describeReply(txn *session.ReplyTxn, askID, toName string, resumed, wasSent bool) string {
	who := "the val"
	if toName != "" {
		who = toName
	}
	if !resumed {
		return fmt.Sprintf("Reply delivered to %s (response %s). Ask %s is closed.", who, txn.ResponseID, askID)
	}
	finished := fmt.Sprintf("An earlier reply to ask %s was found unfinished and has been completed (response %s", txn.Anchor, txn.ResponseID)
	if wasSent {
		finished += ", it had already been delivered; only its archiving was left"
	}
	finished += ")."
	if txn.Anchor == askID {
		return finished + " It went as it was first written; this call's text was not used."
	}
	return finished + fmt.Sprintf(" Your reply to ask %s was NOT sent: call `reply` again for it.", askID)
}
