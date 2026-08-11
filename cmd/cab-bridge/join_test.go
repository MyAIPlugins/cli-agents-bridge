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

// --- F-124 lotto 1: the name has to survive a shell -------------------------

// plantUnsafeName rewrites a session's name straight on disk, the way a binary
// that predates the grammar left it. Not through RenameAgent, which validates:
// the point is a name that exists BECAUSE nothing refused it.
func plantUnsafeName(t *testing.T, dataDir, sid, unsafe string) {
	t.Helper()
	mgr := newSessionManager(config.Config{DataDir: dataDir})
	mf, err := mgr.LoadManifest(sid)
	require.NoError(t, err)
	mf.AgentName = unsafe
	require.NoError(t, mgr.SaveManifest(mf))
}

// TestJoin_LegacyUnsafeNameIsNamedAndRepairable is the test that carries the
// weight of this lot, and it exists because the FIX would otherwise break live
// agents (CRI design-gate #3).
//
// `Manager.Register` validates the agent name at :116, BEFORE the resume branch
// at :123 — and on a bare re-join it is `join` itself that reads the occupant's
// name and hands it back to Register. So tightening the grammar does not merely
// refuse NEW bad names: it stops the idempotent re-join that the skill
// prescribes after every compact, for a session that was registered when the
// name was allowed.
//
// Reproduced before the fix with a throwaway patch: `register: agent name
// "ESC bridge" is not addressable`, exit 1, and not one word about the way out.
// The way out already existed (join.go renames in place, same id, same inbox) —
// what was missing was anybody saying so.
func TestJoin_LegacyUnsafeNameIsNamedAndRepairable(t *testing.T) {
	for _, tc := range []struct{ label, unsafe, repaired string }{
		{"a space", "ESC bridge", "ESC-bridge"},
		{"a leading dash", "-dash", "dash"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			dataDir := t.TempDir()
			proj := t.TempDir()
			t.Setenv("CAB_DATA_DIR", dataDir)
			t.Setenv("CAB_AUTO_GC_HOURS", "0")

			require.NoError(t, runJoin([]string{"--role=esc", "--agent-name=ESC-safe", "--project-path=" + proj}))
			entries, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
			require.NoError(t, err)
			require.Len(t, entries, 1)
			sid := entries[0].Name()

			plantUnsafeName(t, dataDir, sid, tc.unsafe)
			// Mail that predates the repair: it has to survive, because the repair
			// is a rename and identity is the id.
			plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val00001", "VAL-x", message.TypeQuery, "brief")

			// 1. The bare re-join: the one the skill prescribes, and the one that
			// breaks. It must refuse — and refuse by NAMING the repair.
			err = runJoin([]string{"--role=esc", "--project-path=" + proj})
			require.Error(t, err, "a name that cannot be addressed must not pass silently")
			assert.Contains(t, err.Error(), tc.unsafe, "say WHICH name is the problem")
			assert.Contains(t, err.Error(), "--agent-name="+tc.repaired,
				"and hand over a command that runs, not a rule to work out")
			assert.NotContains(t, err.Error(), "register:",
				"the diagnosis must not arrive from two levels down, where it explains nothing")

			// 2. The way out actually works, in place.
			require.NoError(t, runJoin([]string{"--role=esc", "--agent-name=" + tc.repaired, "--project-path=" + proj}))

			after, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
			require.NoError(t, err)
			require.Len(t, after, 1, "the repair must not leave a second session holding the first one's mail")
			assert.Equal(t, sid, after[0].Name(), "same id: the label changed, the identity did not")

			mgr := newSessionManager(config.Config{DataDir: dataDir})
			mf, err := mgr.LoadManifest(sid)
			require.NoError(t, err)
			assert.Equal(t, tc.repaired, mf.AgentName)
			assert.Contains(t, mf.FormerAgentNames, tc.unsafe,
				"the old name stays ON RECORD, which is all the message promises — for `-dash` "+
					"and for anything with `@` no peer can be redirected, because the verbs "+
					"refuse those before the lookup that would do it (see "+
					"TestRepairedSession_OldNameIsRecordedNotForwarded, which runs `tell`)")
			assert.FileExists(t, filepath.Join(dataDir, "sessions", sid, "inbox", "msg-aaaaaaaaaaaa.json"),
				"same inbox — checked against the session's real path, not one rebuilt from a variable")
		})
	}
}

