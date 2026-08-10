package main

import (
	"bytes"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// TestJoin_PrintsEveryoneNotAPickedPeer is the F-92 shape: with three agents
// alive, a command that names ONE of them is wrong however it chooses. The list
// cannot be wrong that way — and "peer: none" with three peers alive is what
// sent an agent into passive waiting while its brief was already in flight.
func TestJoin_PrintsEveryoneNotAPickedPeer(t *testing.T) {
	dataDir := t.TempDir()
	const scope = "/repo/group"
	plantSessionFull(t, dataDir, "valgrp01", session.RoleVal, "VAL-g", scope, "/repo/group", session.StateOrchestrating)
	plantSessionFull(t, dataDir, "crigrp01", session.RoleEsc, "CRI-g", scope, "/repo/group/docs", "working")
	plantSessionFull(t, dataDir, "cr2grp01", session.RoleEsc, "CRI2-g", scope, "/repo/group/cmd", "working")

	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)
	peers, _, err := collectPeers(mgr, dataDir, cfg.StaleSeconds, 65536, true, "", scope)
	require.NoError(t, err)

	here := othersHere(peers, "valgrp01")
	require.Len(t, here, 2, "everyone else, not a chosen one")

	var names []string
	for _, p := range here {
		names = append(names, p.AgentName)
	}
	assert.Equal(t, []string{"CRI-g", "CRI2-g"}, names, "deterministic order")

	var out bytes.Buffer
	require.NoError(t, printJoinReport(&out, joinReport{
		SessionID: "valgrp01", AgentName: "VAL-g", Role: session.RoleVal, Action: "resumed", Here: here,
		Hint: "run next to receive work",
	}))
	assert.Contains(t, out.String(), "CRI-g")
	assert.Contains(t, out.String(), "CRI2-g")
	assert.Contains(t, out.String(), "here with you (2)")
}

func TestJoin_ReportsAnEmptyRoomHonestly(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, printJoinReport(&out, joinReport{
		SessionID: "solo0001", AgentName: "ESC-solo", Role: session.RoleEsc, Action: "registered-new",
		Here: []joinPeer{}, Hint: "run next to receive work",
	}))
	assert.Contains(t, out.String(), "nobody else is here yet")
}

// Returning is not arriving. An agent that re-arms with a bare `join --role=x`
// — which is what the skill tells it to do — must find its own session, whatever
// name it answers to. Deriving a name here produced a phantom, compared it with
// the real one, and called the difference a collision: an agent named by a human
// met that stop on EVERY re-arm, forever.
func TestJoin_BareJoinReturnsToTheSessionAlreadyHere(t *testing.T) {
	dataDir := t.TempDir()
	proj := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	require.NoError(t, runJoin([]string{"--role=esc", "--agent-name=CRI-payload", "--project-path=" + proj}))

	out := captureStdout(t, func() {
		require.NoError(t, runJoin([]string{"--role=esc", "--project-path=" + proj}),
			"a bare re-arm must not stop: it is the same agent coming back")
	})
	assert.Contains(t, out, "CRI-payload", "and it keeps the name it was given")
	assert.Contains(t, out, "resumed")

	entries, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "still exactly one session on this path")
}

