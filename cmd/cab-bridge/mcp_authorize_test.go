package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

const authScope = "/repo/auth"

var authT0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// authEnv plants sessions in one data dir and answers for them through the real
// session.Manager, so the grant files are real files.
type authEnv struct {
	t       *testing.T
	dataDir string
	mgr     *session.Manager
}

func newAuthEnv(t *testing.T) *authEnv {
	t.Helper()
	dataDir := t.TempDir()
	mgr := session.NewManager(dataDir, time.Second)
	mgr.Now = func() time.Time { return authT0 }
	return &authEnv{t: t, dataDir: dataDir, mgr: mgr}
}

func (e *authEnv) plant(id, role, name, scope string) {
	e.t.Helper()
	plantOverviewSession(e.t, e.dataDir, id, role, name, scope, "", session.StateIdle)
}

func (e *authEnv) open(id string, ttl time.Duration) {
	e.t.Helper()
	_, err := e.mgr.OpenDesktopGrant(id, ttl)
	require.NoError(e.t, err)
}

func msgFrom(id, from, claimedRole, claimedName, content string) *message.Message {
	return &message.Message{
		ID: id, From: from, FromRole: claimedRole, FromAgentName: claimedName,
		Type: message.TypeQuery, Timestamp: "2026-10-30T11:00:00Z", Content: content,
	}
}

func TestAuthorizer_AnOpenValOfThisProjectIsVisibleUnderItsManifestName(t *testing.T) {
	t.Parallel()
	env := newAuthEnv(t)
	env.plant("valauth1", session.RoleVal, "VAL-real", authScope)
	env.open("valauth1", time.Hour)

	v, w := newAuthorizer(env.mgr, authScope, authT0).authorize(msgFrom("msg-aaaaaaaaaaaa", "valauth1", "val", "VAL-FORGED", "brief"), 5)
	require.Nil(t, w)
	assert.Equal(t, "VAL-real", v.AuthorName(), "the name on the manifest, not the one the message claims")
	assert.Equal(t, "brief", v.Content())
	assert.Equal(t, "msg-aaaaaaaaaaaa", v.ID())
}

func TestAuthorizer_EveryOtherCaseIsWithheldWithAReasonAndNothingElse(t *testing.T) {
	t.Parallel()
	env := newAuthEnv(t)
	env.plant("valclosed", session.RoleVal, "VAL-closed", authScope)
	env.plant("valexpire", session.RoleVal, "VAL-expired", authScope)
	env.open("valexpire", time.Minute)
	env.plant("valbadgrt", session.RoleVal, "VAL-badgrant", authScope)
	require.NoError(t, os.WriteFile(filepath.Join(env.dataDir, "sessions", "valbadgrt", "desktop-grant.json"), []byte("{broken"), 0o600))
	env.plant("valother1", session.RoleVal, "VAL-other", "/repo/elsewhere")
	env.open("valother1", time.Hour)
	env.plant("escauth01", session.RoleEsc, "ESC-secret-name", authScope)
	env.open("escauth01", time.Hour) // even a grant file in an executor's directory means nothing

	cases := []struct {
		name string
		from string
		now  time.Time
		want withheldReason
	}{
		{"a val whose bridge is closed", "valclosed", authT0, reasonNotOpen},
		{"a val whose grant expired", "valexpire", authT0.Add(2 * time.Minute), reasonNotOpen},
		{"a val whose grant file is damaged", "valbadgrt", authT0, reasonNotOpen},
		{"a val of another project, however open", "valother1", authT0, reasonOtherScope},
		{"an executor, grant or not", "escauth01", authT0, reasonNotVal},
		{"an author with no manifest", "gonegone1", authT0, reasonUnresolvable},
		{"an author id that is a path", "../../etc", authT0, reasonUnresolvable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The message LIES: it says val, with a plausible name.
			v, w := newAuthorizer(env.mgr, authScope, tc.now).authorize(msgFrom("msg-bbbbbbbbbbbb", tc.from, "val", "VAL-lie", "SENTINEL-BODY"), 13)
			require.NotNil(t, w)
			assert.Equal(t, tc.want, w.Reason)
			assert.Equal(t, "msg-bbbbbbbbbbbb", w.ID)
			assert.Equal(t, visibleMessage{}, v, "no content leaks through the zero value")

			raw, err := json.Marshal(w)
			require.NoError(t, err)
			assert.JSONEq(t, `{"id":"msg-bbbbbbbbbbbb","reason":"`+string(tc.want)+`"}`, string(raw), "the notice carries an id and a reason, nothing else")
			assert.NotContains(t, string(raw), "SENTINEL-BODY")
			assert.NotContains(t, string(raw), "ESC-secret-name", "not even the name of the executor")
		})
	}
}

func TestAuthorizer_AFileCannotBeUsedToReadAPathThroughTheAuthorField(t *testing.T) {
	t.Parallel()
	deps := &countingDeps{}
	_, w := newAuthorizer(deps, authScope, authT0).authorize(msgFrom("msg-cccccccccccc", "../../outside", "val", "x", "y"), 1)
	require.NotNil(t, w)
	assert.Equal(t, reasonUnresolvable, w.Reason)
	assert.Zero(t, deps.loads, "an invalid session id is refused BEFORE it builds a path")
}

type countingDeps struct{ loads, grants int }

func (c *countingDeps) LoadManifest(string) (*session.Manifest, error) {
	c.loads++
	return &session.Manifest{Role: session.RoleVal, Scope: authScope, AgentName: "VAL-x"}, nil
}

func (c *countingDeps) ReadDesktopGrant(string) (*session.DesktopGrant, bool, error) {
	c.grants++
	return &session.DesktopGrant{SchemaVersion: session.DesktopGrantSchemaVersion, ExpiresAt: authT0.Add(time.Hour)}, true, nil
}

