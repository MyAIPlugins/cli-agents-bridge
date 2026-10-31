package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

const (
	coworkClient = "local-agent-mode-demo"
	chatClient   = "claude-ai"
)

func architects(t *testing.T, e *mcpEnv) []*session.Manifest {
	t.Helper()
	app := newMCPApp(context.Background(), e.cfg, e.manager(), "", e.home, e.logs.add)
	all, err := app.sessionManifests()
	require.NoError(t, err)
	var out []*session.Manifest
	for _, mf := range all {
		if mf.Role == session.RoleArchitect {
			out = append(out, mf)
		}
	}
	return out
}

func TestMCPJoin_ListsOnlyTheProjectsWhoseValIsOpenNow(t *testing.T) {
	e := newMCPEnv(t)
	e.val("valopen01", "VAL-open", "/repo/open", time.Hour)
	e.val("valclosed", "VAL-closed", "/repo/closed", 0)
	e.val("valexpire", "VAL-expired", "/repo/expired", time.Minute)
	_, c := e.start(coworkClient)
	e.clock.advance(5 * time.Minute) // the third grant is over, the first still open

	res := c.call("join", nil)
	require.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "open")
	assert.Contains(t, res.Text(), "VAL-open")
	assert.NotContains(t, res.Text(), "closed", "a closed bridge is not named at all")
	assert.NotContains(t, res.Text(), "expired")
	assert.Empty(t, architects(t, e), "listing joins nothing")
}

func TestMCPJoin_NoOpenProjectSaysHowToOpenOne(t *testing.T) {
	e := newMCPEnv(t)
	e.val("valclosed", "VAL-closed", "/repo/closed", 0)
	_, c := e.start(coworkClient)
	res := c.call("join", nil)
	assert.False(t, res.IsError)
	assert.Contains(t, res.Text(), "desktop open")
}

func TestMCPJoin_TakesTheArchitectIdentityOnAVirtualPathAndIsIdempotent(t *testing.T) {
	e := newMCPEnv(t)
	e.val("valdemo01", "VAL-demo", "/repo/demo", time.Hour)
	valBefore, err := os.ReadFile(filepath.Join(e.dataDir, "sessions", "valdemo01", "manifest.json"))
	require.NoError(t, err)
	_, c := e.start(coworkClient)

	res := c.call("join", map[string]any{"project": "demo"})
	require.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "Joined demo as ARC-demo-cowork")
	assert.Contains(t, res.Text(), "VAL-demo")

	arcs := architects(t, e)
	require.Len(t, arcs, 1)
	mf := arcs[0]
	assert.Equal(t, "ARC-demo-cowork", mf.AgentName)
	assert.Equal(t, session.RoleArchitect, mf.Role)
	assert.Equal(t, filepath.Join("/repo/demo", ".cab-desktop", "cowork"), mf.ProjectPath, "a virtual path under the project")
	assert.Equal(t, "/repo/demo", mf.Scope, "and the project's real scope")
	assert.Equal(t, os.Getpid(), mf.PID)
	assert.Equal(t, session.StateIdle, mf.State)

	again := c.call("join", map[string]any{"project": "demo"})
	require.False(t, again.IsError, again.Text())
	assert.Contains(t, again.Text(), "Already joined to demo as ARC-demo-cowork")
	assert.Len(t, architects(t, e), 1, "no second session")

	valAfter, err := os.ReadFile(filepath.Join(e.dataDir, "sessions", "valdemo01", "manifest.json"))
	require.NoError(t, err)
	assert.Equal(t, string(valBefore), string(valAfter), "the val's session is not touched: no rename, no role change, no pid adoption (F-134)")
}

// The server never waits as a listener, so nobody can read it as "listening": the
// val sees an architect that has mail UNREAD, not one that is about to take it.
func TestMCPJoin_NeverClaimsAListener(t *testing.T) {
	e := newMCPEnv(t)
	e.val("valdemo01", "VAL-demo", "/repo/demo", time.Hour)
	_, c := e.start(coworkClient)
	require.False(t, c.call("join", map[string]any{"project": "demo"}).IsError)

	arcs := architects(t, e)
	require.Len(t, arcs, 1)
	owner, found, err := e.manager().ReadListener(arcs[0].SessionID)
	require.NoError(t, err)
	assert.False(t, found && owner.Listening(), "join claims no listener")
}

func TestMCPJoin_AProjectThatIsNotOpenIsRefusedAndRegistersNothing(t *testing.T) {
	e := newMCPEnv(t)
	e.val("valdemo01", "VAL-demo", "/repo/demo", 0)
	e.val("valopen01", "VAL-open", "/repo/open", time.Hour)
	_, c := e.start(coworkClient)

	res := c.call("join", map[string]any{"project": "demo"})
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "is not open")
	assert.Contains(t, res.Text(), "VAL-open", "and it offers what is open")
	assert.Empty(t, architects(t, e))

	res = c.call("join", map[string]any{"project": "nonexistent"})
	require.True(t, res.IsError)
	assert.Empty(t, architects(t, e))
}

