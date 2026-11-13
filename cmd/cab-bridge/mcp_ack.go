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

// The `ack` tool: how a tell or a response leaves the inbox.
//
// Messages are never consumed by being read, so this is the one act that archives
// them. It archives exactly the ids named, in two phases (archiveBatch): every id is
// validated before the first one moves, so a mistake in one position cannot read as a
// completed acknowledgement. Two kinds of message are refused whatever the id: an ask,
// which is closed by `reply` (that is what tells the val it was answered), and one
// whose author is not an open val, which Desktop cannot see and so cannot have handled.

type ackArgs struct {
	Project string   `json:"project"`
	IDs     []string `json:"ids"`
}

func (a *mcpApp) toolAck(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
	var args ackArgs
	if err := mcp.DecodeArgs(raw, &args); err != nil {
		return mcp.Result{}, err
	}
	p, err := a.projectFor(args.Project)
	if err != nil {
		return mcp.Result{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	auth := newAuthorizer(a.mgr, p.scope, a.mgr.NowUTC())
	check := func(m *message.Message) error {
		if m.Type == message.TypeQuery {
			return errors.New("an ask is closed by `reply`, not by ack")
		}
		if _, held := auth.authorize(m); held != nil {
			return errors.New("this message is held back, so it cannot be acknowledged from here")
		}
		return nil
	}
	results, ok, lerr := ackArchive(a.mgr, p.sid, filepath.Join(a.cfg.DataDir, "sessions", p.sid), args.IDs, a.cfg.MaxMessageBytes, check, moveToProcessed)
	if lerr != nil {
		var notRun *lockNotTaken
		if !errors.As(lerr, &notRun) {
			return mcp.Result{}, lerr // ackArchive has no other error; one that appears is not swallowed
		}
		return mcp.ErrorResult(renderAckNotRun(args.IDs, notRun)), nil
	}
	text := renderAck(results, ok)
	if !ok {
		return mcp.ErrorResult(text), nil
	}
	return mcp.Result{Content: []mcp.Block{mcp.Text(text)}}, nil
}

// ackArchive archives ids from the inbox of session sid, under the lock of that session (F-177):
// both phases of archiveBatch, the validation and the moves, so that what was validated is what is
// moved. The error is only ever the one of not getting to do it at all (*lockNotTaken, nothing
// moved); archiveBatch has none of its own, it reports an outcome per id. The move is passed in so
// a test can step into the middle of the batch.
func ackArchive(mgr *session.Manager, sid, sessionDir string, ids []string, maxContentBytes int, check ackCheck, move func(src, processedDir string) error) ([]ackResult, bool, error) {
	var results []ackResult
	var ok bool
	err := underSessionLock(mgr, sid, func() error {
		results, ok = archiveBatch(sessionDir, ids, maxContentBytes, check, move)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return results, ok, nil
}

// renderAckNotRun is the result of an ack that did not run because the session lock was not taken.
// Same shape as every other refusal of ack (a line per id), and the first line says which of the
// three it is, because what the agent does next differs: ack again, stop, or look at the folder.
func renderAckNotRun(ids []string, e *lockNotTaken) string {
	var head string
	switch e.outcome() {
	case lockBusy:
		head = fmt.Sprintf("Not done. The session lock was still held by another operation after %s. Nothing was moved and nothing was lost; ack again.", e.wait)
	case lockGone:
		head = "Not done. This session no longer exists (it was retired): there is nothing to acknowledge."
	default:
		head = fmt.Sprintf("Not done. The session lock could not be taken (%v). Nothing was moved. This is not a wait that will pass: look at the session folder.", e.err)
	}
	var b strings.Builder
	b.WriteString(head)
	for _, id := range dedupe(ids) {
		fmt.Fprintf(&b, "\n- %s: %s", id, ackNotAttempted)
	}
	return b.String()
}

// renderAck states the outcome of every id. A reason never quotes a message.
func renderAck(results []ackResult, ok bool) string {
	var b strings.Builder
	if ok {
		b.WriteString("Done.\n")
	} else {
		b.WriteString("Not done. Nothing was lost; fix the ids marked below and ack again.\n")
	}
	for _, r := range results {
		id := r.ID
		if id == "" {
			id = "(batch)"
		}
		if r.Detail == "" {
			fmt.Fprintf(&b, "- %s: %s\n", id, r.Outcome)
			continue
		}
		fmt.Fprintf(&b, "- %s: %s (%s)\n", id, r.Outcome, r.Detail)
	}
	return strings.TrimRight(b.String(), "\n")
}