// One pass reads each author once, however many messages the author sent; the
// next pass reads again. Nothing is remembered across passes (C1).
func TestAuthorizer_ReadsEachAuthorOncePerPassAndNeverRemembersAcrossPasses(t *testing.T) {
	t.Parallel()
	deps := &countingDeps{}
	entries := []mailboxEntry{
		{msg: msgFrom("msg-111111111111", "valcount1", "val", "n", "a")},
		{msg: msgFrom("msg-222222222222", "valcount1", "val", "n", "b")},
		{msg: msgFrom("msg-333333333333", "valcount2", "val", "n", "c")},
	}
	pass1 := newAuthorizer(deps, authScope, authT0)
	visible, held := pass1.authorizeEntries(entries)
	assert.Len(t, visible, 3)
	assert.Empty(t, held)
	assert.Equal(t, 2, deps.loads, "three messages, two authors, two manifest reads")

	newAuthorizer(deps, authScope, authT0).authorizeEntries(entries)
	assert.Equal(t, 4, deps.loads, "a new pass reads again: there is no cache that outlives it")
}

// C1, with the cache populated, which is the only way this test can tell the
// difference. A val closes the bridge while a call that waits is already running:
// the pass that began before the close still holds its verdict (it is a snapshot of
// one instant), the pass that follows does not.
func TestAuthorizer_AClosedBridgeIsSeenByTheNextPassNotByTheOldOne(t *testing.T) {
	t.Parallel()
	env := newAuthEnv(t)
	env.plant("valclose1", session.RoleVal, "VAL-a", authScope)
	env.plant("valstays1", session.RoleVal, "VAL-b", authScope)
	env.open("valclose1", time.Hour)
	env.open("valstays1", time.Hour)
	entries := []mailboxEntry{
		{msg: msgFrom("msg-aaaaaaaaaaaa", "valclose1", "val", "n", "from A")},
		{msg: msgFrom("msg-bbbbbbbbbbbb", "valstays1", "val", "n", "from B")},
	}

	old := newAuthorizer(env.mgr, authScope, authT0)
	visible, _ := old.authorizeEntries(entries) // populates the old pass's verdicts
	require.Len(t, visible, 2)

	require.NoError(t, env.mgr.CloseDesktopGrant("valclose1")) // A closes while the call waits

	fresh := newAuthorizer(env.mgr, authScope, authT0.Add(30*time.Second))
	visible, held := fresh.authorizeEntries(entries)
	require.Len(t, visible, 1, "the final check of the call sees the close")
	assert.Equal(t, "from B", visible[0].Content())
	require.Len(t, held, 1)
	assert.Equal(t, withheld{ID: "msg-aaaaaaaaaaaa", Reason: reasonNotOpen}, held[0])

	// And the clock: a grant that expires between two passes of the same call.
	env.open("valstays1", time.Minute)
	later := newAuthorizer(env.mgr, authScope, authT0.Add(5*time.Minute))
	visible, held = later.authorizeEntries(entries)
	assert.Empty(t, visible, "an expired grant is not visible just because the call began before it expired")
	assert.Len(t, held, 2)
}

// With two vals in one project, one open and one closed, the project is available
// through the open one but the closed one's messages stay withheld: the grant is the
// val's, not the project's.
func TestAuthorizer_TheGrantBelongsToTheValNotToTheProject(t *testing.T) {
	t.Parallel()
	env := newAuthEnv(t)
	env.plant("valopen01", session.RoleVal, "VAL-open", authScope)
	env.plant("valshut01", session.RoleVal, "VAL-shut", authScope)
	env.open("valopen01", time.Hour)
	entries := []mailboxEntry{
		{msg: msgFrom("msg-aaaaaaaaaaaa", "valshut01", "val", "n", "old message of the closed val")},
		{msg: msgFrom("msg-bbbbbbbbbbbb", "valopen01", "val", "n", "message of the open val")},
	}
	visible, held := newAuthorizer(env.mgr, authScope, authT0).authorizeEntries(entries)
	require.Len(t, visible, 1)
	assert.Equal(t, "message of the open val", visible[0].Content())
	require.Len(t, held, 1)
	assert.Equal(t, "msg-aaaaaaaaaaaa", held[0].ID)
}

// The structure is the guarantee, so the structure is tested: the notice has two
// fields, and a visible message cannot be built outside this file because it has no
// exported field to set.
func TestAuthorizer_TheTypesAreShapedSoThatNothingElseCanLeakOrBeForged(t *testing.T) {
	t.Parallel()
	w := reflect.TypeOf(withheld{})
	var names []string
	for i := 0; i < w.NumField(); i++ {
		names = append(names, w.Field(i).Name)
	}
	assert.Equal(t, []string{"ID", "Reason"}, names, "a new field on the notice is a new thing Desktop learns about a held-back message: decide it on purpose")

	v := reflect.TypeOf(visibleMessage{})
	for i := 0; i < v.NumField(); i++ {
		assert.False(t, v.Field(i).IsExported(), "visibleMessage.%s is exported: any package could then forge a visible message", v.Field(i).Name)
	}
	assert.False(t, strings.Contains(v.PkgPath(), "internal/"), "sanity: the type lives in package main")
}

// "Only this file builds a visibleMessage" is a property of the source, and a
// property of the source can be checked: any other non-test file that writes the
// composite literal is a second door.
func TestVisibleMessageHasASingleConstructor(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "mcp_authorize.go" {
			continue
		}
		data, err := os.ReadFile(f)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "visibleMessage{", "%s builds a visibleMessage: only mcp_authorize.go may, after judging the author", f)
	}
}