// The third road the three agents had to invent for themselves. They ran
// `cleanup --scope=my-session && join --agent-name=X`: a new id, an empty
// mailbox, and a message to the val to announce the change — the whole
// choreography v0.8 exists to remove, performed because renaming was impossible.
func TestJoin_ExplicitNameRenamesInPlaceAndKeepsTheMailbox(t *testing.T) {
	dataDir := t.TempDir()
	proj := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	require.NoError(t, runJoin([]string{"--role=architect", "--agent-name=ARCHITECT-docs", "--project-path=" + proj}))
	entries, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	sid := entries[0].Name()

	// Mail arrives BEFORE the rename: it must survive it, because it is addressed
	// to the id and the id does not change.
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val00001", "VAL-payload", message.TypeQuery, "brief")

	require.NoError(t, runJoin([]string{"--role=architect", "--agent-name=CRI-payload", "--project-path=" + proj}),
		"a name given by a human is an instruction, not a collision")

	after, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
	require.NoError(t, err)
	require.Len(t, after, 1, "renaming must not create a second session")
	assert.Equal(t, sid, after[0].Name(), "SAME id: identity did not change, the label did")

	mgr := newSessionManager(config.Config{DataDir: dataDir})
	mf, err := mgr.LoadManifest(sid)
	require.NoError(t, err)
	assert.Equal(t, "CRI-payload", mf.AgentName)
	assert.Equal(t, []string{"ARCHITECT-docs"}, mf.FormerAgentNames, "the old name is remembered, not discarded")

	assert.FileExists(t, filepath.Join(dataDir, "sessions", sid, "inbox", "msg-aaaaaaaaaaaa.json"),
		"the mailbox is untouched — this is the whole point")
}

// The one stop that survives, and it must still stop: a name LIVE in another
// directory belongs to another agent, and taking it would make every by-name
// recipient ambiguous. Checked before the rename, so a rename can never steal it.
func TestJoin_WillNotRenameIntoANameLiveElsewhere(t *testing.T) {
	dataDir := t.TempDir()
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, ".git"), 0o700)) // one scope: names only collide within one
	mine := filepath.Join(base, "mine")
	theirs := filepath.Join(base, "theirs")
	require.NoError(t, os.MkdirAll(mine, 0o700))
	require.NoError(t, os.MkdirAll(theirs, 0o700))
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	require.NoError(t, runJoin([]string{"--role=esc", "--agent-name=ESC-taken", "--project-path=" + theirs}))
	require.NoError(t, runJoin([]string{"--role=esc", "--agent-name=ESC-mine", "--project-path=" + mine}))

	err := runJoin([]string{"--role=esc", "--agent-name=ESC-taken", "--project-path=" + mine})
	require.Error(t, err, "that name is in use by a LIVE session in this project")
	assert.Contains(t, err.Error(), "already has a LIVE")
	assert.Contains(t, err.Error(), theirs, "and it names the directory the reader can go to")

	mgr := newSessionManager(config.Config{DataDir: dataDir})
	peers, _, perr := collectPeers(mgr, dataDir, 300, 65536, true, "", resolveScope(mine))
	require.NoError(t, perr)
	occupant, ok := findSessionHere(mgr, peers, mine)
	require.True(t, ok)
	assert.Equal(t, "ESC-mine", occupant.AgentName, "the refused rename left my name alone")
}

// A peer still writing to the old name must be told where it went. An event
// could not do this job: the peer may have been offline during the rename.
func TestResolveRecipient_TellsWhereARenamedNameWent(t *testing.T) {
	dataDir := t.TempDir()
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, ".git"), 0o700)) // one scope for both
	mine := filepath.Join(base, "mine")
	theirs := filepath.Join(base, "theirs")
	require.NoError(t, os.MkdirAll(mine, 0o700))
	require.NoError(t, os.MkdirAll(theirs, 0o700))
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	require.NoError(t, runJoin([]string{"--role=val", "--agent-name=VAL-payload", "--project-path=" + mine}))
	require.NoError(t, runJoin([]string{"--role=esc", "--agent-name=ARCHITECT-docs", "--project-path=" + theirs}))
	require.NoError(t, runJoin([]string{"--role=esc", "--agent-name=CRI-payload", "--project-path=" + theirs}))

	cfg := config.Config{DataDir: dataDir, StaleSeconds: 300, MaxMessageBytes: 65536}
	mgr := newSessionManager(cfg)
	peers, _, err := collectPeers(mgr, dataDir, 300, 65536, true, "", resolveScope(mine))
	require.NoError(t, err)
	var selfSID string
	for _, p := range peers {
		if p.AgentName == "VAL-payload" {
			selfSID = p.SessionID
		}
	}
	require.NotEmpty(t, selfSID)

	_, rerr := resolveRecipientByName(cfg, mgr, "ARCHITECT-docs", selfSID)
	require.Error(t, rerr)
	assert.Contains(t, rerr.Error(), "answered to that name")
	assert.Contains(t, rerr.Error(), "CRI-payload", "and says what to write to instead")
}

