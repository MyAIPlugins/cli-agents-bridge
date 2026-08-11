package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reconnectDeadPID is a PID very unlikely to exist, so IsProcessAlive reports
// false — used to simulate an abandoned (post-compact/post-register-death)
// session whose owning process is gone.
const reconnectDeadPID = 2147480000

// abandon simulates the real post-compact / post-register-death state: a dead
// manifest PID. Required because Register writes os.Getpid() (the LIVE test
// process), which reconnect would otherwise treat as a live, non-resumable
// session. Liveness is the manifest PID (a running listen keeps it alive), not
// the lock.
func abandon(t *testing.T, mgr *Manager, id string) {
	t.Helper()
	mf, err := mgr.LoadManifest(id)
	require.NoError(t, err)
	mf.PID = reconnectDeadPID
	require.NoError(t, mgr.SaveManifest(mf))
}

// registerReusable registers a session, releases its lock, and marks it
// abandoned (dead PID) so a later resume can take it. With a non-zero age the
// heartbeat is backdated via the injected clock. ForceNew lets multiple
// same-identity sessions be planted for the multi-match test.
func registerReusable(t *testing.T, mgr *Manager, projDir, agent, role, scope string, age time.Duration) string {
	t.Helper()
	saved := mgr.Now
	base := time.Now().UTC()
	mgr.Now = func() time.Time { return base.Add(age) }
	mf, rel, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir, AgentName: agent, Role: role, Scope: scope, ForceNew: true,
	})
	require.NoError(t, err)
	require.NoError(t, rel())
	mgr.Now = saved
	abandon(t, mgr, mf.SessionID)
	return mf.SessionID
}

func TestRegister_Resume_ResumesOwnStale(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	projDir := t.TempDir()
	mgr := NewManager(dataDir, time.Second)

	mf1, rel1, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir, AgentName: "ESC-x", Role: RoleEsc, Scope: "/proj/root",
	})
	require.NoError(t, err)
	require.NoError(t, rel1())     // release so the session is reusable
	abandon(t, mgr, mf1.SessionID) // owning process gone (post-compact)

	// Plant an inbox message to prove preservation across the resume.
	inbox := filepath.Join(dataDir, "sessions", mf1.SessionID, "inbox")
	require.NoError(t, os.WriteFile(filepath.Join(inbox, "keep.json"), []byte("{}"), 0o600))

	mf2, rel2, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir, AgentName: "ESC-x", Role: RoleEsc, Scope: "/proj/root", Resume: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel2() })

	assert.Equal(t, mf1.SessionID, mf2.SessionID, "resume must reuse the same sessionId")
	assert.FileExists(t, filepath.Join(inbox, "keep.json"), "inbox preserved across resume")
	assert.Equal(t, os.Getpid(), mf2.PID, "resume adopts the current PID")
}

// TestRegister_Resume_ReclaimsLiveOrphan is the B-2 INVERSION of F-27 (was
// TestRegister_Resume_DoesNotStealLive): a matching session whose manifest PID
// is alive is now an ORPHAN to reclaim, not a live owner to refuse. A live PID
// proves only that a `listen` survived (e.g. a /clear killed the Claude that
// owned it, leaving its background listen running); the identity + --resume is
// the semantic claim to that session's continuity. register --resume reuses the
// same session, adopts our PID, bumps the listener generation (revoking the
// orphan), and reports the supersession via mf.LastReclaim.
func TestRegister_Resume_ReclaimsLiveOrphan(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	projDir := t.TempDir()
	mgr := NewManager(dataDir, time.Second)

	mf1, rel1, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir, AgentName: "ESC-x", Role: RoleEsc, Scope: "/proj/root",
	})
	require.NoError(t, err)
	require.NoError(t, rel1())

	// A live orphan: manifest PID = a foreign, known-alive PID (1 = init/launchd).
	mf1.PID = 1
	require.NoError(t, mgr.SaveManifest(mf1))

	mf2, rel2, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir, AgentName: "ESC-x", Role: RoleEsc, Scope: "/proj/root", Resume: true,
	})
	require.NoError(t, err, "B-2: a live orphan is reclaimed, not refused")
	t.Cleanup(func() { _ = rel2() })

	assert.Equal(t, mf1.SessionID, mf2.SessionID, "reclaim reuses the same session")
	assert.Equal(t, os.Getpid(), mf2.PID, "reclaim adopts our PID")
	require.NotNil(t, mf2.LastReclaim, "a reclaim reports what it superseded")
	assert.Equal(t, 1, mf2.LastReclaim.NewGeneration, "the listener generation is bumped (orphan revoked)")

	o, ok, rerr := mgr.ReadListener(mf1.SessionID)
	require.NoError(t, rerr)
	require.True(t, ok)
	assert.Equal(t, 1, o.Generation)
	assert.Equal(t, 0, o.PID, "reclaim-pending until the new listen claims")
}

