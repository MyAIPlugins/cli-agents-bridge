package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// OPEN and EXPIRED are decided by the manager's clock, the one the MCP side judges
// the grant by. The grant lives in 2001 so the wall clock cannot give the same answer
// by accident: at the injected instant it is open, at the wall clock it is long gone.
func TestDesktop_StatusJudgesTheGrantByTheManagersClock(t *testing.T) {
	dataDir := desktopEnv(t)
	mgr := newSessionManager(nextTestConfig(dataDir))
	t0 := time.Date(2001, 5, 6, 8, 0, 0, 0, time.UTC)
	mgr.Now = func() time.Time { return t0 }
	_, err := mgr.OpenDesktopGrant(dskVal, 8*time.Hour)
	require.NoError(t, err)
	mf, err := mgr.LoadManifest(dskVal)
	require.NoError(t, err)

	status := func(at time.Time) string {
		mgr.Now = func() time.Time { return at }
		var out bytes.Buffer
		require.NoError(t, desktopStatus(mgr, 300, 65536, dataDir, dskVal, mf, &out))
		return out.String()
	}
	assert.Contains(t, status(t0.Add(time.Hour)), "OPEN until", "an hour into an 8-hour grant")
	assert.Contains(t, status(t0.Add(8*time.Hour)), "EXPIRED", "the expiry instant itself is already closed")
	assert.Contains(t, status(t0.Add(9*time.Hour)), "EXPIRED")
}

// What `close` tells the val is true of THIS val's grant: another val of the same
// project reopening its own bridge does not make this val's messages visible again.
func TestDesktop_CloseSaysWhoseReopeningBringsTheMessagesBack(t *testing.T) {
	desktopEnv(t)
	_, _, err := runDesktopCmd(t, "open")
	require.NoError(t, err)
	out, _, err := runDesktopCmd(t, "close")
	require.NoError(t, err)
	assert.Contains(t, out, "unless this val reopens the bridge")
	assert.NotContains(t, out, "unless a val")
}
