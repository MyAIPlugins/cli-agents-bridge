package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/mcp"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// The `ask` and `tell` tools (D4').
//
// They do not reimplement the sending verbs: they run this very binary as a child
// (mcp_child.go), so recipient resolution, the routing policy, the duplicate warning
// and the delivery under the target's lock are the code the CLI has already been
// through. What this file owns is the decision BEFORE the child runs, which is the
// one that matters here: the recipient has to be a val of this project whose bridge is
// open, picked from a closed list built now, and never anything else, so that a name
// typed by the model cannot become an argument of a command that reaches somebody the
// val did not open the bridge to.

type sendArgs struct {
	Project string `json:"project"`
	To      string `json:"to"`
	Message string `json:"message"`
}

func (a *mcpApp) toolSend(verb string) mcp.Handler {
	return func(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
		var args sendArgs
		if err := mcp.DecodeArgs(raw, &args); err != nil {
			return mcp.Result{}, err
		}
		p, err := a.projectFor(args.Project)
		if err != nil {
			return mcp.Result{}, err
		}
		if strings.TrimSpace(args.Message) == "" {
			return mcp.Result{}, mcp.Refuse("the message is empty")
		}
		if len(args.Message) > a.cfg.MaxMessageBytes {
			return mcp.Result{}, mcp.Refusef("the message is %d bytes; the most one message may carry is %d", len(args.Message), a.cfg.MaxMessageBytes)
		}
		to, err := a.recipient(p, args.To)
		if err != nil {
			return mcp.Result{}, err
		}
		return a.runVerb(ctx, p, verb, to, args.Message)
	}
}

// recipient checks the name against the vals that have the bridge open NOW. The name
// is also held to the agent-name grammar and refused if it could be read as a flag:
// the child's argv is an array, but it is the CLI that parses it.
func (a *mcpApp) recipient(p *mcpProject, named string) (string, error) {
	vals, err := a.grantingVals(p.scope)
	if err != nil {
		return "", err
	}
	var matches []openVal
	names := make([]string, len(vals))
	for i, v := range vals {
		names[i] = v.name
		if v.name == named {
			matches = append(matches, v)
		}
	}
	switch {
	case named == "" || strings.HasPrefix(named, "-") || session.ValidateAgentName(named) != nil:
		return "", mcp.Refusef("`to` must be the name of a val of this project; the ones with the bridge open are: %s", listOrNone(names))
	case len(matches) == 0:
		return "", mcp.Refusef("no val named %q has the bridge open for %s now; the ones that do are: %s", named, p.label, listOrNone(names))
	case len(matches) > 1:
		return "", mcp.Refusef("%d open vals of %s are named %q, so nothing was sent: the val has to rename one of them", len(matches), p.label, named)
	}
	return named, nil
}

func listOrNone(names []string) string {
	if len(names) == 0 {
		return "none (the val of the project opens the bridge with `cab-bridge desktop open`)"
	}
	return strings.Join(names, ", ")
}

// runVerb runs `cab-bridge <verb> <to>` with the message on its stdin, as this
// identity, and turns the outcome into a result.
//
// On success, what the command said is passed on: it carries the echo of what was
// sent and any warning about the recipient. On failure it is NOT: a refusal of the CLI
// may name other sessions of the project (its "no such agent" lists them), and the
// names of executors must not reach Claude Desktop through an error message. The
// failure says what is true without it, and what to do.
func (a *mcpApp) runVerb(ctx context.Context, p *mcpProject, verb, to, content string) (mcp.Result, error) {
	res, err := runChild(ctx, childSpec{
		exe:     a.exe,
		args:    []string{verb, to},
		env:     childEnv(a.home, a.cfg.DataDir, p.sid),
		stdin:   content,
		timeout: time.Duration(a.cfg.MCPChildTimeoutSeconds) * time.Second,
		maxOut:  a.cfg.MCPMaxResultBytes / 4,
	})
	switch {
	case errors.Is(err, errChildTimeout):
		a.logf("%s to a val of %s did not finish in time", verb, p.label)
		return mcp.Result{}, mcp.Refuse("the command did not finish in the time allowed, so it is not known whether the message was sent: look at `next` for the answer before sending it again")
	case err != nil:
		a.logf("%s could not be run: %v", verb, err)
		return mcp.Result{}, mcp.Refuse("the command could not be run, nothing was sent")
	case res.exitCode != 0:
		a.logf("%s to a val of %s was refused (exit %d)", verb, p.label, res.exitCode)
		return mcp.Result{}, mcp.Refusef("the message was not sent (the command refused it, exit %d). Check with `join` that the project is still open and that the val is still there", res.exitCode)
	}
	out := strings.TrimSpace(res.stdout)
	if note := strings.TrimSpace(res.stderr); note != "" {
		out += "\n" + note
	}
	if res.truncated {
		out += "\n(the command's output was cut)"
	}
	return mcp.Result{Content: []mcp.Block{mcp.Text(out)}}, nil
}