// TestRegister_ForceNew_DoesNotReclaim is B-2 test 7: --force-new is a
// DELIBERATE second instance — it bypasses tryReuse entirely, so it creates a
// fresh session and does NOT revoke the previous session's listener.
func TestRegister_ForceNew_DoesNotReclaim(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	projDir := t.TempDir()
	mgr := NewManager(dataDir, time.Second)

	mf1, rel1, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir, AgentName: "ESC-x", Role: RoleEsc, Scope: "/proj/root",
	})
	require.NoError(t, err)
	require.NoError(t, rel1())
	o1, err := mgr.ClaimListener(mf1.SessionID)
	require.NoError(t, err)

	mf2, rel2, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir, AgentName: "ESC-x", Role: RoleEsc, Scope: "/proj/root", ForceNew: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel2() })

	assert.NotEqual(t, mf1.SessionID, mf2.SessionID, "force-new creates a fresh session")
	assert.Nil(t, mf2.LastReclaim, "force-new does not reclaim")
	assert.True(t, mgr.IsListenerCurrent(mf1.SessionID, o1.Token), "the previous listener is NOT revoked by force-new")
}

func TestRegister_Resume_NoMatch_RegistersNew(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	projDir := t.TempDir()
	mgr := NewManager(dataDir, time.Second)

	mf, rel, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir, AgentName: "ESC-fresh", Role: RoleEsc, Scope: "/proj/root", Resume: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel() })
	assert.NotEmpty(t, mf.SessionID, "no identity match -> a fresh session is registered")
}

func TestRegister_Resume_MultiMatch_ResumesMostRecent(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	projDir := t.TempDir()
	mgr := NewManager(dataDir, time.Second)

	older := registerReusable(t, mgr, projDir, "ESC-x", RoleEsc, "/proj/root", -2*time.Hour)
	newer := registerReusable(t, mgr, projDir, "ESC-x", RoleEsc, "/proj/root", -1*time.Hour)

	mf, rel, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir, AgentName: "ESC-x", Role: RoleEsc, Scope: "/proj/root", Resume: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel() })

	assert.Equal(t, newer, mf.SessionID, "multi-match must resume the most recent session")
	assert.NotEqual(t, older, mf.SessionID)
}

// TestRegister_Resume_LegacyNoScope_MatchesAndBackfills: a pre-F-17 session
// (empty scope) is matched by agent-name + projectPath prefix, and the resume
// backfills the derived F-17 scope into it.
func TestRegister_Resume_LegacyNoScope_MatchesAndBackfills(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	projDir := t.TempDir()
	mgr := NewManager(dataDir, time.Second)

	mf1, rel1, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: projDir, AgentName: "ESC-x", Role: RoleEsc, // no Scope (legacy)
	})
	require.NoError(t, err)
	require.NoError(t, rel1())
	require.Empty(t, mf1.Scope)
	abandon(t, mgr, mf1.SessionID)

	subDir := filepath.Join(projDir, "sub")
	require.NoError(t, os.MkdirAll(subDir, 0o700))

	mf2, rel2, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: subDir, AgentName: "ESC-x", Role: RoleEsc, Scope: projDir, Resume: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel2() })

	assert.Equal(t, mf1.SessionID, mf2.SessionID, "legacy session matched by agent-name + projectPath prefix")
	assert.Equal(t, projDir, mf2.Scope, "resume backfills the F-17 scope into the legacy session")
}

