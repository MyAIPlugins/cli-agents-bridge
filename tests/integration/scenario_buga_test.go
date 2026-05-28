package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBUGA_ListenAdoptsPIDEnablesCollision is the Sprint 6 BUG-A end-to-end.
// `register` writes an ephemeral PID that dies the moment it returns, so the
// BUG-6 collision check never fires for a non-listened session. Once a
// long-running `listen` adopts the session (writing its own live PID), a second
// register for the same project must be rejected as a collision.
//
// This bug is invisible to in-process unit tests (register's PID == the live
// test process), which is exactly why the manual smoke test caught it — so the
// regression lives here, driving the real CLI via subprocess.
func TestBUGA_ListenAdoptsPIDEnablesCollision(t *testing.T) {
	// Not parallel: starts and kills a background subprocess.
	dataDir := t.TempDir()
	projDir := t.TempDir()
	bin := buildBinary(t)

	// 1. One-shot register; its PID dies immediately on return.
	out, errOut, exit := run(t, []string{"register", "--project-path=" + projDir, "--json"}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "register must succeed: %s", errOut)
	sid := mustJSONField(t, out, "sessionId")

	// 2. Start a long-running listen that adopts the session.
	listenCmd := exec.Command(bin, "listen", "--session-id="+sid)
	listenCmd.Env = append(os.Environ(), dataDirEnv(dataDir)...)
	require.NoError(t, listenCmd.Start())
	t.Cleanup(func() {
		_ = listenCmd.Process.Kill()
		_, _ = listenCmd.Process.Wait()
	})

	// 3. Wait until listen has written its own (live) PID into the manifest.
	manifestPath := filepath.Join(dataDir, "sessions", sid, "manifest.json")
	listenPID := listenCmd.Process.Pid
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			return false
		}
		var mf struct {
			PID int `json:"pid"`
		}
		if json.Unmarshal(data, &mf) != nil {
			return false
		}
		return mf.PID == listenPID
	}, 5*time.Second, 25*time.Millisecond, "listen must adopt the manifest PID")

	// 4. A second register for the same project must now FAIL (live owner).
	_, errOut2, exit2 := run(t, []string{"register", "--project-path=" + projDir, "--json"}, dataDirEnv(dataDir))
	assert.NotEqual(t, 0, exit2, "register for an actively-listened project must collide")
	assert.Contains(t, errOut2, "already has active session", "collision error must be surfaced")
}
