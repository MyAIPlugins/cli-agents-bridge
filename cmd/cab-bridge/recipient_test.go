package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// --- F-116: the addressing grammar -----------------------------------------

// TestRecipient_RoundTrip is the property the whole grammar rests on: every
// token `peers` prints can be pasted into a command.
//
// Tested as format -> parse -> format rather than as two separate functions,
// because the defect it prevents lives BETWEEN them: a formatter and a parser
// that each look right and disagree would put the discovery list back to being
// wider than reachability — F-116 rebuilt by its own fix.
func TestRecipient_RoundTrip(t *testing.T) {
	t.Parallel()
	cases := []recipient{
		{name: "VAL-payload"},
		{name: "VAL-payload", scope: "alancurtisagency-payload"},
		{name: "VAL-x", scope: "/Users/alan/develop/two-checkouts/bridge"},
		{name: "a", scope: "b"},
	}
	for _, want := range cases {
		got, err := parseRecipient(want.String())
		require.NoError(t, err, "%q must parse", want.String())
		assert.Equal(t, want, got, "format -> parse -> format must be the identity")
	}
}

func TestParseRecipient(t *testing.T) {
	t.Parallel()

	t.Run("bare name is unqualified", func(t *testing.T) {
		got, err := parseRecipient("VAL-x")
		require.NoError(t, err)
		assert.Equal(t, recipient{name: "VAL-x"}, got)
		assert.False(t, got.qualified(), "no scope means my own, i.e. exactly the pre-F-116 behaviour")
	})

	t.Run("path scope keeps every separator after the first", func(t *testing.T) {
		got, err := parseRecipient("VAL-x@/Users/alan/we@rd/repo")
		require.NoError(t, err)
		assert.Equal(t, "VAL-x", got.name)
		assert.Equal(t, "/Users/alan/we@rd/repo", got.scope,
			"the name cannot contain the separator, so the first one splits and the rest is path")
	})

	t.Run("half a destination is refused, never guessed", func(t *testing.T) {
		for _, bad := range []string{"@repo", "VAL-x@"} {
			_, err := parseRecipient(bad)
			require.Error(t, err, "%q", bad)
			assert.Contains(t, err.Error(), "is not a destination")
		}
	})
}

func TestScopeMatchesHint(t *testing.T) {
	t.Parallel()
	const scope = "/Users/alan/develop/cli-agents-bridge"
	assert.True(t, scopeMatchesHint(scope, "cli-agents-bridge"), "the basename is what peers prints")
	assert.True(t, scopeMatchesHint(scope, scope), "and the full path is what it prints when ambiguous")
	assert.False(t, scopeMatchesHint(scope, "other"))
	assert.False(t, scopeMatchesHint(scope, "/elsewhere/cli-agents-bridge"),
		"a path is compared whole: same basename, different place")
	assert.False(t, scopeMatchesHint("", "anything"), "a legacy session with no scope is never addressed")
}

// --- the qualified address, end to end through resolveRecipientByName -------

func planted(t *testing.T, dataDir, id, role, name, scope string) {
	t.Helper()
	plantSessionFull(t, dataDir, id, role, name, scope, scope, "working")
}