// TestRegister_Resume_DifferentRole_NoMatch: role is part of the identity, so a
// session with a different role is not resumed (a fresh one is created). VAL and
// ESC use different project dirs but share a scope, as they really do.
func TestRegister_Resume_DifferentRole_NoMatch(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	mgr := NewManager(dataDir, time.Second)

	val, rel1, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: t.TempDir(), AgentName: "AGENT", Role: RoleVal, Scope: "/proj/root",
	})
	require.NoError(t, err)
	require.NoError(t, rel1())

	// Same agent-name + scope, different role and project dir -> must NOT resume
	// the VAL; a fresh ESC session is registered instead.
	mf, rel2, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: t.TempDir(), AgentName: "AGENT", Role: RoleEsc, Scope: "/proj/root", Resume: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rel2() })
	assert.NotEqual(t, val.SessionID, mf.SessionID, "different role must not be resumed")
}

// --- F-124: a DERIVED name must not be part of the resume identity ----------

// TestResume_NoNameGivenFindsTheSessionWhateverItIsCalled is the second P1 of
// the re-gate, and the version WITHOUT an upgrade in it — which is what showed
// the real cause.
//
// The gate described it as "the upgrade breaks resume", and that is one face:
// the identity was built with the CURRENT derivation, so any change to the
// derivation orphaned sessions written by the previous binary. But the same
// defect fires with one binary and no derivation change at all: register with an
// explicit name, then resume WITHOUT one, and the resume looks for the DERIVED
// name, does not find the explicit one, and starts a second session on the same
// directory — mail included. Reproduced on the real binary before this fix:
// acc8c9e9 'ESC-explicit' with 1 message, c8354d2b 'work' with 0.
//
// So "teach the reader the old derivations" would have fixed neither: here there
// is no historic derivation, there is a name somebody chose.
func TestResume_NoNameGivenFindsTheSessionWhateverItIsCalled(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, time.Second)
	proj := filepath.Join(dir, "work")
	require.NoError(t, os.MkdirAll(proj, 0o700))

	first, release, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: proj, AgentName: "ESC-explicit", Role: RoleEsc, Scope: proj,
	})
	require.NoError(t, err)
	require.NoError(t, release())
	abandon(t, mgr, first.SessionID)

	// Mail arrives before the resume: the whole cost of getting this wrong.
	inbox := filepath.Join(dir, "sessions", first.SessionID, "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(inbox, "msg-aaaaaaaaaaaa.json"), []byte("{}"), 0o600))

	again, release2, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: proj, Role: RoleEsc, Scope: proj, Resume: true,
	})
	require.NoError(t, err)
	require.NoError(t, release2())

	assert.Equal(t, first.SessionID, again.SessionID,
		"the resume must find the session that is HERE, not the one the current derivation would have named")
	assert.Equal(t, "ESC-explicit", again.AgentName,
		"and adopt its name: with no name asked for, the name is an output of the resume")

	entries, err := os.ReadDir(filepath.Join(dir, "sessions"))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "never a second session holding the first one's mail")
	assert.FileExists(t, filepath.Join(inbox, "msg-aaaaaaaaaaaa.json"))
}