func TestMCPJoin_ChatAndCoworkAreTwoIdentitiesOfTheSameProject(t *testing.T) {
	e := newMCPEnv(t)
	e.val("valdemo01", "VAL-demo", "/repo/demo", time.Hour)
	_, chat := e.start(chatClient)
	_, cowork := e.start(coworkClient)
	require.False(t, chat.call("join", map[string]any{"project": "demo"}).IsError)
	require.False(t, cowork.call("join", map[string]any{"project": "demo"}).IsError)

	names := map[string]bool{}
	for _, mf := range architects(t, e) {
		names[mf.AgentName] = true
	}
	assert.Equal(t, map[string]bool{"ARC-demo-chat": true, "ARC-demo-cowork": true}, names)
}

func TestMCPJoin_AClientItCannotTellApartIsRefused(t *testing.T) {
	e := newMCPEnv(t)
	e.val("valdemo01", "VAL-demo", "/repo/demo", time.Hour)
	_, c := e.start("some-other-client")
	res := c.call("join", map[string]any{"project": "demo"})
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "some-other-client")
	assert.Empty(t, architects(t, e))
}

// Two processes for one identity: the second is refused by the kernel lock, with the
// holder's pid, and registers nothing.
func TestMCPJoin_ASecondProcessOnTheSameIdentityIsRefused(t *testing.T) {
	e := newMCPEnv(t)
	e.val("valdemo01", "VAL-demo", "/repo/demo", time.Hour)
	_, first := e.start(coworkClient)
	_, second := e.start(coworkClient)
	require.False(t, first.call("join", map[string]any{"project": "demo"}).IsError)

	res := second.call("join", map[string]any{"project": "demo"})
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "already speaking as this identity")
	assert.Len(t, architects(t, e), 1, "and no second session appeared")
}

// After the owner ends, its identity is free: the next process resumes the SAME session
// under the SAME name, and the session was marked done on the way out.
func TestMCPJoin_ResumesTheSessionAndTheNameAfterTheOwnerEnds(t *testing.T) {
	e := newMCPEnv(t)
	e.val("valdemo01", "VAL-demo", "/repo/demo", time.Hour)
	app1, c1 := e.start(coworkClient)
	require.False(t, c1.call("join", map[string]any{"project": "demo"}).IsError)
	first := architects(t, e)[0]

	app1.close()
	ended, err := e.manager().LoadManifest(first.SessionID)
	require.NoError(t, err)
	assert.Equal(t, session.StateDone, ended.State, "the session says it is finished")

	_, c2 := e.start(coworkClient)
	res := c2.call("join", map[string]any{"project": "demo"})
	require.False(t, res.IsError, res.Text())
	arcs := architects(t, e)
	require.Len(t, arcs, 1, "same session, not a second one")
	assert.Equal(t, first.SessionID, arcs[0].SessionID)
	assert.Equal(t, first.AgentName, arcs[0].AgentName)
	assert.Equal(t, session.StateIdle, arcs[0].State)
}

func TestMCPJoin_ANameTakenInAnotherProjectGetsTheScopeTag(t *testing.T) {
	e := newMCPEnv(t)
	e.val("valdemo01", "VAL-demo", "/repo/demo", time.Hour)
	plantOverviewSession(t, e.dataDir, "squatter1", session.RoleEsc, "ARC-demo-cowork", "/elsewhere/other", "", "idle")
	_, c := e.start(coworkClient)
	res := c.call("join", map[string]any{"project": "demo"})
	require.False(t, res.IsError, res.Text())

	arcs := architects(t, e)
	require.Len(t, arcs, 1)
	assert.Equal(t, "ARC-demo-cowork-"+scopeTag("/repo/demo"), arcs[0].AgentName)
}

func TestMCPJoin_TheArchitectCarriesTheTeamOfTheValsAndRefusesValsThatDisagree(t *testing.T) {
	e := newMCPEnv(t)
	plantOverviewSession(t, e.dataDir, "valteam01", session.RoleVal, "VAL-a", "/repo/demo", "alpha", session.StateOrchestrating)
	_, err := e.manager().OpenDesktopGrant("valteam01", time.Hour)
	require.NoError(t, err)
	_, c := e.start(coworkClient)
	require.False(t, c.call("join", map[string]any{"project": "demo"}).IsError)
	arcs := architects(t, e)
	require.Len(t, arcs, 1)
	assert.Equal(t, "alpha", arcs[0].TeamID, "a val of a team finds its peers by name only inside the team")

	plantOverviewSession(t, e.dataDir, "valteam02", session.RoleVal, "VAL-b", "/repo/demo", "beta", session.StateOrchestrating)
	_, err = e.manager().OpenDesktopGrant("valteam02", time.Hour)
	require.NoError(t, err)
	_, c2 := e.start(chatClient)
	res := c2.call("join", map[string]any{"project": "demo"})
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "different teams")
}

func TestMCPJoin_IsCalledWithoutProjectBeforeAnyToolNeedsOne(t *testing.T) {
	e := newMCPEnv(t)
	app, _ := e.start(coworkClient)
	_, err := app.projectFor("")
	require.ErrorIs(t, err, errNoProjectJoined)
}
