package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/cleanup"
	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// F-160, criterion 8: a binary of this generation never touches the tree of the
// ones before it. These tests build that old tree the way an old binary leaves it
// (<base>/sessions, <base>/archive), run the commands that write the most, and
// compare the old tree byte for byte, mode and mtime included.
//
// What the snapshot proves is that nothing in the old tree was WRITTEN, renamed
// or removed. It does not prove nothing was READ: that half is closed by two
// structural tests in layout_structure_test.go, which fix the set of files that can
// name the old tree's children (TestStructure_OnlyLegacyTreeGoNamesTheOldChildren)
// and the os calls legacy_tree.go can make
// (TestStructure_TheOldTreeIsReadOnlyByLegacyTreeGo).

var frozenAt = time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC)

// seedOldTree leaves under base what a pre-F-160 binary would have: two stale
// orphans (dead pid, 48 hours idle) that any auto-gc eligible to see them would
// remove, one with unread mail and a PID-file lock, and an archive day far older
// than the retention window, which a retention purge would remove. Everything is
// then frozen to a fixed mtime so that a write — even a no-op rewrite — shows.
func seedOldTree(t *testing.T, base, scope string) {
	t.Helper()
	old := session.NewManager(base, time.Second)
	idle := time.Now().UTC().Add(-48 * time.Hour)
	for id, sc := range map[string]string{"oldsess1": "", "oldsess2": scope} {
		require.NoError(t, os.MkdirAll(filepath.Join(base, "sessions", id, "inbox"), 0o700))
		require.NoError(t, old.SaveManifest(&session.Manifest{
			SessionID: id, SchemaVersion: session.SchemaVersionV2, ProjectName: "old", ProjectPath: "/old/" + id,
			AgentName: "ESC-" + id, Role: session.RoleEsc, PID: 999999, StartedAt: idle, LastHeartbeat: idle,
			Status: session.StatusActive, Capabilities: []string{"query"}, Scope: sc,
		}))
	}
	oldSess := filepath.Join(base, "sessions", "oldsess1")
	require.NoError(t, os.WriteFile(filepath.Join(oldSess, "inbox", "msg-aaaaaaaaaaaa.json"), []byte(`{"unread":"mail for the old binary"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(oldSess, "lock"), []byte("12345\n"), 0o600))
	archived := filepath.Join(base, "archive", "2020-01-01", "oldsess9")
	require.NoError(t, os.MkdirAll(archived, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(archived, "manifest.json"), []byte(`{"sessionId":"oldsess9"}`), 0o600))
	// A config.json both generations read. A valid one: an invalid file would stop
	// every command before it did anything, and the test would prove nothing.
	require.NoError(t, os.WriteFile(filepath.Join(base, "config.json"), []byte(`{"stale_seconds": 7200}`), 0o600))

	var paths []string
	require.NoError(t, filepath.WalkDir(base, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != base {
			paths = append(paths, p)
		}
		return nil
	}))
	for i := len(paths) - 1; i >= 0; i-- { // children before parents: a Chtimes on a file must not re-touch its dir
		require.NoError(t, os.Chtimes(paths[i], frozenAt, frozenAt))
	}
}

// snapshotBase describes everything under base EXCEPT the new state dir: kind,
// permissions, size, mtime and a content hash (or link target). config.json is IN: it
// is read by both generations and written by neither, and a binary that rewrote it on
// every command (even with the same bytes) must move the mtime and fail the test (CRI
// diff-gate, M3). The base directory itself is described only by its child names,
// because its mtime legitimately changes when layout-2 appears.
func snapshotBase(t *testing.T, base string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	require.NoError(t, filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(base, p)
		require.NoError(t, rerr)
		if rel == config.LayoutDir {
			return filepath.SkipDir
		}
		if rel == "." {
			var names []string
			entries, derr := os.ReadDir(base)
			require.NoError(t, derr)
			for _, e := range entries {
				if e.Name() != config.LayoutDir {
					names = append(names, e.Name())
				}
			}
			sort.Strings(names)
			snap["."] = "children:" + strings.Join(names, ",")
			return nil
		}
		info, ierr := os.Lstat(p)
		require.NoError(t, ierr)
		desc := info.Mode().String() + "|" + strconv.FormatInt(info.Size(), 10) + "|" + strconv.FormatInt(info.ModTime().UnixNano(), 10)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, lerr := os.Readlink(p)
			require.NoError(t, lerr)
			desc += "|->" + target
		case info.Mode().IsRegular():
			raw, rerr := os.ReadFile(p)
			require.NoError(t, rerr)
			sum := sha256.Sum256(raw)
			desc += "|" + hex.EncodeToString(sum[:])
		}
		snap[rel] = desc
		return nil
	}))
	return snap
}

// joinAndExchange runs the commands that write the most, against the base the
// environment points at: two joins' worth of registration (each triggers the
// auto-gc sweep and the A-posteriori checks), a message between peers of one scope,
// an inbox listing, whoami, and the destructive one, a my-session cleanup.
// It returns the id of the joined session and the scope it landed in.
func joinAndExchange(t *testing.T, proj string) (valID, scope string) {
	t.Helper()
	captureStdout(t, func() {
		require.NoError(t, runJoin([]string{"--role=val", "--agent-name=VAL-iso", "--project-path=" + proj}))
	})
	cfg, err := loadConfigOrFail()
	require.NoError(t, err)
	mgr := newSessionManager(cfg)
	entries, err := os.ReadDir(filepath.Join(cfg.DataDir, "sessions"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "the join created one session, in the NEW tree")
	valID = entries[0].Name()
	mf, err := mgr.LoadManifest(valID)
	require.NoError(t, err)
	scope = session.EffectiveScope(mf)

	plantOverviewSession(t, cfg.DataDir, "newesc001", session.RoleEsc, "ESC-iso", scope, "", "working")
	_, err = sendMessage(cfg, mgr, valID, "newesc001", message.TypeNotify, "hello from the new tree", nil, false)
	require.NoError(t, err)

	captureStdout(t, func() {
		require.NoError(t, runInbox([]string{"--session-id=newesc001", "--list", "--json"}))
		require.NoError(t, runWhoami([]string{"--session-id=" + valID}))
	})
	captureStdoutStderr(t, func() {
		require.NoError(t, runCleanup([]string{"--scope=my-session", "--session-id=newesc001"}))
	})
	return valID, scope
}

func TestLayout_NewBinaryNeverTouchesTheOldTree(t *testing.T) {
	proj := t.TempDir()
	scope := resolveScope(proj)
	base := t.TempDir()
	seedOldTree(t, base, scope)
	before := snapshotBase(t, base)
	require.NotEmpty(t, before)

	// POSITIVE CONTROL 1: the fixture is what it claims. The same seed, in a base
	// that IS the state dir of an older generation, is swept by the auto-gc and the
	// retention purge — so an old tree left untouched below was not safe by
	// construction of the fixture, it was left alone.
	control := t.TempDir()
	seedOldTree(t, control, scope)
	removed, err := cleanup.GCOrphans(control, scope, 24, nil)
	require.NoError(t, err)
	assert.Len(t, removed, 2, "both stale old sessions are eligible for the sweep a generation-1 binary would run")

	t.Setenv("CAB_DATA_DIR", base)
	valID, _ := joinAndExchange(t, proj)
	require.NotEmpty(t, valID)

	after := snapshotBase(t, base)
	assert.Equal(t, before, after, "the old tree is byte for byte what it was (contents, modes, mtimes, child names)")

	// POSITIVE CONTROL 2: the snapshot can see a write. One rewritten byte, with the
	// content unchanged in length, moves the mtime and fails the comparison.
	require.NoError(t, os.WriteFile(filepath.Join(base, "sessions", "oldsess1", "lock"), []byte("12345\n"), 0o600))
	assert.NotEqual(t, before, snapshotBase(t, base), "a rewrite of one file in the old tree is visible to the snapshot")

	// POSITIVE CONTROL 3: the same for config.json, rewritten with the SAME bytes.
	reset := snapshotBase(t, base)
	cfgPath := filepath.Join(base, "config.json")
	same, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfgPath, same, 0o600))
	assert.NotEqual(t, reset, snapshotBase(t, base), "a rewrite of config.json, bytes unchanged, is visible to the snapshot")
}

func TestLayout_OldTreeWorksAfterTheNewBinaryRan(t *testing.T) {
	proj := t.TempDir()
	base := t.TempDir()
	seedOldTree(t, base, resolveScope(proj))
	t.Setenv("CAB_DATA_DIR", base)
	joinAndExchange(t, proj)

	// What a binary of the previous generation does next: open its own root and
	// load its own manifests, with the manager it has always used.
	old := session.NewManager(base, time.Second)
	mf, err := old.LoadManifest("oldsess1")
	require.NoError(t, err, "the old tree still loads with the old manager")
	assert.Equal(t, "ESC-oldsess1", mf.AgentName)
	assert.Equal(t, 999999, mf.PID)
	raw, err := os.ReadFile(filepath.Join(base, "sessions", "oldsess1", "inbox", "msg-aaaaaaaaaaaa.json"))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "mail for the old binary", "the unread mail is where it was")
}

func TestLayout_StateDirIsUnderTheBase(t *testing.T) {
	base := t.TempDir()
	t.Setenv("CAB_DATA_DIR", base)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	valID, _ := joinAndExchange(t, t.TempDir())

	// Sessions and the archive of THIS generation live under layout-2 ...
	assert.DirExists(t, filepath.Join(base, config.LayoutDir, "sessions", valID))
	// ... and nothing of it appeared where the old binaries look.
	assert.NoDirExists(t, filepath.Join(base, "sessions"))
	assert.NoDirExists(t, filepath.Join(base, "archive"))

	cfg, err := loadConfigOrFail()
	require.NoError(t, err)
	assert.Equal(t, base, cfg.BaseDir)
	assert.Equal(t, filepath.Join(base, config.LayoutDir), cfg.DataDir)
	assert.NotEqual(t, cfg.BaseDir, cfg.DataDir, "base and state are two directories")
}