// TestRegister_FusedBasenamesStayAddressable is the consumer end of CRI
// diff-gate P1-2, through the PUBLIC command rather than through the helper.
//
// The unit test in internal/session says two fused basenames get different
// names. This one says the thing that actually matters: that `tell` can still
// reach one of them. Before the digest, `a+b` and `a:b` both derived `a-b`, two
// live sessions answered to it, and `tell a-b` exited 1 with "2 live agents are
// named a-b" — a recipient made unreachable by the fix meant to make names
// reachable.
//
// Through `register`, not `join`, deliberately: the guard the first plan leaned
// on lives in join, and Manager.Register — the surface of the public command —
// has no name-uniqueness check at all. The defence was on one path and the
// defect on the other.
func TestRegister_FusedBasenamesStayAddressable(t *testing.T) {
	dataDir := t.TempDir()
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, ".git"), 0o700)) // one scope
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	plus := filepath.Join(base, "a+b")
	colon := filepath.Join(base, "a:b")
	for _, d := range []string{plus, colon} {
		require.NoError(t, os.MkdirAll(d, 0o700))
		require.NoError(t, runRegister([]string{"--role=esc", "--project-path=" + d}))
	}

	mgr := newSessionManager(config.Config{DataDir: dataDir})
	peers, _, err := collectPeers(mgr, dataDir, 300, 65536, true, "", resolveScope(base))
	require.NoError(t, err)

	names := map[string]string{} // agent name -> project it came from
	for _, p := range peers {
		if prev, dup := names[p.AgentName]; dup {
			t.Fatalf("two projects derived one name %q: %s and %s", p.AgentName, prev, p.ProjectName)
		}
		names[p.AgentName] = p.ProjectName
	}
	require.Len(t, names, 2, "two directories, two agents")

	// And the names are not merely distinct on disk: one of them can be written
	// to. A sender in the same scope, addressing by the derived name.
	sender := filepath.Join(base, "sender")
	require.NoError(t, os.MkdirAll(sender, 0o700))
	require.NoError(t, runJoin([]string{"--role=val", "--agent-name=VAL-sender", "--project-path=" + sender}))
	sid := ""
	peers, _, err = collectPeers(mgr, dataDir, 300, 65536, true, "", resolveScope(base))
	require.NoError(t, err)
	for _, p := range peers {
		if p.AgentName == "VAL-sender" {
			sid = p.SessionID
		}
	}
	require.NotEmpty(t, sid)
	t.Setenv("CAB_SESSION_ID", sid)

	for name := range names {
		var stdout, stderr bytes.Buffer
		require.NoError(t,
			runSendVerb("tell", message.TypeNotify, []string{name, "hi"}, strings.NewReader(""), &stdout, &stderr),
			"the derived name %q must be reachable, which is the whole point of deriving one", name)
	}
}

// TestRepairedSession_OldNameIsRecordedNotForwarded is CRI diff-gate P2-4: the
// repair message used to promise that peers writing to the old name would be
// told where it went, and the test "proved" it by looking at FormerAgentNames
// instead of calling the consumer.
//
// Executed here: for `-dash` the forward CANNOT fire, because runSendVerb
// refuses a leading dash as a flag before any resolution. So the promise is
// reduced to what is true — the old name stays on record — and this test is what
// keeps the two from drifting apart again.
func TestRepairedSession_OldNameIsRecordedNotForwarded(t *testing.T) {
	dataDir := t.TempDir()
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, ".git"), 0o700))
	proj := filepath.Join(base, "work")
	sender := filepath.Join(base, "sender")
	require.NoError(t, os.MkdirAll(proj, 0o700))
	require.NoError(t, os.MkdirAll(sender, 0o700))
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	require.NoError(t, runJoin([]string{"--role=esc", "--agent-name=ESC-safe", "--project-path=" + proj}))
	mgr := newSessionManager(config.Config{DataDir: dataDir})
	peers, _, err := collectPeers(mgr, dataDir, 300, 65536, true, "", resolveScope(base))
	require.NoError(t, err)
	require.Len(t, peers, 1)
	sid := peers[0].SessionID

	plantUnsafeName(t, dataDir, sid, "-dash")
	require.NoError(t, runJoin([]string{"--role=esc", "--agent-name=dash", "--project-path=" + proj}))

	mf, err := mgr.LoadManifest(sid)
	require.NoError(t, err)
	require.Contains(t, mf.FormerAgentNames, "-dash", "the old label is on record")

	require.NoError(t, runJoin([]string{"--role=val", "--agent-name=VAL-sender", "--project-path=" + sender}))
	peers, _, err = collectPeers(mgr, dataDir, 300, 65536, true, "", resolveScope(base))
	require.NoError(t, err)
	for _, p := range peers {
		if p.AgentName == "VAL-sender" {
			t.Setenv("CAB_SESSION_ID", p.SessionID)
		}
	}

	// The new name works.
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSendVerb("tell", message.TypeNotify, []string{"dash", "hi"}, strings.NewReader(""), &stdout, &stderr))

	// The OLD one does not, and not for the reason the message used to imply:
	// it never reaches the lookup that would have redirected it.
	err = runSendVerb("tell", message.TypeNotify, []string{"-dash", "hi"}, strings.NewReader(""), &stdout, &stderr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "takes no flags",
		"refused as a flag before resolution — so no redirect is possible, and the repair message must not promise one")
	assert.NotContains(t, err.Error(), "is now",
		"and it certainly cannot say where the name went")
}

