package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// F-171, criterion 5 of the BAR: the sweep that `join` and `register` start with is the one
// nobody asked for, and a retirement that stops because it could not archive a message must
// neither block them nor go unseen. The orphan that cannot be archived is made to fail with a
// directory inside its inbox/, which needs no permission and so fails the same way on every
// system. It is named orphan01 so that it comes first in the sweep, and the healthy one after
// it shows that the sweep goes on past the failure.

// plantUnarchivable gives a planted session an unread message and a directory inside inbox/
// that the archive step will not move. It returns the two paths.
func plantUnarchivable(t *testing.T, dataDir, id string) (msg, nested string) {
	t.Helper()
	inbox := filepath.Join(dataDir, "sessions", id, "inbox")
	msg = filepath.Join(inbox, "msg-aaaaaaaaaaaa.json")
	nested = filepath.Join(inbox, "unexpected-dir", "x.json")
	for _, p := range []string{msg, nested} {
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, []byte("unread"), 0o600))
	}
	return msg, nested
}

func requireStillThere(t *testing.T, dataDir, id, msg, nested string) {
	t.Helper()
	assert.True(t, sessionExists(dataDir, id), "the session that could not be archived stays")
	assert.FileExists(t, filepath.Join(dataDir, "sessions", id, "manifest.json"), "with its manifest, so the next pass collects it")
	assert.FileExists(t, msg, "and its unread message")
	assert.FileExists(t, nested)
}

func TestRunAutoGC_AnOrphanThatCannotBeArchivedStaysAndIsNamedAndTheSweepGoesOn(t *testing.T) {
	dataDir := t.TempDir()
	old := time.Now().UTC().Add(-48 * time.Hour)
	plantSession(t, dataDir, "orphan01", deadPID, old)
	plantSession(t, dataDir, "orphan02", deadPID, old)
	msg, nested := plantUnarchivable(t, dataDir, "orphan01")

	var buf bytes.Buffer
	removed := runAutoGC(config.Config{DataDir: dataDir, AutoGCHours: 24}, "", &buf)

	requireStillThere(t, dataDir, "orphan01", msg, nested)
	require.Len(t, removed, 1)
	assert.Equal(t, "orphan02", removed[0].SessionID, "the sweep went on past the failure")
	log := buf.String()
	assert.Contains(t, log, "auto-gc finished with errors (non-fatal)")
	assert.Contains(t, log, "orphan01", "the session is named")
	assert.Contains(t, log, "unexpected-dir", "and so is what stopped it")
	assert.Contains(t, log, "auto-gc removed orphan session orphan02")
}

func TestRunRegister_AnOrphanThatCannotBeArchivedDoesNotBlockTheRegistration(t *testing.T) {
	dataDir := useBase(t, t.TempDir())
	t.Setenv("CAB_AUTO_GC_HOURS", "24")
	old := time.Now().UTC().Add(-48 * time.Hour)
	plantSession(t, dataDir, "orphan01", deadPID, old)
	plantSession(t, dataDir, "orphan02", deadPID, old)
	msg, nested := plantUnarchivable(t, dataDir, "orphan01")

	var runErr error
	out, errOut := captureStdoutStderr(t, func() {
		runErr = runRegister([]string{"--role=esc", "--project-path=" + t.TempDir(), "--json=false"})
	})

	require.NoError(t, runErr, "a retirement that stops must not stop the register that started it")
	assert.True(t, sessionExists(dataDir, firstLine(out)), "the new session exists")
	requireStillThere(t, dataDir, "orphan01", msg, nested)
	assert.False(t, sessionExists(dataDir, "orphan02"), "the healthy orphan was collected")
	assert.Contains(t, errOut, "auto-gc finished with errors (non-fatal)")
	assert.Contains(t, errOut, "unexpected-dir")
}

func TestJoin_AnAbandonedSessionThatCannotBeArchivedDoesNotBlockTheJoin(t *testing.T) {
	dataDir := useBase(t, t.TempDir())
	proj := t.TempDir()
	t.Setenv("CAB_AUTO_GC_HOURS", "0") // learn the scope the join derives, with the sweep off
	args := []string{"--role=esc", "--agent-name=ESC-arriving", "--project-path=" + proj}
	captureStdout(t, func() { require.NoError(t, runJoin(args)) })
	entries, err := os.ReadDir(filepath.Join(dataDir, "sessions"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	mf, err := newSessionManager(nextTestConfig(t, dataDir)).LoadManifest(entries[0].Name())
	require.NoError(t, err)
	scope := session.EffectiveScope(mf)
	require.NotEmpty(t, scope)
	plantAbandoned(t, dataDir, "escgone01", session.RoleEsc, "ESC-gone-1", scope)
	plantAbandoned(t, dataDir, "escgone02", session.RoleEsc, "ESC-gone-2", scope)
	msg, nested := plantUnarchivable(t, dataDir, "escgone01")

	t.Setenv("CAB_AUTO_GC_HOURS", "24")
	var joinErr error
	_, errOut := captureStdoutStderr(t, func() { joinErr = runJoin(args) })

	require.NoError(t, joinErr, "a retirement that stops must not stop the join that started it")
	requireStillThere(t, dataDir, "escgone01", msg, nested)
	assert.False(t, sessionExists(dataDir, "escgone02"), "the abandoned session next to it is still collected")
	assert.Contains(t, errOut, "auto-gc finished with errors (non-fatal)")
	assert.Contains(t, errOut, "escgone01")
}
