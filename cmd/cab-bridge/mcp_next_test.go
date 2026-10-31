//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// joinedDemo plants an open val of /repo/demo, starts a cowork client, joins it and
// returns the pieces a test needs: the client, the app, and the ids of both sessions.
type joinedDemo struct {
	e       *mcpEnv
	app     *mcpApp
	c       *mcpClient
	valSID  string
	arcSID  string
	inbox   string
	project string
}

func newJoinedDemo(t *testing.T) *joinedDemo {
	t.Helper()
	e := newMCPEnv(t)
	e.val("valdemo01", "VAL-demo", "/repo/demo", 8*time.Hour)
	app, c := e.start(coworkClient)
	res := c.call("join", map[string]any{"project": "demo"})
	require.False(t, res.IsError, res.Text())
	arcs := architects(t, e)
	require.Len(t, arcs, 1)
	return &joinedDemo{e: e, app: app, c: c, valSID: "valdemo01", arcSID: arcs[0].SessionID,
		inbox: filepath.Join(e.dataDir, "sessions", arcs[0].SessionID, "inbox"), project: "demo"}
}

// put plants a message in the architect's inbox as if `from` had sent it.
func (j *joinedDemo) put(id, from, typ, content string) {
	j.e.t.Helper()
	plantInboxAt(j.e.t, j.e.dataDir, j.arcSID, id, from, typ, content, j.e.clock.now().Add(-time.Minute))
}

func (j *joinedDemo) next(wait int) toolResult {
	j.e.t.Helper()
	return j.c.call("next", map[string]any{"wait_seconds": wait})
}

func TestMCPNext_AMessageComesInFullOnceAndAsAPointerAfter(t *testing.T) {
	j := newJoinedDemo(t)
	body := "please review the plan " + strings.Repeat("filler ", 40) + "TAIL-MARKER"
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeQuery, body)

	first := j.next(0)
	require.False(t, first.IsError, first.Text())
	assert.Contains(t, first.Text(), "from VAL-demo")
	assert.Contains(t, first.Text(), "TAIL-MARKER")
	assert.Contains(t, first.Text(), "untrusted content below")

	second := j.next(0)
	assert.Contains(t, second.Text(), "id msg-aaaaaaaaaaaa")
	assert.NotContains(t, second.Text(), "TAIL-MARKER", "already shown: a pointer, not the body again")
}

// The whole point of `next` being a read: calling it changes nothing on disk, so a
// result lost on the way (the relay gives up after 60 s, Stop sends nothing) costs
// nothing.
func TestMCPNext_ChangesNothingOnDisk(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeQuery, "an ask")
	j.put("msg-bbbbbbbbbbbb", j.valSID, message.TypeNotify, "a tell")
	sessionDir := filepath.Join(j.e.dataDir, "sessions", j.arcSID)
	before := snapshotDir(t, sessionDir)

	require.False(t, j.next(0).IsError)
	require.False(t, j.next(0).IsError)

	after := snapshotDir(t, sessionDir)
	// The heartbeat rewrites the manifest while the process lives; everything else is
	// compared byte for byte, and nothing may have appeared (wake cursor, listener).
	delete(before, "manifest.json")
	delete(after, "manifest.json")
	assert.Equal(t, before, after, "next must not move, mark or create anything in the session")
	assert.NoFileExists(t, filepath.Join(sessionDir, "wake-cursor.json"))
	assert.NoFileExists(t, filepath.Join(sessionDir, "listener.json"))
}

func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		out[rel] = string(data)
		return nil
	}))
	return out
}

// An ask is visible with no wake cursor at all: it is not NOTIFIED by anything, and
// it does not need to be.
func TestMCPNext_AnAskIsVisibleWithoutAnyWakeCursor(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeQuery, "what is the status?")
	res := j.next(0)
	assert.Contains(t, res.Text(), "ask")
	assert.Contains(t, res.Text(), "what is the status?")
	assert.Contains(t, res.Text(), "`reply` with ask_id")
}

func TestMCPNext_WaitsForMailAndReturnsTheMomentItArrives(t *testing.T) {
	j := newJoinedDemo(t)
	start := time.Now()
	done := j.c.callAsync("next", map[string]any{"wait_seconds": 4})
	time.Sleep(200 * time.Millisecond)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, "it arrived mid-wait")

	res := done()
	assert.Contains(t, res.Text(), "it arrived mid-wait")
	assert.Less(t, time.Since(start), 3*time.Second, "it did not wait out the whole window")
}

func TestMCPNext_NothingNewWaitsTheWindowOutAndSaysSo(t *testing.T) {
	j := newJoinedDemo(t)
	start := time.Now()
	res := j.next(1)
	assert.GreaterOrEqual(t, time.Since(start), 900*time.Millisecond)
	assert.Contains(t, res.Text(), "0 message(s) open for you")
}

// A message the agent was already shown does not end a wait: the agent has it, and
// returning at once for it would turn the agent's loop into a spin.
func TestMCPNext_AnAlreadyShownMessageDoesNotEndTheWaitEarly(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, "shown once")
	require.Contains(t, j.next(0).Text(), "shown once")

	start := time.Now()
	res := j.next(1)
	assert.GreaterOrEqual(t, time.Since(start), 900*time.Millisecond)
	assert.Contains(t, res.Text(), "id msg-aaaaaaaaaaaa", "and it is still listed, as a pointer")
}

func TestMCPNext_RefusesAWaitOutsideWhatTheConfigurationAllows(t *testing.T) {
	j := newJoinedDemo(t)
	for _, wait := range []int{-1, 6, 1000} {
		res := j.next(wait)
		require.True(t, res.IsError, "wait %d", wait)
		assert.Contains(t, res.Text(), "between 0 and 5")
	}
	res := j.c.call("next", map[string]any{"wait_seconds": "ten"})
	require.True(t, res.IsError, "not an integer")
	res = j.c.call("next", map[string]any{"waitseconds": 3})
	require.True(t, res.IsError, "an unknown argument is an error, not silently dropped")
}

