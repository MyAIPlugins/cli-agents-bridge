package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// The two hints of legacy_tree.go are the only reads of the old tree this binary
// makes (SEC gate round 3, finding A). What they must never do is follow a link:
// a link planted at <base>/sessions would otherwise make an error message open,
// count and disclose the contents of a directory somewhere else.

func legacyCfg(t *testing.T, base string) config.Config {
	t.Helper()
	cfg := fixtureConfig(t, filepath.Join(base, config.LayoutDir))
	cfg.BaseDir = base
	return cfg
}

func plantOldSessionDirs(t *testing.T, root string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		require.NoError(t, os.MkdirAll(filepath.Join(root, id), 0o700))
	}
}

func TestLegacyHints_ARealOldTreeIsNamedAndCounted(t *testing.T) {
	base := t.TempDir()
	plantOldSessionDirs(t, filepath.Join(base, "sessions"), "oldsess1", "oldsess2", "oldsess3")
	cfg := legacyCfg(t, base)

	got := legacyRecipientHint(cfg)
	assert.Contains(t, got, "3 session(s)")
	assert.Contains(t, got, filepath.Join(base, "sessions"))
	assert.Contains(t, got, "not reachable from this version")

	hint := legacySessionHint(cfg, "oldsess2")
	assert.Contains(t, hint, "cab-bridge join", "it says what to do")
	assert.Contains(t, hint, "from before the update")
	assert.Empty(t, legacySessionHint(cfg, "neverhere"), "an id the old tree does not have gets no hint")
}

func TestLegacyHints_NothingToSayWhenThereIsNothing(t *testing.T) {
	base := t.TempDir()
	cfg := legacyCfg(t, base)
	assert.Empty(t, legacyRecipientHint(cfg), "no old tree")
	assert.Empty(t, legacySessionHint(cfg, "oldsess1"))

	require.NoError(t, os.MkdirAll(filepath.Join(base, "sessions"), 0o700))
	assert.Empty(t, legacyRecipientHint(cfg), "an old tree with no entries is not worth a sentence")
}

func TestLegacyHints_TheCountIsCapped(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "sessions")
	require.NoError(t, os.MkdirAll(root, 0o700))
	for i := 0; i < legacyCountCap+1; i++ {
		require.NoError(t, os.Mkdir(filepath.Join(root, "s"+strconv.Itoa(i)), 0o700))
	}
	got := legacyRecipientHint(legacyCfg(t, base))
	assert.Contains(t, got, "more than "+strconv.Itoa(legacyCountCap), "the number is a hint, not an inventory")
	assert.NotContains(t, got, strconv.Itoa(legacyCountCap+1))
}

// SEC-A: three ways the old tree's root can be something other than a real
// directory. In each, both hints are empty, and the place the link points at is
// exactly as it was.
func TestLegacyHints_NeverFollowALinkOrAFile(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, base string) (watch string)
	}{
		{"a dangling link", func(t *testing.T, base string) string {
			mustSymlink(t, filepath.Join(base, "nowhere"), filepath.Join(base, "sessions"))
			return ""
		}},
		{"a link to a directory elsewhere with entries inside", func(t *testing.T, base string) string {
			external := filepath.Join(t.TempDir(), "external-sessions")
			plantOldSessionDirs(t, external, "oldsess1", "oldsess2", "victim01")
			mustSymlink(t, external, filepath.Join(base, "sessions"))
			return external
		}},
		{"a regular file in place of sessions", func(t *testing.T, base string) string {
			require.NoError(t, os.WriteFile(filepath.Join(base, "sessions"), []byte("not a directory"), 0o600))
			return ""
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			watch := tc.plant(t, base)
			var before map[string]string
			if watch != "" {
				before = snapshotBase(t, watch)
			}
			cfg := legacyCfg(t, base)

			assert.Empty(t, legacyRecipientHint(cfg), "no count, no name, no root: a link is not an old tree")
			assert.Empty(t, legacySessionHint(cfg, "oldsess1"), "an id that exists BEHIND the link is not announced either")
			assert.Empty(t, legacySessionHint(cfg, "victim01"))

			if watch != "" {
				assert.Equal(t, before, snapshotBase(t, watch), "the directory behind the link was not mutated")
			}
		})
	}
}