func TestResolveRecipient_QualifiedCrossScope(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)

	const mine, theirs = "/repo/bridge", "/repo/payload"
	planted(t, dataDir, "valmine1", session.RoleVal, "VAL-bridge", mine)
	planted(t, dataDir, "valthem1", session.RoleVal, "VAL-payload", theirs)
	planted(t, dataDir, "escthem1", session.RoleEsc, "ESC-payload", theirs)

	t.Run("unqualified stays inside my scope — F-116 as reported", func(t *testing.T) {
		_, err := resolveRecipientByName(cfg, mgr, "VAL-payload", "valmine1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no agent named")
		assert.Contains(t, err.Error(), "another project", "and it names the form that would work")
	})

	t.Run("qualified reaches the other repository", func(t *testing.T) {
		got, err := resolveRecipientByName(cfg, mgr, "VAL-payload@payload", "valmine1")
		require.NoError(t, err)
		assert.Equal(t, "valthem1", got)
	})

	t.Run("a wrong project says which projects exist", func(t *testing.T) {
		_, err := resolveRecipientByName(cfg, mgr, "VAL-payload@nowhere", "valmine1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "projects with agents")
		assert.Contains(t, err.Error(), "payload", "the question is 'wrong name or wrong project?'")
	})

	t.Run("across projects only val to val", func(t *testing.T) {
		_, err := resolveRecipientByName(cfg, mgr, "ESC-payload@payload", "valmine1")
		require.Error(t, err, "the scope WAS the barrier: removing it must not open every pair at once")
		assert.Contains(t, err.Error(), "only a val writes to a val")
	})
}

// Two checkouts of one repository share a basename, so the short form cannot
// name one of them. The way out is the full path — which is exactly what `peers`
// prints on those rows (scopeColumn), so the error stays inside the grammar.
func TestResolveRecipient_AmbiguousBasenameFailsClosed(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)

	planted(t, dataDir, "valmine1", session.RoleVal, "VAL-bridge", "/repo/mine")
	planted(t, dataDir, "valone01", session.RoleVal, "VAL-twin", "/a/twin")
	planted(t, dataDir, "valtwo02", session.RoleVal, "VAL-twin", "/b/twin")

	_, err := resolveRecipientByName(cfg, mgr, "VAL-twin@twin", "valmine1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "matches 2 projects")
	assert.Contains(t, err.Error(), "/a/twin")
	assert.Contains(t, err.Error(), "/b/twin")
	assert.NotContains(t, err.Error(), "cleanup",
		"the ambiguity is in the address, not in the sessions: never advise destroying a live one")

	got, err := resolveRecipientByName(cfg, mgr, "VAL-twin@/b/twin", "valmine1")
	require.NoError(t, err, "the full path is the way out, and peers prints it on these rows")
	assert.Equal(t, "valtwo02", got)
}

// --- reply on cross-scope homonyms (CRI design-gate P1-2) -------------------

// The plan claimed `(name, scope)` was an invariant because `join` enforces it.
// It does — on ONE of the doors: `register` is still public and `join
// --force-new` skips the guard, which is what soleSessionNamed's own comment has
// said all along. So two senders CAN share a name, and before this there was no
// id-free way to answer either of them.
func TestSoleSessionNamed_CrossScopeHomonymsHaveAWayOut(t *testing.T) {
	t.Parallel()
	senders := map[string]string{"aaaaaaa1": "VAL-same", "bbbbbbb2": "VAL-same"}
	asks := []openAsk{
		{id: "msg-aaaaaaaaaaaa", from: "aaaaaaa1", fromName: "VAL-same", scope: "/repo/one"},
		{id: "msg-bbbbbbbbbbbb", from: "bbbbbbb2", fromName: "VAL-same", scope: "/repo/two"},
	}

	_, err := soleSessionNamed("VAL-same", asks, senders)
	require.Error(t, err, "two senders, one name: never a silent pick")
	assert.Contains(t, err.Error(), "different projects")
	assert.Contains(t, err.Error(), "VAL-same@one", "and the way out is an address, not a cleanup")
	assert.Contains(t, err.Error(), "VAL-same@two")
	assert.NotContains(t, err.Error(), "cleanup",
		"neither is a duplicate — advising removal would destroy a live session to answer a question")

	got, err := soleSessionNamed("VAL-same@two", asks, senders)
	require.NoError(t, err)
	assert.Equal(t, "bbbbbbb2", got, "the qualified form answers the one you meant")
}

