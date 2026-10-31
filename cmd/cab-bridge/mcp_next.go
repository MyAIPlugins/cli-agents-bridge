package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/mcp"
)

// The `next` tool: what the val has sent, read and never taken.
//
// It only READS. Nothing is marked, no wake cursor moves, no listener is claimed, no
// file leaves the inbox: a result that never reaches the model (the relay gives up on
// a call after sixty seconds, and Stop sends nothing) loses nothing, because the same
// messages are there on the next call. A message leaves the inbox only by an act of
// the agent: `ack` for a tell or a response, `reply` for an ask.
//
// Who may be read is decided afresh on EVERY pass of the wait (C1): a val can close
// the bridge while a call is waiting, and what is about to be returned must not be
// judged by what was true when the wait began.

type nextArgs struct {
	Project     string `json:"project"`
	WaitSeconds *int   `json:"wait_seconds"`
}

// pass is the outcome of one look at the inbox.
type pass struct {
	visible []visibleMessage
	held    []withheld
	corrupt int
}

func (a *mcpApp) toolNext(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
	var args nextArgs
	if err := mcp.DecodeArgs(raw, &args); err != nil {
		return mcp.Result{}, err
	}
	p, err := a.projectFor(args.Project)
	if err != nil {
		return mcp.Result{}, err
	}
	wait, err := a.waitFor(args.WaitSeconds)
	if err != nil {
		return mcp.Result{}, err
	}

	deadline := time.Now().Add(wait)
	// The loop is bounded by the deadline and by a count as well: one look per
	// interval, plus the first and the last.
	maxLooks := int(wait/a.pollInterval) + 2
	for look := 1; ; look++ {
		cur, err := a.look(ctx, p)
		if err != nil {
			return mcp.Result{}, err
		}
		if a.hasNew(p, cur) || look >= maxLooks || !time.Now().Before(deadline) {
			return a.renderPass(p, cur)
		}
		select {
		case <-ctx.Done():
			return mcp.Result{}, ctx.Err()
		case <-time.After(a.pollInterval):
		}
	}
}

func (a *mcpApp) waitFor(requested *int) (time.Duration, error) {
	secs := a.cfg.MCPNextDefaultWaitSeconds
	if requested != nil {
		secs = *requested
	}
	if secs < 0 || secs > a.cfg.MCPNextMaxWaitSeconds {
		return 0, fmt.Errorf("wait_seconds must be between 0 and %d, got %d", a.cfg.MCPNextMaxWaitSeconds, secs)
	}
	return time.Duration(secs) * time.Second, nil
}

// look reads the inbox once and judges every author with an authorizer built for this
// look and thrown away after it. The scan has its own deadline; a scan that cannot
// finish is an error, never a half-read inbox presented as the inbox.
func (a *mcpApp) look(ctx context.Context, p *mcpProject) (pass, error) {
	scanCtx, cancel := context.WithTimeout(ctx, time.Duration(a.cfg.MCPScanBudgetSeconds)*time.Second)
	defer cancel()
	scan, err := scanInboxWithin(scanCtx, &p.guard, filepath.Join(a.cfg.DataDir, "sessions", p.sid, "inbox"), a.cfg.MaxMessageBytes)
	if err != nil {
		if errors.Is(err, errInboxScanTimeout) {
			return pass{}, err
		}
		return pass{}, fmt.Errorf("could not read the inbox: %w", err)
	}
	visible, held := newAuthorizer(a.mgr, p.scope, a.mgr.NowUTC()).authorizeEntries(scan.entries)
	return pass{visible: visible, held: held, corrupt: len(scan.corrupt)}, nil
}

// hasNew says whether this look found a visible message no result has shown yet. A
// message that was already shown does not end a wait: the agent has it, and a call
// that returned at once for it would turn the agent's loop into a spin.
func (a *mcpApp) hasNew(p *mcpProject, cur pass) bool {
	ids := make([]string, len(cur.visible))
	for i, v := range cur.visible {
		ids[i] = v.ID()
	}
	return p.planner.CountNew(ids) > 0
}

func (a *mcpApp) renderPass(p *mcpProject, cur pass) (mcp.Result, error) {
	vals, err := a.grantingVals(p.scope)
	if err != nil {
		return mcp.Result{}, fmt.Errorf("could not tell whether the bridge is open: %w", err)
	}
	return renderNext(p.planner, nextView{
		project:      p.label,
		agentName:    p.name,
		visible:      cur.visible,
		held:         cur.held,
		corrupt:      cur.corrupt,
		bridgeClosed: len(vals) == 0,
		now:          a.mgr.NowUTC(),
		budget:       a.cfg.MCPMaxResultBytes,
	}), nil
}
