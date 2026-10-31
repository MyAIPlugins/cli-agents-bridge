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
	results, ok := archiveBatch(filepath.Join(a.cfg.DataDir, "sessions", p.sid), args.IDs, a.cfg.MaxMessageBytes, check, moveToProcessed)
	text := renderAck(results, ok)
	if !ok {
		return mcp.ErrorResult(text), nil
	}
	return mcp.Result{Content: []mcp.Block{mcp.Text(text)}}, nil
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