// The complement: two sessions with one name in ONE project ARE a duplicate, and
// there the old remediation is the right one.
func TestSoleSessionNamed_SameScopeHomonymsStillSayCleanup(t *testing.T) {
	t.Parallel()
	senders := map[string]string{"aaaaaaa1": "VAL-same", "bbbbbbb2": "VAL-same"}
	asks := []openAsk{
		{id: "msg-aaaaaaaaaaaa", from: "aaaaaaa1", fromName: "VAL-same", scope: "/repo/one"},
		{id: "msg-bbbbbbbbbbbb", from: "bbbbbbb2", fromName: "VAL-same", scope: "/repo/one"},
	}
	_, err := soleSessionNamed("VAL-same", asks, senders)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "in one project")
	assert.Contains(t, err.Error(), "cleanup")
}

// --- known, narrowed (CRI design-gate P1-3) ---------------------------------

// The plan proposed widening `known` to the whole data dir. That would have made
// the MEANING OF A VALID PAYLOAD depend on sessions in unrelated projects: with
// one open ask, `reply GO` sends "GO" — unless somebody, somewhere, registers an
// agent called `GO`, at which point the same command starts failing without this
// mailbox having changed.
func TestReplyGuardrail_IsBuiltFromOpenAskersOnly(t *testing.T) {
	t.Parallel()
	asks := []openAsk{{id: "msg-aaaaaaaaaaaa", from: "valaaaaa", fromName: "VAL-x", when: "2026-08-10T10:00:00Z"}}

	assert.Equal(t, []string{"VAL-x"}, senderNames(asks), "only senders with an open ask can be a reply's recipient")

	// `GO` is a message here and must stay one, whoever exists elsewhere.
	target, content, err := resolveReplyTarget([]string{"GO"}, asks, senderNames(asks), strings.NewReader(""))
	require.NoError(t, err)
	assert.Equal(t, "valaaaaa", target)
	assert.Equal(t, "GO", content, "a one-word payload must not become an error because of another project")

	// And a typo on somebody who DOES have an open ask is still caught.
	_, _, err = resolveReplyTarget([]string{"VAL-y"}, []openAsk{
		{id: "msg-aaaaaaaaaaaa", from: "valaaaaa", fromName: "VAL-x", when: "2026-08-10T10:00:00Z"},
		{id: "msg-bbbbbbbbbbbb", from: "cribbbbb", fromName: "CRI-z", when: "2026-08-10T11:00:00Z"},
	}, []string{"VAL-x", "CRI-z"}, strings.NewReader("the report"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "looks like")
}

// The cross-scope page carries the address ready to paste, and it must be the
// one that resolves — the full path, since here there is no list of other scopes
// to detect an ambiguous basename with.
func TestNextMessage_CrossScopeCarriesAPastableAddress(t *testing.T) {
	t.Parallel()
	const theirs = "/Users/alan/develop/payload"
	e := mailboxEntry{msg: &message.Message{
		ID: "msg-aaaaaaaaaaaa", From: "valthem1", FromAgentName: "VAL-payload",
		Type: message.TypeQuery, Content: "brief",
		Metadata: message.Metadata{FromScope: theirs},
	}}

	got := newNextMessage(e, false, "/Users/alan/develop/bridge", true)
	assert.Equal(t, theirs, got.FromScope)
	assert.Equal(t, "VAL-payload@"+theirs, got.FromAddress, "the agent copies, it does not assemble")

	parsed, err := parseRecipient(got.FromAddress)
	require.NoError(t, err, "and what it copies must parse back")
	assert.Equal(t, "VAL-payload", parsed.name)
	assert.True(t, scopeMatchesHint(theirs, parsed.scope), "and resolve to the sender's scope")

	// Same scope: neither field, because both would be noise on every message.
	same := newNextMessage(e, false, theirs, true)
	assert.Empty(t, same.FromScope)
	assert.Empty(t, same.FromAddress)

	// Legacy message with no fromScope: nothing invented, nothing looked up.
	legacy := mailboxEntry{msg: &message.Message{ID: "msg-bbbbbbbbbbbb", From: "old", FromAgentName: "VAL-old", Type: message.TypeQuery}}
	old := newNextMessage(legacy, false, "/Users/alan/develop/bridge", true)
	assert.Empty(t, old.FromScope, "absent means not stated, never 'same project as you'")
	assert.Empty(t, old.FromAddress)

	// And when MY OWN scope could not be read, nothing is labelled: not knowing
	// and being different are two facts, and an empty string collapsed them into
	// "everything is foreign" while the comment claimed the label was dropped.
	unknown := newNextMessage(e, false, "", false)
	assert.Empty(t, unknown.FromScope, "a failed manifest read must drop the label, not invent one")
	assert.Empty(t, unknown.FromAddress)
}

// --- diff-gate CRI, the four P1 --------------------------------------------

// P1-2: the way out of an ambiguity must be a token that RESOLVES. The first
// version wrote its own shortening and handed back `VAL-same@twin` twice — two
// identical strings for two different projects, neither of which reaches
// anybody. The remediation was the trap, again, three lines below a comment
// saying it must not be.
func TestSoleSessionNamed_AmbiguousBasenamesGetDistinctWorkingTokens(t *testing.T) {
	t.Parallel()
	senders := map[string]string{"aaaaaaa1": "VAL-same", "bbbbbbb2": "VAL-same"}
	asks := []openAsk{
		{id: "msg-aaaaaaaaaaaa", from: "aaaaaaa1", fromName: "VAL-same", scope: "/a/twin"},
		{id: "msg-bbbbbbbbbbbb", from: "bbbbbbb2", fromName: "VAL-same", scope: "/b/twin"},
	}

	_, err := soleSessionNamed("VAL-same", asks, senders)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VAL-same@/a/twin", "the basename is shared, so only the path resolves")
	assert.Contains(t, err.Error(), "VAL-same@/b/twin")

	// And both tokens the message offered actually work.
	for _, token := range []struct{ addr, want string }{
		{"VAL-same@/a/twin", "aaaaaaa1"},
		{"VAL-same@/b/twin", "bbbbbbb2"},
	} {
		got, err := soleSessionNamed(token.addr, asks, senders)
		require.NoError(t, err, "%q was offered as the way out and must work", token.addr)
		assert.Equal(t, token.want, got)
	}
}

func TestScopeLabels_ShortenOnlyWhereItCannotMislead(t *testing.T) {
	t.Parallel()
	labels := scopeLabels([]string{"/a/twin", "/b/twin", "/x/solo"})
	assert.Equal(t, "/a/twin", labels["/a/twin"], "two places cannot share one label")
	assert.Equal(t, "/b/twin", labels["/b/twin"])
	assert.Equal(t, "solo", labels["/x/solo"], "and the unambiguous one still shortens")
}

// P1-3: the restriction has to BIND where the message is composed, not where
// the name was looked up.
//
// The two are separated by SetRole, which F-110 put on the ordinary path: the
// resolver saw val→val, the role changed, and the message left for an `esc` in
// another repository. A check that runs on a lookup is a warning; this test
// pins that the guarantee is at the gateway, on the manifests actually used.
func TestSendMessage_CrossScopeRestrictionBindsAtTheGateway(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)

	const mine, theirs = "/repo/bridge", "/repo/payload"
	planted(t, dataDir, "valmine1", session.RoleVal, "VAL-bridge", mine)
	planted(t, dataDir, "valthem1", session.RoleVal, "VAL-payload", theirs)

	// The lookup happens while both are vals, and passes.
	target, err := resolveRecipientByName(cfg, mgr, "VAL-payload@payload", "valmine1")
	require.NoError(t, err)

	// Then the recipient changes role — an ordinary `join --role=esc` there.
	require.NoError(t, mgr.SetRole(target, session.RoleEsc))

	_, err = sendMessage(cfg, mgr, "valmine1", target, message.TypeQuery, "brief", nil, false)
	require.Error(t, err, "the resolver's answer is stale by the time we compose: the gateway must re-decide")
	assert.Contains(t, err.Error(), "only a val writes to a val")

	// And the complement: qualifying a peer in MY OWN project is a long way of
	// writing a local address, not a crossing — it must not be refused.
	planted(t, dataDir, "escmine1", session.RoleEsc, "ESC-bridge", mine)
	local, err := resolveRecipientByName(cfg, mgr, "ESC-bridge@bridge", "valmine1")
	require.NoError(t, err, "same scope, spelled the long way")
	_, err = sendMessage(cfg, mgr, "valmine1", local, message.TypeQuery, "brief", nil, false)
	require.NoError(t, err, "cross-scope is decided by comparing scopes, never by how the address was written")
}

