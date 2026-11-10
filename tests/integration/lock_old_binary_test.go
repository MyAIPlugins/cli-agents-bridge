//go:build !windows

package integration

// F-160, transition (a): an old binary and the new one on the same data directory
// never touch the same file. The new one keeps its state in <base>/layout-2 and does
// not read or write <base>/sessions or <base>/archive, so an old `next` that is still
// running keeps running, is not retired by anything the new binary does, and is not
// handed mail by it. The two worlds do not see each other: that is the design, and
// the CHANGELOG tells people to drain the mail and stop the old consumers before they
// update because of it.
//
// OPT-IN. It needs a binary built from the last commit before layout-2:
//
//	git worktree add <dir> c0649ad && (cd <dir> && go build -o <path> ./cmd/cab-bridge)
//	CAB_OLD_BINARY=<path> go test -run TestOldAndNewBinariesNeverShareAFile ./tests/integration/
//
// Without the variable the test is skipped, and says so. The old binary is run on a
// sandbox data directory only (CAB_DATA_DIR), never on the real one. Windows is not
// covered: an old binary was never built there for this check.

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
)

const (
	oldWorldMail  = "ping from the old world"
	oldWorldAsk   = "question from the old world"
	oldWorldReply = "answer from the old world"
	newWorldMail  = "mail from the new world"
)

// runBinary runs bin to completion, like run does for this tree's binary.
func runBinary(t *testing.T, bin string, args, env []string) (stdout, stderr string, exit int) {
	t.Helper()
	c := startBinary(t, bin, env, args...)
	c.wait(t)
	return c.stdout.String(), c.stderr.String(), c.exit
}