func TestRunJoin_RequiresRole(t *testing.T) {
	t.Setenv("CAB_DATA_DIR", t.TempDir())
	err := runJoin(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--role")
}

// TestRunJoin_EndToEndIsIdempotent: joining twice from the same directory is a
// resume, never a second session — the whole point of replacing register.
func TestRunJoin_EndToEndIsIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	proj := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	first := captureStdout(t, func() {
		require.NoError(t, runJoin([]string{"--role=esc", "--agent-name=ESC-j", "--project-path=" + proj}))
	})
	assert.Contains(t, first, "registered-new")

	second := captureStdout(t, func() {
		require.NoError(t, runJoin([]string{"--role=esc", "--agent-name=ESC-j", "--project-path=" + proj}))
	})
	assert.Contains(t, second, "resumed", "the second join must resume, not create")

	entries, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "exactly one session on this project path")

	// And a different name RENAMES the session that is already here, rather than
	// stopping: it is the same agent, correcting its label.
	renamed := captureStdout(t, func() {
		require.NoError(t, runJoin([]string{"--role=esc", "--agent-name=ESC-other", "--project-path=" + proj}))
	})
	assert.Contains(t, renamed, "ESC-other")
	entries, err = os.ReadDir(filepath.Join(dataDir, "sessions"))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "renaming never creates a second session")
}

// TestJoin_ReplaysOpenAsksAcrossLives is the F-34 cross-life gap (CRI2 P1-4).
//
// Within one life the guard holds — collectOpenAsks only counts NOTIFIED. But
// the cursor only ever grew: a page of `next` lost to a compact left its asks
// NOTIFIED forever, no later `next` re-showed them, and the next reply to that
// sender CLOSED them without this life having ever seen them.
func TestJoin_ReplaysOpenAsksAcrossLives(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)

	const sid = "jrepl001"
	plantOverviewSession(t, dataDir, sid, session.RoleEsc, "ESC-r", "/repo/r", "", "working")

	now := time.Now().UTC()
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeQuery, "the brief", now)
	plantInboxAt(t, dataDir, sid, "msg-bbbbbbbbbbbb", "valxxx01", message.TypeNotify, "an update", now)
	plantInboxAt(t, dataDir, sid, "msg-cccccccccccc", "valxxx01", message.TypeResponse, "an answer", now)
	_, err := mgr.CommitWakeCursor(sid, []string{"msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb", "msg-cccccccccccc"}, now, nil, nil)
	require.NoError(t, err)

	n, err := replayOpenAsks(mgr, cfg, sid)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the ask is replayed")

	cursor, _, err := mgr.ReadWakeCursor(sid)
	require.NoError(t, err)
	assert.False(t, cursor.IsNotified("msg-aaaaaaaaaaaa"), "the ask goes back to UNREAD")
	assert.True(t, cursor.IsNotified("msg-bbbbbbbbbbbb"), "a tell is one-shot: never re-woken")
	assert.True(t, cursor.IsNotified("msg-cccccccccccc"), "a response is one-shot too")
}

// TestForgetNotified_IsIdempotentAndBounded: replaying twice changes nothing
// more, and unknown ids are simply absent.
func TestForgetNotified_IsIdempotentAndBounded(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)

	const sid = "jrepl002"
	plantOverviewSession(t, dataDir, sid, session.RoleEsc, "ESC-r2", "/repo/r2", "", "working")
	now := time.Now().UTC()
	_, err := mgr.CommitWakeCursor(sid, []string{"msg-aaaaaaaaaaaa"}, now, nil, nil)
	require.NoError(t, err)

	require.NoError(t, mgr.ForgetNotified(sid, []string{"msg-aaaaaaaaaaaa"}))
	require.NoError(t, mgr.ForgetNotified(sid, []string{"msg-aaaaaaaaaaaa"}))
	require.NoError(t, mgr.ForgetNotified(sid, nil))
	require.NoError(t, mgr.ForgetNotified(sid, []string{"msg-neverexisted"}))

	cursor, _, err := mgr.ReadWakeCursor(sid)
	require.NoError(t, err)
	assert.Empty(t, cursor.Notified)
}

