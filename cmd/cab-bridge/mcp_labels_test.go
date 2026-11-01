//go:build !windows

package main

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// A project is named for the model by its basename; two that share one are told apart
// by a tag of their path's hash, never by the path, which can hold a user's or a
// client's name. The name printed is the name every tool accepts back.

func TestProjectLabels_ATwinGetsATagAndNeverAPath(t *testing.T) {
	labels, err := projectLabels([]string{"/a/repo", "/b/repo", "/c/solo"})
	require.NoError(t, err)

	assert.Equal(t, "solo", labels["/c/solo"], "a basename that is alone stays as it is")
	assert.Equal(t, "repo-"+scopeTag("/a/repo"), labels["/a/repo"])
	assert.Equal(t, "repo-"+scopeTag("/b/repo"), labels["/b/repo"])
	require.NotEqual(t, scopeTag("/a/repo"), scopeTag("/b/repo"), "the premise: these two paths hash apart")
	for scope, label := range labels {
		assert.NotContains(t, label, "/", "the label of %s is not a path", scope)
	}
}

// twinsOfOneTag searches for two paths of one basename that share the four hex
// characters, which is possible and rare (1 in 65536 a pair).
func twinsOfOneTag(t *testing.T) (string, string) {
	t.Helper()
	seen := map[string]string{}
	for i := 0; i < 20000; i++ {
		scope := "/p" + strings.Repeat("x", i%7) + "/" + string(rune('a'+i%26)) + "/" + strconv.Itoa(i) + "/repo"
		if other, ok := seen[scopeTag(scope)]; ok {
			return other, scope
		}
		seen[scopeTag(scope)] = scope
	}
	t.Fatal("no two paths sharing four hex characters in 20000 tries")
	return "", ""
}

func TestProjectLabels_TheTagGrowsUntilTheLabelsAreDistinct(t *testing.T) {
	a, b := twinsOfOneTag(t)
	require.Equal(t, scopeTag(a), scopeTag(b), "the premise: four hex characters do not tell these two apart")
	require.NotEqual(t, a, b)

	labels, err := projectLabels([]string{a, b})
	require.NoError(t, err)
	assert.NotEqual(t, labels[a], labels[b], "two projects, two names")
	assert.Greater(t, len(labels[a]), len("repo-")+scopeTagChars, "it took more of the hash than four characters")
	assert.NotContains(t, labels[a]+labels[b], "/")
}

func TestProjectLabels_ATaggedLabelThatIsAnotherProjectsBasenameStaysDistinct(t *testing.T) {
	tagged := "repo-" + scopeTag("/a/repo")
	scopes := []string{"/a/repo", "/b/repo", "/z/" + tagged}

	labels, err := projectLabels(scopes)
	require.NoError(t, err)
	seen := map[string]string{}
	for scope, label := range labels {
		other, dup := seen[label]
		assert.False(t, dup, "%s and %s are both called %q", scope, other, label)
		seen[label] = scope
	}
	assert.Len(t, labels, 3)
}

func TestProjectLabels_TheCLIKeepsThePathWhereAPathIsWhatResolves(t *testing.T) {
	labels := scopeLabels([]string{"/a/repo", "/b/repo", "/c/solo"})
	assert.Equal(t, "/a/repo", labels["/a/repo"])
	assert.Equal(t, "/b/repo", labels["/b/repo"])
	assert.Equal(t, "solo", labels["/c/solo"])
}

// architectOf is the session the server registered for the project, to plant mail in.
func architectOf(t *testing.T, e *mcpEnv, scope string) *session.Manifest {
	t.Helper()
	for _, mf := range architects(t, e) {
		if session.EffectiveScope(mf) == scope {
			return mf
		}
	}
	t.Fatalf("no architect session for %s", scope)
	return nil
}

