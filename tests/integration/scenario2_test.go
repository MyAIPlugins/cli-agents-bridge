package integration

import (
	"testing"

	"github.com/stretchr/testify/assert"
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

	// One scope, one working directory per agent: the v0.8 verbs resolve the
	// sender from the cwd and the recipient by agent name.
	dirs := sharedScopeDirs(t, "val", "esca", "escb", "obs")
	valDir, escADir, escBDir, obsDir := dirs[0], dirs[1], dirs[2], dirs[3]

	registerIn(t, dataDir, valDir, "val", "VAL-routing")
	registerIn(t, dataDir, escADir, "esc", "ESC-A")
	registerIn(t, dataDir, escBDir, "esc", "ESC-B")
	registerIn(t, dataDir, obsDir, "observer", "OBS-1")

	// Case 1: VAL → ESC-A (canonical, must succeed)
	_, errOut, exit := runInDir(t, valDir, []string{"ask", "ESC-A", "hi-esc-a"}, dataDirEnv(dataDir))
	assert.Equal(t, 0, exit, "VAL→ESC-A must succeed; stderr: %s", errOut)

	// Case 2: ESC-A → ESC-B blocked (BUG-3 regression).
	//
	// NOTE: the v0.8 verbs carry no flags, so --allow-mesh is no longer
	// reachable from the CLI and the old "override" case cannot be expressed
	// here. esc→esc is therefore enforced unconditionally on this path; the
	// override still exists in sendMessage for callers that pass it.
	_, errOut, exit = runInDir(t, escADir, []string{"ask", "ESC-B", "secret"}, dataDirEnv(dataDir))
	assert.NotEqual(t, 0, exit, "ESC→ESC must fail")
	assert.Contains(t, errOut, "esc", "error must mention the offending roles")

	// Case 3: observer → VAL structurally blocked (no flag relaxes it)
	_, errOut, exit = runInDir(t, obsDir, []string{"ask", "VAL-routing", "should-not-send"}, dataDirEnv(dataDir))
	assert.NotEqual(t, 0, exit, "observer→VAL must fail structurally")
	assert.Contains(t, errOut, "observer", "error must mention observer role")
}