// R3-M2 + SEC remark: a stale CAB_SESSION_ID goes through the REAL resolver, with
// a Config loaded the way a command loads it, not a hand-built one. The id exists
// only in the old tree, and the error says so.
func TestResolveCurrentSession_AnIdOfTheOldTreeIsExplained(t *testing.T) {
	base := t.TempDir()
	useBase(t, base)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	idle := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, os.MkdirAll(filepath.Join(base, "sessions", "oldsess1"), 0o700))
	require.NoError(t, session.NewManager(base, time.Second).SaveManifest(&session.Manifest{
		SessionID: "oldsess1", SchemaVersion: session.SchemaVersionV2, ProjectName: "old", ProjectPath: "/old",
		AgentName: "ESC-old", Role: session.RoleEsc, PID: os.Getpid(), StartedAt: idle, LastHeartbeat: idle,
		Status: session.StatusActive, Capabilities: []string{"query"},
	}))
	before := snapshotBase(t, base)

	cfg, err := loadConfigOrFail()
	require.NoError(t, err)
	mgr := newSessionManager(cfg)

	t.Setenv("CAB_SESSION_ID", "oldsess1")
	_, err = resolveCurrentSession(cfg, mgr, "next", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist", "the id is not in the current tree")
	assert.Contains(t, err.Error(), "from before the update", "and the error says where it is")
	assert.Contains(t, err.Error(), "cab-bridge join", "and what to do")

	t.Setenv("CAB_SESSION_ID", "neverhere")
	_, err = resolveCurrentSession(cfg, mgr, "next", "")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "from before the update", "an id that is nowhere gets the plain error")

	assert.Equal(t, before, snapshotBase(t, base), "the explanation reads the old tree and writes nothing")
}

// The three "no agent named" errors are three return statements, and each one
// carries the sentence: the case that exercises one of them says nothing about the
// others (a mutation on the third was invisible to a test that only reached the
// second).
func TestResolveRecipient_AnUnknownNameMentionsTheOldTreeOnlyWhenThereIsOne(t *testing.T) {
	base := t.TempDir()
	useBase(t, base)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	cfg, err := loadConfigOrFail()
	require.NoError(t, err)
	mgr := newSessionManager(cfg)
	// Two projects, planted up front so the cases are independent of their order:
	// alone1 is the only agent of /repo/alone; selfsess1 shares /repo/r with a peer.
	plantOverviewSession(t, cfg.DataDir, "alone1sess", session.RoleEsc, "ESC-alone", "/repo/alone", "", "working")
	plantOverviewSession(t, cfg.DataDir, "selfsess1", session.RoleEsc, "ESC-self", "/repo/r", "", "working")
	plantOverviewSession(t, cfg.DataDir, "peersess1", session.RoleVal, "VAL-peer", "/repo/r", "", session.StateOrchestrating)

	branches := []struct {
		name  string
		self  string
		token string
		want  string // a fragment that proves WHICH error this is
	}{
		{"nobody else is registered here", "alone1sess", "VAL-ghost", "no other agent is registered here"},
		{"others are registered here", "selfsess1", "VAL-ghost", "registered here: VAL-peer"},
		{"a qualified address", "selfsess1", "VAL-ghost@/repo/r", "projects with agents"},
	}
	for _, b := range branches {
		_, err = resolveRecipientByName(cfg, mgr, b.token, b.self)
		require.Error(t, err, b.name)
		require.Contains(t, err.Error(), b.want, "%s: this must be the error it claims to be", b.name)
		assert.NotContains(t, err.Error(), "from before the update", "%s: no old tree, no sentence", b.name)
	}

	plantOldSessionDirs(t, filepath.Join(base, "sessions"), "oldsess1", "oldsess2")
	for _, b := range branches {
		_, err = resolveRecipientByName(cfg, mgr, b.token, b.self)
		require.Error(t, err, b.name)
		require.Contains(t, err.Error(), b.want, b.name)
		assert.Contains(t, err.Error(), "2 session(s) of the tree of a cab-bridge from before the update", "%s: the sentence is on this branch too", b.name)
	}
}