// TestJoin_NameTakenElsewhereNamesAReachablePlace: an error that names a place
// must name one the reader can go to.
//
// It used to print ProjectName, which is filepath.Base — so "run this from
// cridir" pointed at something that is not a directory, and a repo can hold
// several with that name. Same dead-end class this command had just closed by
// dropping the reference to `bootstrap`.
func TestJoin_NameTakenElsewhereNamesAReachablePlace(t *testing.T) {
	dataDir := t.TempDir()
	base := t.TempDir()
	occupantDir := filepath.Join(base, "cridir")
	mineDir := filepath.Join(base, "valdir")
	require.NoError(t, os.MkdirAll(occupantDir, 0o700))
	require.NoError(t, os.MkdirAll(mineDir, 0o700))

	const scope = "/repo/shared"
	plantSessionFull(t, dataDir, "occup001", session.RoleVal, "VAL-x", scope, occupantDir, session.StateOrchestrating)

	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)
	peers, _, err := collectPeers(mgr, dataDir, cfg.StaleSeconds, 65536, true, "", scope)
	require.NoError(t, err)

	occupant, path, clash := findNameElsewhere(mgr, peers, mineDir, "VAL-x")
	require.True(t, clash)
	assert.Equal(t, "occup001", occupant.SessionID)
	assert.Equal(t, occupantDir, path, "the full path, not the basename")
	assert.True(t, filepath.IsAbs(path), "an agent must be able to cd into what the error names")

	// Same directory is a resume, not a clash.
	_, _, clash = findNameElsewhere(mgr, peers, occupantDir, "VAL-x")
	assert.False(t, clash)
}

// Two days, two hand-kept role lists, two fresh reviewers sent to the wrong
// role. The lists now come from one place, and this is what that has to mean:
// whatever `--role` advertises is what the error teaches, and `critic` is in
// both because it is the role the CRI agents actually take.
func TestRoles_OneSourceOffersCriticAndReservesArchitect(t *testing.T) {
	t.Parallel()
	names := session.RoleNamesWithNote()
	assert.Contains(t, names, "critic", "the role a critic must be able to find")
	assert.Contains(t, names, "architect", "kept: sessions already run under it")
	assert.NotContains(t, names, "neutral", "the v1-read fallback is not a choice")

	lines := session.RoleLines()
	for _, r := range session.SelectableRoles {
		assert.Contains(t, lines, r.Name, "every offered role is explained")
		assert.Contains(t, lines, r.Description)
	}
	assert.Contains(t, lines, "Claude Desktop", "architect says what it is reserved for")

	// The two renderings cannot disagree: that was the whole failure.
	for _, r := range session.SelectableRoles {
		assert.Contains(t, names, r.Name)
	}
}

// A critic must be able to register and to be addressed by name — the routing
// was always permissive, but nothing exercised the role end to end.
func TestJoin_CriticIsAFirstClassRole(t *testing.T) {
	dataDir := t.TempDir()
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, ".git"), 0o700))
	valDir := filepath.Join(base, "val")
	criDir := filepath.Join(base, "cri")
	require.NoError(t, os.MkdirAll(valDir, 0o700))
	require.NoError(t, os.MkdirAll(criDir, 0o700))
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	require.NoError(t, runJoin([]string{"--role=val", "--agent-name=VAL-p", "--project-path=" + valDir}))
	out := captureStdout(t, func() {
		require.NoError(t, runJoin([]string{"--role=critic", "--agent-name=CRI-p", "--project-path=" + criDir}))
	})
	assert.Contains(t, out, "role critic")

	cfg := config.Config{DataDir: dataDir, StaleSeconds: 300, MaxMessageBytes: 65536}
	mgr := newSessionManager(cfg)
	peers, _, err := collectPeers(mgr, dataDir, 300, 65536, true, "", resolveScope(criDir))
	require.NoError(t, err)

	var criSID string
	for _, p := range peers {
		if p.AgentName == "CRI-p" {
			criSID = p.SessionID
		}
	}
	require.NotEmpty(t, criSID)

	// Addressable by name from the val...
	var valSID string
	for _, p := range peers {
		if p.AgentName == "VAL-p" {
			valSID = p.SessionID
		}
	}
	got, rerr := resolveRecipientByName(cfg, mgr, "CRI-p", valSID)
	require.NoError(t, rerr)
	assert.Equal(t, criSID, got)

	// ...and its overview pairs it with the val it reports to, not with whoever
	// happened to be first.
	peer, ok := selectPeer(session.RoleCritic, peers)
	require.True(t, ok)
	assert.Equal(t, session.RoleVal, peer.Role)
}

