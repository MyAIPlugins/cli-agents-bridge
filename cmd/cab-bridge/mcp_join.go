package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/mcp"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// The `join` tool: how Claude Desktop gets into a project.
//
// Without an argument it lists the projects whose val has opened the bridge. With one
// it takes the architect identity of (project, surface): a session whose ProjectPath is
// a VIRTUAL path under the project, so it never collides with the val's own session or
// with a CLI agent working in the repository, and whose scope is the project's real
// one, so everything that groups by scope sees it where it belongs.

type joinArgs struct {
	Project string `json:"project"`
}

func (a *mcpApp) toolJoin(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
	var args joinArgs
	if err := mcp.DecodeArgs(raw, &args); err != nil {
		return mcp.Result{}, err
	}
	open, err := a.openProjects()
	if err != nil {
		return mcp.Result{}, err
	}
	if args.Project == "" {
		return mcp.Result{Content: []mcp.Block{mcp.Text(a.renderOpenProjects(open))}}, nil
	}
	op, ok := pickProject(open, args.Project)
	if !ok {
		return mcp.Result{}, fmt.Errorf("project %q is not open to Claude Desktop. The val of a project opens it with `cab-bridge desktop open`. %s", args.Project, a.renderOpenProjects(open))
	}
	client, ok := mcp.ClientFrom(ctx)
	if !ok {
		return mcp.Result{}, errors.New("the client did not introduce itself, so its surface is unknown")
	}
	surface, err := surfaceOf(client.Name)
	if err != nil {
		return mcp.Result{}, err
	}
	p, already, err := a.joinProject(op, surface)
	if err != nil {
		return mcp.Result{}, err
	}
	return mcp.Result{Content: []mcp.Block{mcp.Text(a.renderJoined(p, op, already))}}, nil
}

func pickProject(open []openProject, named string) (openProject, bool) {
	for _, p := range open {
		if p.label == named || p.scope == named {
			return p, true
		}
	}
	return openProject{}, false
}