// oldTreeSnapshot records what the new binary must leave alone, in the shape that
// does not change by itself while an old `next` is alive: which sessions and
// archives exist, the bytes of every message file (inbox, processed, outbox), and
// whether a lock file has appeared. The manifest and the listener record of a live
// old session are rewritten by the old process itself, so only their presence counts.
func oldTreeSnapshot(t *testing.T, base string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	for _, root := range []string{"sessions", "archive"} {
		top := filepath.Join(base, root)
		err := filepath.WalkDir(top, func(p string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			rel, rerr := filepath.Rel(base, p)
			require.NoError(t, rerr)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				snap[rel+"/"] = "dir"
				return nil
			}
			dir := filepath.Base(filepath.Dir(p))
			if dir == "inbox" || dir == "processed" || dir == "outbox" || strings.HasPrefix(rel, "archive/") {
				data, rerr := os.ReadFile(p)
				require.NoError(t, rerr)
				sum := sha256.Sum256(data)
				snap[rel] = hex.EncodeToString(sum[:])
				return nil
			}
			snap[rel] = "file"
			return nil
		})
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
	}
	return snap
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func TestOldAndNewBinariesNeverShareAFile(t *testing.T) {
	oldBin := os.Getenv("CAB_OLD_BINARY")
	if oldBin == "" {
		t.Skip("opt-in: set CAB_OLD_BINARY to a cab-bridge built from c0649ad (the last commit before layout-2); see the header of this file")
	}
	oldBin, err := filepath.Abs(oldBin)
	require.NoError(t, err)
	require.FileExists(t, oldBin, "CAB_OLD_BINARY does not point at a file")
	newBin := buildBinary(t)

	base := t.TempDir()
	project := t.TempDir()
	oldEnv := dataDirEnv(base, "CAB_AUTO_GC_HOURS=0")
	// Auto-gc ON for the new binary: it runs on every command and sweeps orphans, so
	// it is the path most likely to reach into a tree it should not know about.
	newEnv := dataDirEnv(base, "CAB_AUTO_GC_HOURS=1")

	register := func(bin string, env []string, role, name string) string {
		out, errOut, exit := runBinary(t, bin, []string{"register", "--role=" + role, "--agent-name=" + name, "--project-path=" + project}, env)
		require.Equal(t, 0, exit, "register %s: %s", name, errOut)
		return mustJSONField(t, out, "sessionId")
	}

	escOld := register(oldBin, oldEnv, "esc", "ESC-old")
	valOld := register(oldBin, oldEnv, "val", "VAL-old")
	critOld := register(oldBin, oldEnv, "critic", "CRI-old")
	require.FileExists(t, filepath.Join(base, "sessions", escOld, "manifest.json"),
		"premise: CAB_OLD_BINARY writes <base>/sessions, i.e. it is a binary from before layout-2")
	require.NoDirExists(t, filepath.Join(base, config.LayoutDir), "premise: nothing of the new world exists yet")

	// The old VAL is an orphan as the old world sees it (its register process is
	// gone, its heartbeat two days old): exactly what a sweep would remove, if it
	// could see it.
	backdateManifest(t, filepath.Join(base, "sessions", valOld, "manifest.json"), time.Now().Add(-48*time.Hour))

	// Two live old consumers, each waiting for mail in its own process.
	startOldNext := func(id string) *liveCmd {
		c := startBinary(t, oldBin, append(oldEnv, "CAB_SESSION_ID="+id), "next")
		listener := filepath.Join(base, "sessions", id, "listener.json")
		require.Eventually(t, func() bool { _, serr := os.Stat(listener); return serr == nil }, 10*time.Second, 50*time.Millisecond,
			"premise: the old next of %s claimed its session; stderr: %s", id, c.stderr.String())
		require.True(t, c.running(), "premise: the old next of %s is waiting for mail", id)
		return c
	}
	oldNext := startOldNext(escOld)
	critNext := startOldNext(critOld)
	before := oldTreeSnapshot(t, base)

	// ---- the new world acts: register, list, try to reach the old agent, sweep.
	valNew := register(newBin, newEnv, "val", "VAL-new")
	newSession := append(append([]string{}, newEnv...), "CAB_SESSION_ID="+valNew)

	out, errOut, exit := run(t, []string{"peers", "--all-scopes", "--json"}, newSession)
	require.Equal(t, 0, exit, "peers: %s", errOut)
	assert.Contains(t, out, "VAL-new")
	assert.NotContains(t, out, "ESC-old", "the new binary does not list the agents of the old tree")
	assert.NotContains(t, out, "VAL-old")

	_, errOut, exit = run(t, []string{"tell", "ESC-old", newWorldMail}, newSession)
	assert.NotEqual(t, 0, exit, "the new binary cannot address an agent that lives in the old tree")
	assert.Contains(t, errOut, "ESC-old", "and the error names who it could not find")

	stateDir := filepath.Join(base, config.LayoutDir)
	_, errOut, exit = run(t, []string{"cleanup", "--scope=global", "--all-scopes", "--force", "--data-dir=" + stateDir}, newSession)
	require.Equal(t, 0, exit, "cleanup on the new tree: %s", errOut)

	// ---- the old world is exactly as it was.
	assert.Equal(t, before, oldTreeSnapshot(t, base), "nothing in <base>/sessions or <base>/archive changed")
	assert.True(t, oldNext.running(), "the old next is still running: nothing retired it")
	assert.True(t, critNext.running(), "and so is the second one")
	assert.DirExists(t, filepath.Join(base, "sessions", valOld), "the old orphan was not swept by the new binary")
	assert.Empty(t, jsonFiles(t, filepath.Join(base, "sessions", escOld, "inbox")), "and no mail was delivered to the old agent")
	for _, id := range []string{escOld, valOld} {
		assert.NoFileExists(t, filepath.Join(base, "sessions", id, "lock"), "the new binary left no lock file in the old tree")
	}
	assert.Equal(t, []string{valNew}, dirEntries(t, filepath.Join(stateDir, "sessions")), "the new tree holds only the new session")

	// ---- the old consumer is alive and still gets the old world's mail.
	_, errOut, exit = runBinary(t, oldBin, []string{"tell", "ESC-old", oldWorldMail}, append(append([]string{}, oldEnv...), "CAB_SESSION_ID="+valOld))
	require.Equal(t, 0, exit, "the old tell: %s", errOut)
	oldNext.wait(t)
	require.Equal(t, 0, oldNext.exit, "stderr: %s", oldNext.stderr.String())
	assert.Contains(t, oldNext.stdout.String(), oldWorldMail, "the old next received the old world's mail")
	assert.NotContains(t, oldNext.stdout.String(), newWorldMail)

	// ... and a whole round trip of the old world, ask -> next -> reply, between the
	// other two old agents, works after the new binary has run.
	valEnv := append(append([]string{}, oldEnv...), "CAB_SESSION_ID="+valOld)
	critEnv := append(append([]string{}, oldEnv...), "CAB_SESSION_ID="+critOld)
	_, errOut, exit = runBinary(t, oldBin, []string{"ask", "CRI-old", oldWorldAsk}, valEnv)
	require.Equal(t, 0, exit, "the old ask: %s", errOut)
	critNext.wait(t)
	require.Equal(t, 0, critNext.exit, "stderr: %s", critNext.stderr.String())
	assert.Contains(t, critNext.stdout.String(), oldWorldAsk, "the second old next received the ask")
	_, errOut, exit = runBinary(t, oldBin, []string{"reply", oldWorldReply}, critEnv)
	require.Equal(t, 0, exit, "the old reply: %s", errOut)
	answers := jsonFiles(t, filepath.Join(base, "sessions", valOld, "inbox"))
	require.Len(t, answers, 1, "exactly one answer reached the old asker")
	answer, err := os.ReadFile(filepath.Join(base, "sessions", valOld, "inbox", answers[0]))
	require.NoError(t, err)
	assert.Contains(t, string(answer), oldWorldReply, "and it is the one that was sent")

	// ---- and the other direction: the old binary does not see the new session.
	out, errOut, exit = runBinary(t, oldBin, []string{"peers", "--all-scopes", "--json"}, oldEnv)
	require.Equal(t, 0, exit, "old peers: %s", errOut)
	assert.NotContains(t, out, "VAL-new", "the old binary does not list the agents of the new tree")
}