// Alan's rule on names, all four situations in one place — and the fifth line is
// the one that matters most: the SAME-DIRECTORY case must keep renaming rather
// than becoming a takeover. If a test stops telling those two apart, the next
// change merges them, and that merge is only noticed when somebody loses a
// session.
func TestJoin_NameRuleAcrossTheFourSituations(t *testing.T) {
	dataDir := t.TempDir()
	repoA := t.TempDir()
	repoB := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoA, ".git"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(repoB, ".git"), 0o700))
	a1 := filepath.Join(repoA, "a")
	a2 := filepath.Join(repoA, "b")
	b1 := filepath.Join(repoB, "c")
	for _, d := range []string{a1, a2, b1} {
		require.NoError(t, os.MkdirAll(d, 0o700))
	}
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	require.NoError(t, runJoin([]string{"--role=critic", "--agent-name=CRI-x", "--project-path=" + a1}))

	t.Run("same_directory_different_name_still_RENAMES", func(t *testing.T) {
		before, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
		require.NoError(t, err)
		require.NoError(t, runJoin([]string{"--role=critic", "--agent-name=CRI-renamed", "--project-path=" + a1}))
		after, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
		require.NoError(t, err)
		assert.Len(t, after, len(before), "a rename creates nothing and retires nobody")
		// put the name back for the cases below
		require.NoError(t, runJoin([]string{"--role=critic", "--agent-name=CRI-x", "--project-path=" + a1}))
	})

	t.Run("same_scope_other_directory_LIVE_is_refused", func(t *testing.T) {
		err := runJoin([]string{"--role=critic", "--agent-name=CRI-x", "--project-path=" + a2})
		require.Error(t, err, "an agent at work does not lose its identity to a newcomer")
		assert.Contains(t, err.Error(), "already has a LIVE")
		assert.Contains(t, err.Error(), a1, "and the error names the place, absolute")
	})

	t.Run("another_scope_is_refused_even_though_scopes_isolate", func(t *testing.T) {
		err := runJoin([]string{"--role=critic", "--agent-name=CRI-x", "--project-path=" + b1})
		require.Error(t, err, "the guard is against the human error, not against ambiguity")
		assert.Contains(t, err.Error(), "ANOTHER project")
		assert.Contains(t, err.Error(), a1)
	})

	t.Run("same_scope_other_directory_STALE_is_taken_over", func(t *testing.T) {
		// Age the holder out: no heartbeat, no PID.
		mgr := newSessionManager(config.Config{DataDir: dataDir})
		peers, _, err := collectPeers(mgr, dataDir, 300, 65536, true, "", resolveScope(a1))
		require.NoError(t, err)
		var holder string
		for _, p := range peers {
			if p.AgentName == "CRI-x" {
				holder = p.SessionID
			}
		}
		require.NotEmpty(t, holder)
		mf, err := mgr.LoadManifest(holder)
		require.NoError(t, err)
		mf.PID = deadPID
		mf.LastHeartbeat = time.Now().UTC().Add(-2 * time.Hour)
		require.NoError(t, mgr.SaveManifest(mf))

		require.NoError(t, runJoin([]string{"--role=critic", "--agent-name=CRI-x", "--project-path=" + a2}),
			"a restarted agent reclaims its place; nobody is on the other side")

		// The stale one yielded the NAME and kept its session and mailbox.
		retired, err := mgr.LoadManifest(holder)
		require.NoError(t, err)
		assert.NotEqual(t, "CRI-x", retired.AgentName, "it no longer holds the name")
		assert.Contains(t, retired.AgentName, "superseded")
		require.NotEmpty(t, retired.FormerAgentNames)
		assert.Equal(t, "CRI-x", retired.FormerAgentNames[len(retired.FormerAgentNames)-1],
			"the name it just yielded is the most recent former one, on disk")
		assert.DirExists(t, filepath.Join(dataDir, "sessions", holder), "the session itself is not destroyed")
	})
}