// The upgrade face of the same defect, with a FIXTURE written the way the
// previous binary wrote it — because a test that creates and resumes with the
// same code cannot see a change of derivation. This is the two-version test the
// gate asked for, without needing two binaries: `a-b` is what `725347c` stored
// for a directory called `a+b`, and `1b09ba0` stored `a-b-<digest>`. Neither is
// what the current code derives.
func TestResume_FindsASessionNamedByAnEarlierDerivation(t *testing.T) {
	for _, historic := range []struct {
		label, name string
	}{
		{"the substituting derivation (725347c)", "a-b"},
		{"the digest derivation (1b09ba0)", "a-b-59f4"},
	} {
		t.Run(historic.label, func(t *testing.T) {
			dir := t.TempDir()
			mgr := NewManager(dir, time.Second)
			proj := filepath.Join(dir, "a+b")
			require.NoError(t, os.MkdirAll(proj, 0o700))

			// Written by the older binary: same project, same role, a name this
			// code would never derive.
			first, release, err := mgr.Register(context.Background(), RegisterOpts{
				ProjectPath: proj, AgentName: historic.name, Role: RoleEsc, Scope: proj,
			})
			if err != nil {
				// `a+b` is refused as a TYPED name today, so plant it the way the
				// old binary left it on disk: as a derived default nobody typed.
				first, release, err = mgr.Register(context.Background(), RegisterOpts{
					ProjectPath: proj, AgentName: "placeholder", Role: RoleEsc, Scope: proj,
				})
				require.NoError(t, err)
				require.NoError(t, release())
				mf, lerr := mgr.LoadManifest(first.SessionID)
				require.NoError(t, lerr)
				mf.AgentName = historic.name
				require.NoError(t, mgr.SaveManifest(mf))
			} else {
				require.NoError(t, release())
			}
			abandon(t, mgr, first.SessionID)

			again, release2, err := mgr.Register(context.Background(), RegisterOpts{
				ProjectPath: proj, Role: RoleEsc, Scope: proj, Resume: true,
			})
			require.NoError(t, err)
			require.NoError(t, release2())

			assert.Equal(t, first.SessionID, again.SessionID,
				"a session written by an older derivation must still be re-entered, or the upgrade orphans its inbox")
			assert.Equal(t, historic.name, again.AgentName, "and keeps the name it already had")
		})
	}
}

// TestResume_TwoNamesOnOnePathIsAmbiguousNotATieBreak REPLACES a test that
// certified the wrong property, and the replacement is the finding.
//
// The first version asserted that the pick was DETERMINISTIC — five runs, same
// answer — and it was true. But repeatability is not correctness: the pick took
// the most recent heartbeat, which meant an empty mailbox over one holding a
// message, silently. "Making a choice repeatable does not make it right"
// (CRI re-gate P1-1), and §2.2 of the mailbox design already said that where
// duplicates exist the system fails closed instead of choosing.
//
// Third time in this lot that a test of mine proved a property ADJACENT to the
// one that was needed. The shape is always the same: I assert what the code
// does, and the question was what it should do.
func TestResume_TwoNamesOnOnePathIsAmbiguousNotATieBreak(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, time.Second)
	proj := filepath.Join(dir, "work")
	require.NoError(t, os.MkdirAll(proj, 0o700))

	older, release, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: proj, AgentName: "ESC-older", Role: RoleEsc, Scope: proj,
	})
	require.NoError(t, err)
	require.NoError(t, release())
	abandon(t, mgr, older.SessionID)

	// A second one on the SAME path: only ForceNew can produce this, and it is
	// the state the old identity rule would leave behind every time a name
	// changed — so it is not hypothetical, it is the wreckage of the defect.
	newer, release2, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: proj, AgentName: "ESC-newer", Role: RoleEsc, Scope: proj, ForceNew: true,
	})
	require.NoError(t, err)
	require.NoError(t, release2())
	abandon(t, mgr, newer.SessionID)

	// Make "most recent" unambiguous instead of relying on write ordering.
	mf, err := mgr.LoadManifest(newer.SessionID)
	require.NoError(t, err)
	mf.LastHeartbeat = time.Now().UTC()
	require.NoError(t, mgr.SaveManifest(mf))
	mf, err = mgr.LoadManifest(older.SessionID)
	require.NoError(t, err)
	mf.LastHeartbeat = time.Now().UTC().Add(-time.Hour)
	require.NoError(t, mgr.SaveManifest(mf))

	// Mail in the OLDER one, so a silent pick has something real to lose.
	inbox := filepath.Join(dir, "sessions", older.SessionID, "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(inbox, "msg-aaaaaaaaaaaa.json"), []byte("{}"), 0o600))

	_, _, err = mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: proj, Role: RoleEsc, Scope: proj, Resume: true,
	})
	require.Error(t, err, "two names on one path are two identities: never a silent pick")
	assert.ErrorIs(t, err, ErrAmbiguousResume)
	assert.Contains(t, err.Error(), "ESC-older", "the way out needs both names to choose between")
	assert.Contains(t, err.Error(), "ESC-newer")
	assert.Contains(t, err.Error(), "--agent-name", "and the flag that resolves it")

	// Fail-closed means nothing moved: no third session, and the mail is where
	// it was.
	entries, err := os.ReadDir(filepath.Join(dir, "sessions"))
	require.NoError(t, err)
	assert.Len(t, entries, 2, "a refused resume must not register a fresh session either")
	assert.FileExists(t, filepath.Join(inbox, "msg-aaaaaaaaaaaa.json"))

	// And naming one resumes it: the ambiguity is in the question, not in the
	// sessions, so the way out is an answer and never a cleanup.
	got, release3, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: proj, AgentName: "ESC-older", Role: RoleEsc, Scope: proj, Resume: true,
	})
	require.NoError(t, err)
	require.NoError(t, release3())
	assert.Equal(t, older.SessionID, got.SessionID, "the one I named, not the most recent one")
}