// TestReply_CrossScopeExemptionIsADecision makes the exemption VERIFIED rather
// than merely absent — the same question `listenUntil` posed: reading the code,
// nobody can tell "deliberate" from "forgotten".
//
// The val→val restriction limits who may START a conversation, not who may
// answer one. And the reason is narrower than it first looked: it is NOT that
// everything already open passed today's checks — an open ask can predate them
// in three ordinary ways (delivered by a pre-F-116 binary; sent val→val and then
// one endpoint changed role via SetRole, which F-110 put on the normal path; or
// a reply transaction still finishing after an upgrade). It is that replying is
// a ONE-SHOT capability derived from a state that is already open, migration and
// compatibility included. An ask nobody can answer stays open forever, which is
// worse than the thing the restriction guards against.
func TestReply_CrossScopeExemptionIsADecision(t *testing.T) {
	dataDir := t.TempDir()
	cfg := nextTestConfig(dataDir)
	mgr := newSessionManager(cfg)
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_SESSION_ID", "escrpl01")

	const mine, theirs = "/repo/bridge", "/repo/payload"
	plantOverviewSession(t, dataDir, "escrpl01", session.RoleVal, "VAL-bridge", mine, "", "working")
	plantOverviewSession(t, dataDir, "valrpl01", session.RoleVal, "VAL-payload", theirs, "", session.StateOrchestrating)

	// A cross-scope ask that WAS legitimate when it arrived, plus a second one
	// from the same sender in a later delivery — so "closes only that ask" is a
	// claim with something to fail against.
	base := time.Now().UTC().Add(-time.Hour)
	deliverPage(t, mgr, dataDir, base, base, plantedAsk{"msg-aaaaaaaaaaaa", "the cross-scope brief"})
	deliverPage(t, mgr, dataDir, base.Add(time.Minute), base.Add(time.Minute), plantedAsk{"msg-bbbbbbbbbbbb", "a later one"})

	// Now the sender changes role — an ordinary `join --role=esc` over there.
	// Under today's restriction this pair could no longer OPEN a conversation.
	require.NoError(t, mgr.SetRole("valrpl01", session.RoleEsc))

	var stdout, stderr bytes.Buffer
	require.NoError(t, replyRun([]string{"the answer"}, strings.NewReader(""), &stdout, &stderr),
		"an ask that can never be answered stays open forever: replying must survive the role change")

	inbox := filepath.Join(dataDir, "sessions", "escrpl01", "inbox")
	assert.NoFileExists(t, filepath.Join(inbox, "msg-aaaaaaaaaaaa.json"), "the answered delivery is closed")
	assert.FileExists(t, filepath.Join(inbox, "msg-bbbbbbbbbbbb.json"), "and ONLY that one")

	// The exemption is a one-shot derived from an open state, not a channel: with
	// nothing open, the same pair cannot start anything.
	_, err := sendMessage(cfg, mgr, "escrpl01", "valrpl01", message.TypeQuery, "may I open one?", nil, false)
	require.Error(t, err, "answering is not a channel: opening is still refused")
	assert.Contains(t, err.Error(), "only a val writes to a val")
}