func TestMCPJoin_TwoProjectsOfOneBasenameAreDistinctAndEachReachable(t *testing.T) {
	e := newMCPEnv(t)
	e.val("valx0001", "VAL-x", "/x/repo", time.Hour)
	e.val("valy0001", "VAL-y", "/y/repo", time.Hour)
	_, c := e.start(coworkClient)
	labelX, labelY := "repo-"+scopeTag("/x/repo"), "repo-"+scopeTag("/y/repo")

	list := c.call("join", nil)
	require.False(t, list.IsError, list.Text())
	assert.Contains(t, list.Text(), `project: "`+labelX+`"; val: VAL-x`)
	assert.Contains(t, list.Text(), `project: "`+labelY+`"; val: VAL-y`)
	assert.NotContains(t, list.Text(), "/x/")
	assert.NotContains(t, list.Text(), "/y/")

	refused := c.call("join", map[string]any{"project": "VAL-x"})
	require.True(t, refused.IsError)
	assert.Contains(t, refused.Text(), `with project "`+labelX+`"`)
	assert.NotContains(t, refused.Text(), "/x/")

	for _, label := range []string{labelX, labelY} {
		joined := c.call("join", map[string]any{"project": label})
		require.False(t, joined.IsError, "the name the list printed joins: %s", joined.Text())
		assert.Contains(t, joined.Text(), "Joined "+label+" as ")
		assert.NotContains(t, joined.Text(), "/x/")
		assert.NotContains(t, joined.Text(), "/y/")
	}

	ambiguous := c.call("next", map[string]any{"wait_seconds": 0})
	require.True(t, ambiguous.IsError)
	assert.Contains(t, ambiguous.Text(), "several projects are joined (", ambiguous.Text())
	assert.Contains(t, ambiguous.Text(), labelX, "both are named, and neither is a path")
	assert.Contains(t, ambiguous.Text(), labelY)
	assert.NotContains(t, ambiguous.Text(), "/x/")

	for _, p := range []struct{ scope, val, label, id, text string }{
		{"/x/repo", "valx0001", labelX, "msg-aaaaaaaaaaaa", "mail for x"},
		{"/y/repo", "valy0001", labelY, "msg-bbbbbbbbbbbb", "mail for y"},
	} {
		plantInboxAt(t, e.dataDir, architectOf(t, e, p.scope).SessionID, p.id, p.val, message.TypeNotify, p.text, e.clock.now().Add(-time.Minute))
		res := c.call("next", map[string]any{"project": p.label, "wait_seconds": 0})
		require.False(t, res.IsError, res.Text())
		assert.Contains(t, res.Text(), "Bridge: project "+p.label+",", "each name reaches its own project")
		assert.Contains(t, res.Text(), p.text)
	}
}

func TestMCPProject_TheNameTheListPrintsNowReachesAProjectJoinedUnderAnotherName(t *testing.T) {
	e := newMCPEnv(t)
	e.val("valx0001", "VAL-x", "/x/repo", time.Hour)
	_, c := e.start(coworkClient)
	require.False(t, c.call("join", map[string]any{"project": "repo"}).IsError, "joined alone, as plain \"repo\"")

	e.val("valy0001", "VAL-y", "/y/repo", time.Hour)
	labelX := "repo-" + scopeTag("/x/repo")
	list := c.call("join", nil)
	require.Contains(t, list.Text(), `project: "`+labelX+`"; val: VAL-x`, "the premise: the list now calls the joined project by another name")

	byOld := c.call("next", map[string]any{"project": "repo", "wait_seconds": 0})
	assert.False(t, byOld.IsError, "the name it was joined under still works: %s", byOld.Text())
	byList := c.call("next", map[string]any{"project": labelX, "wait_seconds": 0})
	require.False(t, byList.IsError, "and the name the list prints reaches it too: %s", byList.Text())
	assert.Contains(t, byList.Text(), "Bridge: project repo,", "the same project")

	unknown := c.call("next", map[string]any{"project": "repo-0000", "wait_seconds": 0})
	assert.True(t, unknown.IsError, "a name nobody printed is still refused")
}

func TestMCPJoin_AJoinedProjectWhoseBridgeIsClosedStillCountsWhenNamingATwin(t *testing.T) {
	e := newMCPEnv(t)
	e.val("valx0001", "VAL-x", "/x/repo", time.Hour)
	_, c := e.start(coworkClient)
	require.False(t, c.call("join", map[string]any{"project": "repo"}).IsError)

	e.clock.advance(2 * time.Hour) // the bridge of /x/repo closes; it stays joined
	e.val("valz0001", "VAL-z", "/z/repo", time.Hour)
	labelZ := "repo-" + scopeTag("/z/repo")
	list := c.call("join", nil)
	require.False(t, list.IsError, list.Text())
	require.NotContains(t, list.Text(), "VAL-x", "the premise: only the twin is open now")
	assert.Contains(t, list.Text(), `project: "`+labelZ+`"`, "the twin is not called \"repo\", that is already the joined one's name: %s", list.Text())

	require.False(t, c.call("join", map[string]any{"project": labelZ}).IsError)
	res := c.call("next", map[string]any{"project": "repo", "wait_seconds": 0})
	require.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "Bridge: project repo, you are "+architectOf(t, e, "/x/repo").AgentName+".", "\"repo\" is still the first project, not the twin")
	assert.NotContains(t, res.Text(), labelZ)
}