// TestRepairCommand_IsRunnableFromAnywhere runs the command the error PRINTS,
// instead of asserting a substring of it.
//
// CRI re-gate P1: `register --resume --project-path=A` works from any cwd, and
// the repair it suggested was `join --role=... --agent-name=...` with no path —
// so `join` fell back to the CWD, registered a NEW session there, and left the
// legacy one with its mail untouched. The message said "SAME id, SAME inbox"
// and delivered neither.
//
// The previous test asserted that the error CONTAINED `join --agent-name`. It
// did. A substring cannot notice that the command targets a different project —
// fourth time in this lot that a test proved a property adjacent to the one that
// mattered, and the way out was the same each time: run the thing.
func TestRepairCommand_IsRunnableFromAnywhere(t *testing.T) {
	dataDir := t.TempDir()
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, ".git"), 0o700))
	projA := filepath.Join(base, "work")     // where the legacy session lives
	cwdB := filepath.Join(base, "elsewhere") // where the operator happens to be
	require.NoError(t, os.MkdirAll(projA, 0o700))
	require.NoError(t, os.MkdirAll(cwdB, 0o700))
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	require.NoError(t, runJoin([]string{"--role=esc", "--agent-name=ESC-ok", "--project-path=" + projA}))
	entries, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	legacyID := entries[0].Name()
	plantUnsafeName(t, dataDir, legacyID, "-dash")
	plantMsg(t, dataDir, legacyID, "inbox", "msg-aaaaaaaaaaaa", "val00001", "VAL-x", message.TypeQuery, "brief")

	// The refusal, asked for from a DIFFERENT directory — which is what
	// --project-path is for.
	t.Chdir(cwdB)
	err = runRegister([]string{"--role=esc", "--resume", "--project-path=" + projA})
	require.Error(t, err)

	// Pull the command out of the message and RUN IT. Splitting on spaces is
	// exactly what a shell does, so a path with a space would break here — that
	// is the known debt this lot leaves to lot 2, and the fixture avoids it on
	// purpose rather than by accident.
	var cmd []string
	for _, line := range strings.Split(err.Error(), "\n") {
		if f := strings.Fields(line); len(f) > 1 && f[0] == "cab-bridge" {
			cmd = f[1:]
			break
		}
	}
	require.NotEmpty(t, cmd, "the error has to carry a command, not a description:\n%s", err)
	require.Equal(t, "join", cmd[0])

	require.NoError(t, runJoin(cmd[1:]), "the printed command must run as printed")

	// SAME id, SAME inbox, ONE manifest — the three things the message promises.
	after, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
	require.NoError(t, err)
	assert.Len(t, after, 1, "the repair must not register a second session in whatever cwd I was standing in")
	assert.Equal(t, legacyID, after[0].Name(), "SAME id")
	assert.FileExists(t, filepath.Join(dataDir, "sessions", legacyID, "inbox", "msg-aaaaaaaaaaaa.json"), "SAME inbox")

	mgr := newSessionManager(config.Config{DataDir: dataDir})
	mf, err := mgr.LoadManifest(legacyID)
	require.NoError(t, err)
	assert.Equal(t, "dash", mf.AgentName)
	assert.Contains(t, mf.FormerAgentNames, "-dash")

	// And the session is addressable now: the point of the whole exercise.
	require.NoError(t, runJoin([]string{"--role=val", "--agent-name=VAL-sender", "--project-path=" + cwdB}))
	peers, _, perr := collectPeers(mgr, dataDir, 300, 65536, true, "", resolveScope(base))
	require.NoError(t, perr)
	for _, p := range peers {
		if p.AgentName == "VAL-sender" {
			t.Setenv("CAB_SESSION_ID", p.SessionID)
		}
	}
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSendVerb("tell", message.TypeNotify, []string{"dash", "hi"}, strings.NewReader(""), &stdout, &stderr))
}