// The legacy branch, which is the first of the three ways an open ask can
// predate the checks: a message carrying no fromScope at all.
func TestReply_LegacyAskWithoutProvenanceIsStillAnswerable(t *testing.T) {
	dataDir := t.TempDir()
	cfg := nextTestConfig(dataDir)
	mgr := newSessionManager(cfg)
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_SESSION_ID", "escrpl01")

	plantOverviewSession(t, dataDir, "escrpl01", session.RoleVal, "VAL-bridge", "/repo/bridge", "", "working")
	plantOverviewSession(t, dataDir, "valrpl01", session.RoleVal, "VAL-payload", "/repo/payload", "", session.StateOrchestrating)

	// plantInboxAt writes no fromScope: exactly a message from before the field.
	base := time.Now().UTC().Add(-time.Hour)
	deliverPage(t, mgr, dataDir, base, base, plantedAsk{"msg-aaaaaaaaaaaa", "written before fromScope existed"})

	var stdout, stderr bytes.Buffer
	require.NoError(t, replyRun([]string{"answered anyway"}, strings.NewReader(""), &stdout, &stderr),
		"the index comes from the sender's MANIFEST, so an ask with no provenance is still answerable")
	assert.NoFileExists(t, filepath.Join(dataDir, "sessions", "escrpl01", "inbox", "msg-aaaaaaaaaaaa.json"))
}