func (a *mcpApp) renderOpenProjects(open []openProject) string {
	if len(open) == 0 {
		return "No project is open to Claude Desktop right now. The val of a project opens it with `cab-bridge desktop open`: ask them."
	}
	now := a.mgr.NowUTC()
	var b strings.Builder
	fmt.Fprintf(&b, "Projects open to you (%d). Join one with `join` and its name:\n", len(open))
	for _, p := range open {
		names := make([]string, len(p.vals))
		for i, v := range p.vals {
			names[i] = fmt.Sprintf("%s (open for another %s)", v.name, v.expires.Sub(now).Round(time.Minute))
		}
		fmt.Fprintf(&b, "- %s: %s\n", p.label, strings.Join(names, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (a *mcpApp) renderJoined(p *mcpProject, op openProject, already bool) string {
	now := a.mgr.NowUTC()
	var b strings.Builder
	if already {
		fmt.Fprintf(&b, "Already joined to %s as %s.\n", p.label, p.name)
	} else {
		fmt.Fprintf(&b, "Joined %s as %s (role architect, through %s).\n", p.label, p.name, p.surface)
	}
	b.WriteString("You can talk to the val of this project, and only to it:\n")
	for _, v := range op.vals {
		fmt.Fprintf(&b, "- %s (bridge open for another %s)\n", v.name, v.expires.Sub(now).Round(time.Minute))
	}
	b.WriteString("What the val sends arrives with `next`. `ask` expects an answer, `tell` does not; `reply` answers an ask you received.")
	return b.String()
}

// joinProject takes the identity (project, surface) for this process, once. A second
// call for the same one is reported as already joined and touches nothing.
func (a *mcpApp) joinProject(op openProject, surface string) (*mcpProject, bool, error) {
	a.joinMu.Lock()
	defer a.joinMu.Unlock()

	key := projectKey(op.scope, surface)
	if p := a.lookup(key); p != nil {
		return p, true, nil
	}
	lock, err := a.mgr.AcquireIdentityLock(key)
	if err != nil {
		return nil, false, identityLockError(err)
	}
	p, err := a.registerArchitect(op, surface, lock)
	if err != nil {
		if rerr := lock.Release(); rerr != nil {
			a.logf("could not release an identity lock after a failed join: %v", rerr)
		}
		return nil, false, err
	}
	a.mu.Lock()
	a.projects[key] = p
	a.mu.Unlock()
	return p, false, nil
}

func identityLockError(err error) error {
	var held *session.HeldError
	switch {
	case errors.As(err, &held):
		return fmt.Errorf("another Claude Desktop process is already speaking as this identity (pid %d): only one conversation can hold a project through a surface at a time. Close the other one, or wait for it to end", held.PID)
	case errors.Is(err, session.ErrIdentityLockUnsupported):
		return err
	}
	return fmt.Errorf("could not take the identity of this project: %w", err)
}

// registerArchitect registers (or resumes) the architect session of the identity and
// starts its heartbeat. The caller holds the identity lock, so no other process can be
// registering the same identity: the resume may reclaim whatever a dead predecessor
// left, which is the whole point of resuming.
func (a *mcpApp) registerArchitect(op openProject, surface string, lock *session.IdentityLock) (*mcpProject, error) {
	team, err := commonTeam(op)
	if err != nil {
		return nil, err
	}
	vpath := filepath.Join(op.scope, virtualProjectDirName, surface)
	name, err := a.architectName(op.scope, vpath, surface)
	if err != nil {
		return nil, err
	}
	mf, release, err := a.mgr.Register(a.ctx, session.RegisterOpts{
		ProjectPath: vpath,
		AgentName:   name,
		Role:        session.RoleArchitect,
		TeamID:      team,
		Scope:       op.scope,
		Resume:      true,
	})
	if err != nil {
		return nil, fmt.Errorf("could not register the architect session: %w", err)
	}
	if rerr := release(); rerr != nil {
		a.logf("could not release the session lock after registering: %v", rerr)
	}
	if err := a.mgr.SetState(mf.SessionID, session.StateIdle); err != nil {
		return nil, fmt.Errorf("could not set the architect session idle: %w", err)
	}
	beatCtx, stop := context.WithCancel(a.ctx)
	return &mcpProject{
		scope: op.scope, surface: surface, label: op.label, sid: mf.SessionID, name: mf.AgentName,
		lock: lock, stopBeats: stop, beatsDone: a.mgr.StartHeartbeat(beatCtx, mf.SessionID),
		planner: mcp.NewPlanner(),
	}, nil
}

// commonTeam is the team label the architect carries: the one the project's open vals
// share. A val that belongs to a team reaches its peers by name only inside that team
// (collectPeers), so an architect with no team would be invisible to it. Vals that
// disagree leave no label that serves them all, and that is refused rather than
// settled by picking one.
func commonTeam(op openProject) (string, error) {
	team := op.vals[0].team
	for _, v := range op.vals[1:] {
		if v.team != team {
			return "", fmt.Errorf("the vals of %s belong to different teams (%q and %q), so no single team label would let each of them find the architect by name: close the bridge of all but one of them", op.label, team, v.team)
		}
	}
	return team, nil
}

// architectName is the name of the architect session of the identity. An existing
// session of the identity keeps its name for good: a name is what the val addresses
// it by, and renaming it on a re-join would send the val's next message to nobody.
// A new one is ARC-<project>-<surface>, with four characters of the scope's hash
// added when another session in the data dir already answers to that name (names are
// unique per data dir).
func (a *mcpApp) architectName(scope, vpath, surface string) (string, error) {
	manifests, err := a.sessionManifests()
	if err != nil {
		return "", err
	}
	var newest *session.Manifest
	taken := map[string]bool{}
	for _, mf := range manifests {
		taken[mf.AgentName] = true
		if mf.Role != session.RoleArchitect || !session.SamePathLexical(mf.ProjectPath, vpath) {
			continue
		}
		if newest == nil || mf.LastHeartbeat.After(newest.LastHeartbeat) {
			newest = mf
		}
	}
	if newest != nil {
		return newest.AgentName, nil
	}
	name := session.SuggestAddressableName(strings.Join([]string{architectNamePrefix, filepath.Base(scope), surface}, "-"))
	if taken[name] {
		name = session.SuggestAddressableName(name + "-" + scopeTag(scope))
	}
	if taken[name] {
		return "", fmt.Errorf("the name %s is already taken by another session in this data dir, and so is the one with the project's tag added: remove the stale session that holds it (`cab-bridge peers --all-scopes`, then `cab-bridge cleanup --session-id=<id>`)", name)
	}
	return name, nil
}