// TestJoin_RelativeProjectPathIsTheSameProject — CRI re-gate P2.
//
// `pp` reached scope detection, the occupant lookup and the name derivation as
// the caller typed it. With `--project-path=.` that meant: a name derived from
// `.` (`ESC-_2E`), and findSessionHere comparing `Clean(".")` against the stored
// ABSOLUTE path, so the occupant was invisible.
//
// The shape suggests "two sessions", and I assumed that. Executed, it is worse
// in a different way: the cross-name guard then refuses the re-join with "this
// project already has a LIVE ESC-_2E" — the session locks its own owner out,
// permanently, which is F-110 again. Written down because the deduction was
// wrong and only running it said so.
func TestJoin_RelativeProjectPathIsTheSameProject(t *testing.T) {
	dataDir := t.TempDir()
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, ".git"), 0o700))
	work := filepath.Join(base, "work")
	require.NoError(t, os.MkdirAll(work, 0o700))
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	t.Chdir(work)

	require.NoError(t, runJoin([]string{"--role=esc", "--project-path=."}))
	entries, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	sid := entries[0].Name()

	mgr := newSessionManager(config.Config{DataDir: dataDir})
	mf, err := mgr.LoadManifest(sid)
	require.NoError(t, err)
	assert.Equal(t, "ESC-work", mf.AgentName,
		"the name comes from the directory, not from how the flag spelled it")

	// The re-arm the skill prescribes, in all three spellings of one place.
	for _, spelling := range []string{".", work, filepath.Join(work, ".")} {
		require.NoError(t, runJoin([]string{"--role=esc", "--project-path=" + spelling}),
			"%q is the same project: a re-join must find the occupant, not stop on it", spelling)
		after, rerr := os.ReadDir(filepath.Join(dataDir, "sessions"))
		require.NoError(t, rerr)
		assert.Len(t, after, 1, "%q must not produce a second session", spelling)
		assert.Equal(t, sid, after[0].Name(), "%q resolves to the same session", spelling)
	}
}

// TestRegister_SaysWhenTheNameIsNotTheDirectorys — CRI re-gate P2-5.
//
// SanitizeDerivedName returns `changed` so the caller can say so out loud, and
// `join` did. `register` printed the id and nothing else, so `my repo` quietly
// became `my_20repo`. The two commands differed for no reason anybody chose.
func TestRegister_SaysWhenTheNameIsNotTheDirectorys(t *testing.T) {
	dataDir := t.TempDir()
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, ".git"), 0o700))
	spaced := filepath.Join(base, "my repo")
	plain := filepath.Join(base, "plain")
	require.NoError(t, os.MkdirAll(spaced, 0o700))
	require.NoError(t, os.MkdirAll(plain, 0o700))
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	stderr := captureStderr(t, func() {
		require.NoError(t, runRegister([]string{"--role=esc", "--json=false", "--project-path=" + spaced}))
	})
	assert.Contains(t, stderr, "my repo", "name the directory")
	assert.Contains(t, stderr, "my_20repo", "and the name it actually got")

	// The quiet cases stay quiet: nothing to announce when nothing changed, and
	// nothing to announce when the caller chose the name.
	stderr = captureStderr(t, func() {
		require.NoError(t, runRegister([]string{"--role=esc", "--json=false", "--project-path=" + plain}))
	})
	assert.NotContains(t, stderr, "deriving from", "an unchanged name is not news")

	stderr = captureStderr(t, func() {
		require.NoError(t, runRegister([]string{"--role=val", "--json=false", "--agent-name=VAL-chosen", "--project-path=" + spaced, "--force-new"}))
	})
	assert.NotContains(t, stderr, "derived", "nothing was derived: the caller said the name")
}