// A list of values must contain only values. The first attempt at marking the
// reserved role put the mark INSIDE the list — `architect(reserved)` — and that
// token is a value like any other on a surface built to be copied, in a parser
// that accepts any string: pasting it produced a session whose role was
// literally "architect(reserved)", outside every role invariant, silently.
func TestRoleNames_EveryTokenIsAUsableValue(t *testing.T) {
	t.Parallel()
	valid := map[string]bool{}
	for _, r := range session.SelectableRoles {
		valid[r.Name] = true
	}
	// Through the only exported door — which is itself the point: the bare list
	// cannot be reached from here, so no surface can print it without the note.
	list := strings.SplitN(session.RoleNamesWithNote(), "  (", 2)[0]
	for _, tok := range strings.Split(list, "|") {
		assert.True(t, valid[tok], "%q appears in the list but is not a role you can pass", tok)
		assert.NotContains(t, tok, "(", "no annotation may travel inside a value")
		assert.NotContains(t, tok, " ")
	}

	// The reservation is still said — beside the list, where it cannot be pasted
	// as a value.
	assert.Contains(t, session.RoleNamesWithNote(), "reserved for Claude Desktop")
}

// --- F-110: one directory, one working place --------------------------------

// TestJoin_RestartingWithADifferentRoleKeepsOneSession is F-110 as it happened:
// an agent was restarted with `--role=browser-tester` after first joining as
// `--role=tester`, and the tool created a SECOND session on the same path with
// the same name. Two live homonyms in one scope block every by-name recipient
// until a human runs cleanup, and the trigger is an ordinary action.
//
// None of the three name guards could have caught it: they all look at another
// path or another scope by construction, and delegate "same place" to
// findSessionHere — which matched on the role and so found the place empty.
func TestJoin_RestartingWithADifferentRoleKeepsOneSession(t *testing.T) {
	dataDir := t.TempDir()
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, ".git"), 0o700))
	mine := filepath.Join(base, "bro")
	require.NoError(t, os.MkdirAll(mine, 0o700))
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	require.NoError(t, runJoin([]string{"--role=tester", "--agent-name=BRO-x", "--project-path=" + mine}))
	mgr := newSessionManager(config.Config{DataDir: dataDir})
	leaveTheJoinProcessDead(t, mgr, dataDir, mine)

	require.NoError(t, runJoin([]string{"--role=browser-tester", "--agent-name=BRO-x", "--project-path=" + mine}))

	peers, _, perr := collectPeers(mgr, dataDir, 300, 65536, true, "", resolveScope(mine))
	require.NoError(t, perr)

	named := 0
	for _, p := range peers {
		if p.AgentName == "BRO-x" {
			named++
			assert.Equal(t, "browser-tester", p.Role, "the role is updated in place, not forked into a twin")
		}
	}
	assert.Equal(t, 1, named, "one directory holds ONE session, whatever role it answers to")
}