// The complement, and it must keep working: several records under ONE name are
// the ordinary post-compact case, not an ambiguity — most-recent-first still
// applies there. Without this the fix above would turn a normal resume into an
// error the moment a session had ever been reclaimed.
func TestResume_SeveralRecordsOfOneNameAreStillOneIdentity(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, time.Second)
	proj := filepath.Join(dir, "work")
	require.NoError(t, os.MkdirAll(proj, 0o700))

	first, release, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: proj, AgentName: "ESC-same", Role: RoleEsc, Scope: proj,
	})
	require.NoError(t, err)
	require.NoError(t, release())
	abandon(t, mgr, first.SessionID)

	second, release2, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: proj, AgentName: "ESC-same", Role: RoleEsc, Scope: proj, ForceNew: true,
	})
	require.NoError(t, err)
	require.NoError(t, release2())
	abandon(t, mgr, second.SessionID)

	mf, err := mgr.LoadManifest(second.SessionID)
	require.NoError(t, err)
	mf.LastHeartbeat = time.Now().UTC()
	require.NoError(t, mgr.SaveManifest(mf))

	got, release3, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: proj, Role: RoleEsc, Scope: proj, Resume: true,
	})
	require.NoError(t, err, "one name, several records: that is continuity, not ambiguity")
	require.NoError(t, release3())
	assert.Equal(t, second.SessionID, got.SessionID, "and the most recent one is the continuation")
}