// TestRegister_NoticeDoesNotAnnounceADerivationThatWillNotHappen — CRI re-gate
// P2-1, and the branch the first version of the notice did not have.
//
// That version ran BEFORE Manager.Register, so on `--resume` it printed
// `deriving from "a_2Bb"` and the same command then returned `ESC-custom`: two
// surfaces of one command stating two different identities, the printed one
// false. Lot 1 had just established that a resume ADOPTS the existing name — so
// the notice was describing a derivation that was not going to occur.
//
// Declaring an outcome before it happens is the oldest class in this project,
// and I put it back in the lot that closes it. The test is on the resume branch
// because that is the one that was missing, not the one that was easy.
func TestRegister_NoticeDoesNotAnnounceADerivationThatWillNotHappen(t *testing.T) {
	dataDir := t.TempDir()
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, ".git"), 0o700))
	odd := filepath.Join(base, "a+b") // a directory whose basename IS escaped
	require.NoError(t, os.MkdirAll(odd, 0o700))
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	// Registered with a name the caller chose, in a directory that would derive
	// something else entirely.
	require.NoError(t, runRegister([]string{"--role=esc", "--json=false", "--agent-name=ESC-custom", "--project-path=" + odd}))
	entries, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	sid := entries[0].Name()

	mgr := newSessionManager(config.Config{DataDir: dataDir})
	mf, err := mgr.LoadManifest(sid)
	require.NoError(t, err)
	mf.PID = 999999999 // abandoned, so the resume may take it
	require.NoError(t, mgr.SaveManifest(mf))

	stderr := captureStderr(t, func() {
		require.NoError(t, runRegister([]string{"--role=esc", "--json=false", "--resume", "--project-path=" + odd}))
	})
	assert.NotContains(t, stderr, "derived",
		"a resume adopts the existing name — announcing a derivation states an outcome that will not happen")
	assert.NotContains(t, stderr, "a_2Bb",
		"and it must certainly not name the identity the session did NOT take")

	after, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.Equal(t, sid, after[0].Name(), "same session")
	mf, err = mgr.LoadManifest(sid)
	require.NoError(t, err)
	assert.Equal(t, "ESC-custom", mf.AgentName, "and the name the surfaces must agree on")
}

// TestOutcome_IsReadFromTheResultNotTheClock — CRI re-gate, and the case my
// previous test could not see because it left StartedAt alone.
//
// `register` and `join` both decided "was this a resume?" by comparing the
// manifest's StartedAt with a clock reading taken just before the call. But
// StartedAt was persisted in an earlier life: it says when the session BEGAN,
// not what this call did. With a StartedAt in the future — clock rollback, a
// restored VM, a manifest carried from another machine — both said the opposite
// of what happened: `register` announced a derivation it had not performed, and
// `join` reported `registered-new` for a session it had just reclaimed.
//
// The temporal form of "which object is this a property of": a persisted field
// is evidence about THEN. The answer was in the same file eleven lines below —
// LastReclaim, set in memory by this very call, already used there for the
// reclaim notice.
//
// The fixture puts StartedAt in 2099 so the OLD criterion is guaranteed wrong,
// which is the only way this test can fail for the right reason.
func TestOutcome_IsReadFromTheResultNotTheClock(t *testing.T) {
	dataDir := t.TempDir()
	base := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(base, ".git"), 0o700))
	odd := filepath.Join(base, "a+b") // basename that would derive something else
	require.NoError(t, os.MkdirAll(odd, 0o700))
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	require.NoError(t, runRegister([]string{"--role=esc", "--json=false", "--agent-name=ESC-custom", "--project-path=" + odd}))
	entries, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	sid := entries[0].Name()

	mgr := newSessionManager(config.Config{DataDir: dataDir})
	skew := func() {
		t.Helper()
		mf, lerr := mgr.LoadManifest(sid)
		require.NoError(t, lerr)
		mf.PID = 999999999                                         // abandoned, so a resume may take it
		mf.StartedAt = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC) // the future
		require.NoError(t, mgr.SaveManifest(mf))
	}

	t.Run("register does not announce a derivation it did not perform", func(t *testing.T) {
		skew()
		stderr := captureStderr(t, func() {
			require.NoError(t, runRegister([]string{"--role=esc", "--json=false", "--resume", "--project-path=" + odd}))
		})
		assert.NotContains(t, stderr, "derived", "the name was adopted, not derived — whatever the clock says")
		assert.NotContains(t, stderr, "a_2Bb", "and the identity it did NOT take must not be printed")

		mf, lerr := mgr.LoadManifest(sid)
		require.NoError(t, lerr)
		assert.Equal(t, "ESC-custom", mf.AgentName)
	})

	t.Run("join reports resumed, not registered-new", func(t *testing.T) {
		skew()
		out := captureStdout(t, func() {
			require.NoError(t, runJoin([]string{"--role=esc", "--project-path=" + odd}))
		})
		assert.Contains(t, out, "resumed", "the session was reclaimed: id and inbox are the same one")
		assert.NotContains(t, out, "registered-new")

		after, rerr := os.ReadDir(filepath.Join(dataDir, "sessions"))
		require.NoError(t, rerr)
		assert.Len(t, after, 1, "and nothing new was created, which is what makes the label a lie")
		assert.Equal(t, sid, after[0].Name())
	})
}