// The complement, and the reason the role is an attribute rather than an
// identity: changing it must keep the mailbox. A fork would have left the unread
// mail in a session nobody addresses any more.
func TestJoin_ChangingRoleKeepsTheMailbox(t *testing.T) {
	dataDir := t.TempDir()
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, ".git"), 0o700))
	mine := filepath.Join(base, "bro")
	require.NoError(t, os.MkdirAll(mine, 0o700))
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	require.NoError(t, runJoin([]string{"--role=tester", "--agent-name=BRO-x", "--project-path=" + mine}))
	mgr := newSessionManager(config.Config{DataDir: dataDir})
	peers, _, perr := collectPeers(mgr, dataDir, 300, 65536, true, "", resolveScope(mine))
	require.NoError(t, perr)
	occupant, ok := findSessionHere(mgr, peers, mine)
	require.True(t, ok)
	sid := occupant.SessionID
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "someone1", message.TypeQuery, "unread work", time.Now().UTC())
	leaveTheJoinProcessDead(t, mgr, dataDir, mine)

	require.NoError(t, runJoin([]string{"--role=critic", "--agent-name=BRO-x", "--project-path=" + mine}))

	mf, err := mgr.LoadManifest(sid)
	require.NoError(t, err, "same session id: it was updated, not replaced")
	assert.Equal(t, session.RoleCritic, mf.Role)
	assert.FileExists(t, filepath.Join(dataDir, "sessions", sid, "inbox", "msg-aaaaaaaaaaaa.json"),
		"the mailbox travels with the place, not with the role")
}

// betterOccupant matters on a data dir that ALREADY holds two homonyms — the
// state the fix prevents but does not repair. collectPeers returns them in
// os.ReadDir order, so without a rank a join could adopt the DEAD twin and leave
// the live one standing: the defect reproduced by its own fix.
func TestBetterOccupant_PrefersTheLiveThenTheMostRecent(t *testing.T) {
	t.Parallel()
	older := time.Now().UTC().Add(-time.Hour)
	newer := time.Now().UTC()

	live := peerSummary{SessionID: "bbbbbbbb", Stale: false, LastHeartbeat: older}
	dead := peerSummary{SessionID: "aaaaaaaa", Stale: true, LastHeartbeat: newer}
	assert.True(t, betterOccupant(live, dead), "alive wins even with an older heartbeat and a later id")
	assert.False(t, betterOccupant(dead, live))

	recent := peerSummary{SessionID: "zzzzzzzz", Stale: false, LastHeartbeat: newer}
	assert.True(t, betterOccupant(recent, live), "among the living, the most recent")

	tieA := peerSummary{SessionID: "aaaaaaaa", Stale: false, LastHeartbeat: newer}
	tieB := peerSummary{SessionID: "bbbbbbbb", Stale: false, LastHeartbeat: newer}
	assert.True(t, betterOccupant(tieA, tieB), "and the id breaks the tie, so the choice is deterministic")
	assert.False(t, betterOccupant(tieB, tieA))
}

// leaveTheJoinProcessDead reproduces what a real join leaves behind: the command
// exits, so its PID is gone, while the heartbeat it just wrote is still fresh.
// The session looks alive in every table and has nobody home — the exact state
// the two BRO-bridge were found in (heartbeat 1m18s, PID dead).
//
// Without it these tests pass for the WRONG REASON. In-process the PID is the
// test binary's own, so Register refuses with "session already exists for
// project (pid N)" — a guard that cannot fire in the field, where join is a
// one-shot process whose PID dies with it (LL-10, and BUG-A before it). The
// pre-fix run was red because of that guard, not because of F-110.
func leaveTheJoinProcessDead(t *testing.T, mgr *session.Manager, dataDir, projectPath string) {
	t.Helper()
	peers, _, err := collectPeers(mgr, dataDir, 300, 65536, true, "", resolveScope(projectPath))
	require.NoError(t, err)
	occupant, ok := findSessionHere(mgr, peers, projectPath)
	require.True(t, ok, "there must be a session here to leave behind")
	mf, err := mgr.LoadManifest(occupant.SessionID)
	require.NoError(t, err)
	mf.PID = deadPID
	mf.LastHeartbeat = time.Now().UTC() // fresh on purpose: not stale, just nobody home
	require.NoError(t, mgr.SaveManifest(mf))
}
