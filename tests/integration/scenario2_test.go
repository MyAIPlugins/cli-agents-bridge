package integration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScenario2_RoleRoutingEnforcement implements PLAN §7.3 scenario 2:
// 1 VAL + 2 ESC + 1 observer exercising the BUG-3 role policy in cmd path.
//
// Cases verified:
//   - VAL → ESC-A: OK (canonical val↔esc).
//   - ESC-A → ESC-B: blocked by default (esc↔esc forbidden).
//   - ESC-A → ESC-B with --allow-mesh: OK (explicit override).
//   - observer → VAL: blocked structurally (no flag relaxes this).
//
// Each subprocess call exercises the full ask pipeline: load sender +
// target manifest, ValidateSendPair, atomic write to peer inbox.
func TestScenario2_RoleRoutingEnforcement(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()

	register := func(role, name string) string {
		t.Helper()
		proj := t.TempDir()
		out, errOut, exit := run(t, []string{
			"register",
			"--role=" + role,
			"--agent-name=" + name,
			"--project-path=" + proj,
		}, dataDirEnv(dataDir))
		require.Equal(t, 0, exit, "register %s must succeed: %s", name, errOut)
		return mustJSONField(t, out, "sessionId")
	}

	valID := register("val", "VAL-routing")
	escAID := register("esc", "ESC-A")
	escBID := register("esc", "ESC-B")
	obsID := register("observer", "OBS-1")

	// Case 1: VAL → ESC-A (canonical, must succeed)
	_, errOut, exit := run(t, []string{
		"ask", "--to=" + escAID, "--type=query", "--content=hi-esc-a",
		"--session-id=" + valID,
	}, dataDirEnv(dataDir))
	assert.Equal(t, 0, exit, "VAL→ESC-A must succeed; stderr: %s", errOut)

	// Case 2: ESC-A → ESC-B default-blocked (BUG-3 regression)
	_, errOut, exit = run(t, []string{
		"ask", "--to=" + escBID, "--type=query", "--content=secret",
		"--session-id=" + escAID,
	}, dataDirEnv(dataDir))
	assert.NotEqual(t, 0, exit, "ESC→ESC default must fail")
	assert.Contains(t, errOut, "esc",
		"error must mention the offending roles")
	assert.Contains(t, errOut, "--allow-mesh",
		"error must surface override hint")

	// Case 3: ESC-A → ESC-B with --allow-mesh succeeds
	_, errOut, exit = run(t, []string{
		"ask", "--to=" + escBID, "--type=query", "--content=mesh-allowed",
		"--session-id=" + escAID, "--allow-mesh",
	}, dataDirEnv(dataDir))
	assert.Equal(t, 0, exit, "ESC→ESC with --allow-mesh must succeed; stderr: %s", errOut)

	// Case 4: observer → VAL structurally blocked (no flag relaxes)
	_, errOut, exit = run(t, []string{
		"ask", "--to=" + valID, "--type=query", "--content=should-not-send",
		"--session-id=" + obsID,
	}, dataDirEnv(dataDir))
	assert.NotEqual(t, 0, exit, "observer→VAL must fail structurally")
	assert.Contains(t, errOut, "observer", "error must mention observer role")

	// Case 5: observer → VAL with --allow-mesh STILL blocked (mesh does not relax observer)
	_, _, exit = run(t, []string{
		"ask", "--to=" + valID, "--type=query", "--content=should-not-send",
		"--session-id=" + obsID, "--allow-mesh",
	}, dataDirEnv(dataDir))
	assert.NotEqual(t, 0, exit, "observer→any must remain blocked even with --allow-mesh (structural rule)")
}