func TestMCPNext_NeedsAJoinedProjectAndNamesTheChoiceWhenThereAreSeveral(t *testing.T) {
	e := newMCPEnv(t)
	e.val("valdemo01", "VAL-demo", "/repo/demo", time.Hour)
	e.val("valother1", "VAL-other", "/repo/other", time.Hour)
	_, c := e.start(coworkClient)

	res := c.call("next", nil)
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "call `join` first")

	require.False(t, c.call("join", map[string]any{"project": "demo"}).IsError)
	require.False(t, c.call("join", map[string]any{"project": "other"}).IsError)
	res = c.call("next", map[string]any{"wait_seconds": 0})
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "several projects are joined (demo, other)")

	res = c.call("next", map[string]any{"project": "other", "wait_seconds": 0})
	assert.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "project other")
}

// What the val has not authorized is not shown: not its body, not the name of its
// author, and the notice carries an id and a reason only. The message claims to be
// from a val, with a plausible name; the manifest on disk decides.
func TestMCPNext_HoldsBackWhatIsNotAnOpenValAndNamesNothingAboutIt(t *testing.T) {
	j := newJoinedDemo(t)
	plantOverviewSession(t, j.e.dataDir, "escsecret", session.RoleEsc, "ESC-SECRET-NAME", "/repo/demo", "", "working")
	j.put("msg-aaaaaaaaaaaa", "escsecret", message.TypeQuery, "SECRET-BODY from an executor")
	j.put("msg-bbbbbbbbbbbb", j.valSID, message.TypeNotify, "from the open val")

	res := j.next(0)
	assert.Contains(t, res.Text(), "from the open val")
	assert.Contains(t, res.Text(), "1 message(s) are held back")
	assert.Contains(t, res.Text(), "msg-aaaaaaaaaaaa")
	assert.Contains(t, res.Text(), "author-not-val")
	for _, leaked := range []string{"SECRET-BODY", "ESC-SECRET-NAME", "escsecret"} {
		assert.NotContains(t, res.Raw, leaked)
		assert.NotContains(t, j.e.logs.all(), leaked)
	}
}

// C1, with the old verdict populated: the message was shown while the bridge was
// open; the val closes it while a later call is waiting; that call must not show the
// message again, not even as a pointer.
func TestMCPNext_ACloseDuringTheWaitIsSeenByTheCallThatIsWaiting(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, "SHOWN-WHILE-OPEN")
	require.Contains(t, j.next(0).Text(), "SHOWN-WHILE-OPEN")

	done := j.c.callAsync("next", map[string]any{"wait_seconds": 2})
	time.Sleep(300 * time.Millisecond)
	require.NoError(t, j.e.manager().CloseDesktopGrant(j.valSID))

	res := done()
	require.False(t, res.IsError, res.Text())
	assert.NotContains(t, res.Raw, "SHOWN-WHILE-OPEN")
	assert.NotContains(t, res.Raw, "id msg-aaaaaaaaaaaa |", "not even the pointer line")
	assert.Contains(t, res.Text(), "author-not-open")
	assert.Contains(t, res.Text(), "0 message(s) open for you")
}

// The expiry works the same way, on the clock the manager and the authorizer share.
func TestMCPNext_AGrantThatExpiresStopsShowingTheValsMessages(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, "SEEN-BEFORE-EXPIRY")
	require.Contains(t, j.next(0).Text(), "SEEN-BEFORE-EXPIRY")

	j.e.clock.advance(9 * time.Hour) // the grant was 8 hours
	res := j.next(0)
	assert.NotContains(t, res.Raw, "SEEN-BEFORE-EXPIRY")
	assert.Contains(t, res.Text(), "author-not-open")

	_, err := j.e.manager().OpenDesktopGrant(j.valSID, time.Hour)
	require.NoError(t, err)
	assert.Contains(t, j.next(0).Text(), "id msg-aaaaaaaaaaaa", "reopened, it is visible again (as a pointer: it was shown)")
}

// An inbox that cannot be read in time is an error, never a half-read inbox.
func TestMCPNext_AScanThatCannotFinishIsAnErrorNotAPartialInbox(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeNotify, "should not appear partially")
	p := j.app.lookup(projectKey("/repo/demo", surfaceCowork))
	require.NotNil(t, p)
	p.guard.busy.Store(true) // a scan is still running

	res := j.next(0)
	require.True(t, res.IsError)
	assert.Contains(t, res.Text(), "could not be read within the time")
	assert.NotContains(t, res.Raw, "should not appear partially")
}

// An empty result must not read as "the inbox is empty" when the truth is "you cannot
// see": with no val holding the bridge open, `next` says so.
func TestMCPNext_SaysWhenNoValHasTheBridgeOpen(t *testing.T) {
	j := newJoinedDemo(t)
	assert.NotContains(t, j.next(0).Text(), "No val of this project has the bridge open", "open: no such notice")

	require.NoError(t, j.e.manager().CloseDesktopGrant(j.valSID))
	res := j.next(0)
	require.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "No val of this project has the bridge open right now")
	assert.Contains(t, res.Text(), "0 message(s) open for you")
	assert.Contains(t, res.Text(), "desktop open")

	_, err := j.e.manager().OpenDesktopGrant(j.valSID, time.Hour)
	require.NoError(t, err)
	j.e.clock.advance(2 * time.Hour)
	assert.Contains(t, j.next(0).Text(), "No val of this project has the bridge open right now", "expired counts the same")
}