// CRI re-gate P1-2: the resume must not adopt a name the rest of the tool
// refuses. `join` stops on such a session and offers the in-place repair;
// `register --resume` walked past that gate and returned `resumed` with a name
// `tell` rejects before any lookup. Two doors, opposite answers, one state.
func TestResume_RefusesToAdoptAnUnaddressableName(t *testing.T) {
	for _, name := range []string{"-dash", "a+b", "ESC bridge"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			mgr := NewManager(dir, time.Second)
			proj := filepath.Join(dir, "work")
			require.NoError(t, os.MkdirAll(proj, 0o700))

			first, release, err := mgr.Register(context.Background(), RegisterOpts{
				ProjectPath: proj, AgentName: "ESC-placeholder", Role: RoleEsc, Scope: proj,
			})
			require.NoError(t, err)
			require.NoError(t, release())
			// Left on disk the way a pre-grammar binary left it.
			mf, err := mgr.LoadManifest(first.SessionID)
			require.NoError(t, err)
			mf.AgentName = name
			require.NoError(t, mgr.SaveManifest(mf))
			abandon(t, mgr, first.SessionID)

			_, _, err = mgr.Register(context.Background(), RegisterOpts{
				ProjectPath: proj, Role: RoleEsc, Scope: proj, Resume: true,
			})
			require.Error(t, err, "resuming into an unaddressable name hands back a peer nobody can write to")
			assert.ErrorIs(t, err, ErrUnaddressableResume)
			assert.Contains(t, err.Error(), "cab-bridge join --role=esc --agent-name=",
				"and the remediation is the one join already implements, not a second copy of it")

			// Nothing was created and nothing was renamed: fail-closed.
			entries, rerr := os.ReadDir(filepath.Join(dir, "sessions"))
			require.NoError(t, rerr)
			assert.Len(t, entries, 1)
			after, rerr := mgr.LoadManifest(first.SessionID)
			require.NoError(t, rerr)
			assert.Equal(t, name, after.AgentName, "a refused resume does not migrate the name behind anyone's back")
		})
	}
}

// shellArgvOf runs the command a message offers, against a stand-in for its
// first word that reports the argv it was handed. Twelve lines duplicated from
// cmd/cab-bridge rather than shared: a test helper crossing a package boundary
// would need a non-test package to live in, and that is a worse trade.
func shellArgvOf(t *testing.T, text, start string) []string {
	t.Helper()
	i := strings.Index(text, start)
	require.GreaterOrEqual(t, i, 0, "no command starting %q in:\n%s", start, text)
	cmd := text[i:]
	if j := strings.IndexAny(cmd, "\n`"); j >= 0 {
		cmd = cmd[:j]
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, strings.Fields(cmd)[0]),
		[]byte("#!/bin/sh\nprintf '%s\\0' \"$@\"\n"), 0o700))
	c := exec.Command("/bin/sh", "-c", cmd)
	c.Env = []string{"PATH=" + dir}
	out, err := c.Output()
	require.NoError(t, err, "the shell refused the emitted command — it was not rendered: %s", cmd)
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
}

// The repair command this refusal prints carries BOTH a role and a project path,
// and until F-124's second lot it rendered the path and left the role bare —
// one argument of two, which is how a producer looks when nobody asked which
// values it interpolates rather than which one is in front of them.
//
// Roles are not an enum (internal/routing/role.go), and a project path comes
// from the filesystem: neither is under the tool's control, and both land in a
// line whose entire purpose is to be pasted into a shell.
func TestResume_TheRepairCommandSurvivesAPaste(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir, time.Second)
	proj := filepath.Join(dir, "Alan's twin")
	require.NoError(t, os.MkdirAll(proj, 0o700))

	// Registered under the custom role too: a resume matches on the role
	// (wantRole below), so an agent that registered as `esc` and resumes as
	// something else does not reuse — it registers anew, and this test would
	// then be asserting on a session that was never refused.
	first, release, err := mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: proj, AgentName: "ESC-placeholder", Role: "browser tester", Scope: proj,
	})
	require.NoError(t, err)
	require.NoError(t, release())

	mf, err := mgr.LoadManifest(first.SessionID)
	require.NoError(t, err)
	mf.AgentName = "ESC bridge" // the way a pre-grammar binary left it
	require.NoError(t, mgr.SaveManifest(mf))
	abandon(t, mgr, first.SessionID)

	_, _, err = mgr.Register(context.Background(), RegisterOpts{
		ProjectPath: proj, Role: "browser tester", Scope: proj, Resume: true,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, ErrUnaddressableResume)

	argv := shellArgvOf(t, err.Error(), "cab-bridge join")
	assert.Contains(t, argv, "--role=browser tester", "the role must arrive as ONE argument")
	assert.Contains(t, argv, "--project-path="+proj, "and so must the path — this is the pair that was half-rendered")
	assert.Contains(t, argv, "--agent-name=ESC-bridge")
}