// A derived agent name is half ROLE and half DIRECTORY. The directory half went
// through SanitizeDerivedName from the first day of F-124; the role half went
// through strings.ToUpper and nothing else — and a role is deliberately not an
// enum. So `--role='browser tester'`, which register accepts and README
// advertises, derived `BROWSER TESTER-002` and Register refused it two levels
// down: a join that failed on a NAME the caller never chose, quoting the
// agent-name grammar at somebody who had only typed a role.
//
// Found by the wiring harness (shellarg_wiring_test.go) on its first run, while
// looking for something else entirely.
func TestJoin_ACustomRoleStillDerivesAnAddressableName(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	proj := t.TempDir()

	_, stderr := captureStdoutStderr(t, func() {
		require.NoError(t, runJoin([]string{"--role=browser tester", "--project-path=" + proj}),
			"a legal role must not fail a join on a name nobody typed")
	})
	assert.Contains(t, stderr, "cannot be used as it stands in an agent name",
		"and the reshaping is announced, exactly as the directory's is — a silent rename of one's own identity is the substitution this project keeps removing")

	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)
	peers, _, err := collectPeers(mgr, dataDir, cfg.StaleSeconds, cfg.MaxMessageBytes, true, "", "")
	require.NoError(t, err)
	require.Len(t, peers, 1, "the session must exist: the point is that the join SUCCEEDED")
	assert.NoError(t, session.ValidateAgentName(peers[0].AgentName),
		"whatever is derived must be addressable, whatever the role was")
	assert.Equal(t, "BROWSER-TESTER-"+filepath.Base(proj), peers[0].AgentName)
	assert.Equal(t, "browser tester", peers[0].Role, "the ROLE itself is stored as typed — only the name is reshaped")
}

// And the other half of that fix: every role anybody actually uses passes
// through untouched, so this changes nothing for anyone not using a custom one.
func TestRoleUpper_LeavesEverySelectableRoleAlone(t *testing.T) {
	t.Parallel()
	for _, rc := range session.SelectableRoles {
		prefix, changed := roleUpper(rc.Name)
		assert.False(t, changed, "%q is a standard role and must not be reshaped", rc.Name)
		assert.Equal(t, strings.ToUpper(rc.Name), prefix)
	}

	for _, tc := range []struct{ role, want string }{
		{"browser tester", "BROWSER-TESTER"},
		{"qa/e2e", "QA-E2E"},
		{"-leading", "LEADING"},
		{"@@@", "session"}, // nothing addressable survives: the fallback, not an empty prefix
	} {
		prefix, changed := roleUpper(tc.role)
		assert.True(t, changed, "%q needs reshaping", tc.role)
		assert.Equal(t, tc.want, prefix)
		assert.NoError(t, session.ValidateAgentName(prefix+"-dir"), "and the whole derived name is addressable")
	}
}
