package integration

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// backdateManifest rewrites the manifest at path with lastHeartbeat = past.
// Used by scenarios that need to engineer a stale-looking session without
// mocking time in the subprocess.
func backdateManifest(t *testing.T, path string, past time.Time) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var mf map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &mf))
	mf["lastHeartbeat"] = past.UTC().Format(time.RFC3339Nano)
	out, err := json.MarshalIndent(mf, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, out, 0o600))
}

// manifestHeartbeat reads lastHeartbeat from the manifest at path. Used by
// scenarios verifying heartbeat refresh after Touch / heartbeat tick.
func manifestHeartbeat(t *testing.T, path string) time.Time {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var mf struct {
		LastHeartbeat time.Time `json:"lastHeartbeat"`
	}
	require.NoError(t, json.Unmarshal(data, &mf))
	return mf.LastHeartbeat
}
