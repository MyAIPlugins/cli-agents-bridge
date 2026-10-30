package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

const (
	dskVal   = "valdsk01"
	dskEsc   = "escdsk01"
	dskScope = "/repo/dsk"
)

// desktopEnv plants a val and an esc in one scope and points the verb at the
// data dir as the val (CAB_SESSION_ID is how a session is named without a cwd).
// Not parallel: it sets process environment.
func desktopEnv(t *testing.T) (dataDir string) {
	t.Helper()
	dataDir = t.TempDir()
	plantOverviewSession(t, dataDir, dskVal, session.RoleVal, "VAL-dsk", dskScope, "", session.StateOrchestrating)
	plantOverviewSession(t, dataDir, dskEsc, session.RoleEsc, "ESC-dsk", dskScope, "", "working")
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_SESSION_ID", dskVal)
	t.Setenv("CAB_MCP_GRANT_TTL_MINUTES", "")
	return dataDir
}

func runDesktopCmd(t *testing.T, args ...string) (out, errOut string, err error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err = desktopRun(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func grantFile(dataDir, sid string) string {
	return filepath.Join(dataDir, "sessions", sid, "desktop-grant.json")
}

func TestDesktop_OpenWritesAGrantForTheConfiguredTTL(t *testing.T) {
	dataDir := desktopEnv(t)
	t.Setenv("CAB_MCP_GRANT_TTL_MINUTES", "7")

	out, _, err := runDesktopCmd(t, "open")
	require.NoError(t, err)
	assert.Contains(t, out, "OPEN")
	assert.Contains(t, out, dskScope)
	assert.Contains(t, out, "neither closes this nor renews it", "the D8 contract is said where the val reads it")

	mgr := newSessionManager(nextTestConfig(dataDir))
	g, found, rerr := mgr.ReadDesktopGrant(dskVal)
	require.NoError(t, rerr)
	require.True(t, found)
	assert.WithinDuration(t, time.Now().UTC().Add(7*time.Minute), g.ExpiresAt, time.Minute)
	assert.True(t, g.ValidAt(time.Now().UTC()))
}

func TestDesktop_OnlyAValOpensIt(t *testing.T) {
	dataDir := desktopEnv(t)
	t.Setenv("CAB_SESSION_ID", dskEsc)

	for _, verb := range []string{"open", "close", "status"} {
		_, _, err := runDesktopCmd(t, verb)
		require.Error(t, err, verb)
		assert.Contains(t, err.Error(), "only a val", verb)
	}
	assert.NoFileExists(t, grantFile(dataDir, dskEsc), "an esc never gets a grant")
	assert.NoFileExists(t, grantFile(dataDir, dskVal), "and nothing leaks onto the val")
}

func TestDesktop_RefusesANonPositiveTTLAndWritesNothing(t *testing.T) {
	dataDir := desktopEnv(t)
	// The environment is applied as given by envInt, so 0 and a negative number
	// reach the verb: an open that never expires is refused, not defaulted.
	for _, ttl := range []string{"-3", "0"} {
		t.Setenv("CAB_MCP_GRANT_TTL_MINUTES", ttl)
		_, _, err := runDesktopCmd(t, "open")
		require.Error(t, err, "ttl %s", ttl)
		assert.Contains(t, err.Error(), "must be positive")
		assert.NoFileExists(t, grantFile(dataDir, dskVal))
	}
}

func TestDesktop_CloseRemovesAndIsIdempotent(t *testing.T) {
	dataDir := desktopEnv(t)
	_, _, err := runDesktopCmd(t, "open")
	require.NoError(t, err)
	require.FileExists(t, grantFile(dataDir, dskVal))

	out, _, err := runDesktopCmd(t, "close")
	require.NoError(t, err)
	assert.Contains(t, out, "CLOSED")
	assert.NoFileExists(t, grantFile(dataDir, dskVal))

	out, _, err = runDesktopCmd(t, "close")
	require.NoError(t, err)
	assert.Contains(t, out, "was not open", "closing twice says what happened instead of pretending")
}

func TestDesktop_StatusTellsOpenExpiredClosedAndUnreadable(t *testing.T) {
	dataDir := desktopEnv(t)

	out, _, err := runDesktopCmd(t, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "CLOSED")

	_, _, err = runDesktopCmd(t, "open")
	require.NoError(t, err)
	out, _, err = runDesktopCmd(t, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "OPEN until")

	// Expired: written by hand, an hour in the past.
	require.NoError(t, os.WriteFile(grantFile(dataDir, dskVal),
		[]byte(`{"schemaVersion":1,"openedAt":"2020-01-01T00:00:00Z","expiresAt":"2020-01-01T08:00:00Z"}`), 0o600))
	out, _, err = runDesktopCmd(t, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "EXPIRED")

	require.NoError(t, os.WriteFile(grantFile(dataDir, dskVal), []byte("{not json"), 0o600))
	out, _, err = runDesktopCmd(t, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "UNREADABLE, counted as CLOSED", "a damaged file is shown, never read as open")
}

func TestDesktop_StatusListsTheArchitectSessionsOfThisScopeOnly(t *testing.T) {
	dataDir := desktopEnv(t)
	plantOverviewSession(t, dataDir, "arcdsk01", session.RoleArchitect, "ARC-dsk-cowork", dskScope, "", session.StateIdle)
	plantOverviewSession(t, dataDir, "arcdsk02", session.RoleArchitect, "ARC-other-cowork", "/repo/elsewhere", "", session.StateIdle)

	out, _, err := runDesktopCmd(t, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "ARC-dsk-cowork")
	assert.NotContains(t, out, "ARC-other-cowork", "another repository's Desktop session is not this val's business")
	assert.NotContains(t, out, "ESC-dsk", "and status lists Desktop sessions, not executors")
}

func TestDesktop_ArgumentsAreValidated(t *testing.T) {
	desktopEnv(t)
	for _, args := range [][]string{nil, {"open", "close"}, {"reopen"}, {"--ttl=5"}} {
		_, _, err := runDesktopCmd(t, args...)
		require.Error(t, err, "%v", args)
		assert.Contains(t, err.Error(), "usage:", "%v", args)
	}
	out, errOut, err := runDesktopCmd(t, "--help")
	require.NoError(t, err)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "usage:")
}