// --- CRI2 on the document, which surfaced three code defects ----------------

// F-6: an UNKNOWN scope is not a different scope.
//
// A legacy session (pre-F-17) has none, and comparing strings made `"" !=
// "/repo/x"` read as a crossing — so such a session could no longer send a
// PLAIN, in-scope ask, refused about a project it never named.
//
// The same conflation as P2-5, in another file of the same commit, with the
// sentence describing it sitting in the file where it HAD been fixed.
func TestCrossesScopes_UnknownIsNotDifferent(t *testing.T) {
	t.Parallel()
	assert.False(t, crossesScopes("", "/repo/x"), "a legacy sender does not know where it is")
	assert.False(t, crossesScopes("/repo/x", ""), "and neither does a legacy recipient")
	assert.False(t, crossesScopes("", ""))
	assert.False(t, crossesScopes("/repo/x", "/repo/x"))
	assert.True(t, crossesScopes("/repo/x", "/repo/y"), "two KNOWN and different scopes: that is a crossing")
}

func TestResolveRecipient_LegacySessionCanStillSendInScope(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)

	// A pre-F-17 session: no scope at all. It is an esc, so if its empty scope
	// were read as "different", the val→val restriction would refuse it.
	plantSessionFull(t, dataDir, "legacyes", session.RoleEsc, "ESC-legacy", "", "/repo/x", "working")
	plantSessionFull(t, dataDir, "valhere1", session.RoleVal, "VAL-here", "", "/repo/x", "working")

	target, err := resolveRecipientByName(cfg, mgr, "VAL-here", "legacyes")
	require.NoError(t, err, "an unqualified, in-scope ask must not be refused about a project nobody named")
	_, err = sendMessage(cfg, mgr, "legacyes", target, message.TypeQuery, "brief", nil, false)
	require.NoError(t, err, "and the gateway must agree with the resolver")
}

// F-2: two sessions in ONE project are duplicates, not two projects — and the
// way out is different. The branch offered the same token twice and announced
// "matches 2 projects": paste it and the identical error comes back, with no
// flag on the loop verbs to escape by.
func TestResolveRecipient_SameScopeDuplicatesSayDuplicates(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)

	planted(t, dataDir, "valmine1", session.RoleVal, "VAL-bridge", "/repo/mine")
	planted(t, dataDir, "dupone01", session.RoleVal, "VAL-dup", "/a/twin")
	planted(t, dataDir, "duptwo02", session.RoleVal, "VAL-dup", "/a/twin")

	_, err := resolveRecipientByName(cfg, mgr, "VAL-dup@twin", "valmine1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "are duplicates, not different projects")
	assert.Contains(t, err.Error(), "cleanup", "here removing the dead one IS the way out")
	assert.NotContains(t, err.Error(), "matches 2 projects", "there is one project and two sessions")
}
