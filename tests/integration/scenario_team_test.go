package integration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// registerTeam registers an ESC with an explicit --team and returns its id.
func registerTeam(t *testing.T, dataDir, agent, team, projectPath string) string {
	t.Helper()
	args := []string{"register", "--role=esc", "--agent-name=" + agent, "--project-path=" + projectPath}
	if team != "" {
		args = append(args, "--team="+team)
	}
	out, errOut, exit := run(t, args, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "register %s: %s", agent, errOut)
	return mustJSONField(t, out, "sessionId")
}

// TestScenarioTeam_PeersFilterIsolatesTeam is the CORE F-5 regression: three
// sessions share one data dir — team alpha, team beta, and no team. `peers
// --team=alpha` must return ONLY the alpha session; the beta and team-less
// sessions are excluded. This is the isolation that stops two VAL/ESC pairs
// from seeing each other's sessions in a shared data dir.
func TestScenarioTeam_PeersFilterIsolatesTeam(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	alphaID := registerTeam(t, dataDir, "ESC-alpha", "alpha", t.TempDir())
	betaID := registerTeam(t, dataDir, "ESC-beta", "beta", t.TempDir())
	noTeamID := registerTeam(t, dataDir, "ESC-none", "", t.TempDir())

	out, errOut, exit := run(t, []string{"peers", "--team=alpha", "--json"}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "peers --team=alpha: %s", errOut)

	assert.Contains(t, out, alphaID, "alpha session must appear under --team=alpha")
	assert.NotContains(t, out, betaID, "beta session must be excluded by --team=alpha")
	assert.NotContains(t, out, noTeamID, "team-less session must be excluded by any --team filter")

	// Default peers (no --team) is the unchanged global view: all three present.
	out, errOut, exit = run(t, []string{"peers", "--json"}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "peers (global): %s", errOut)
	assert.Contains(t, out, alphaID)
	assert.Contains(t, out, betaID)
	assert.Contains(t, out, noTeamID)
}

// TestScenarioTeam_Whoami shows full identity: full projectPath (not the
// basename, resolving F-6), the team, and the dataDir (the direct diagnostic
// for "registered in the wrong data dir").
func TestScenarioTeam_Whoami(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	projectPath := t.TempDir()

	id := registerTeam(t, dataDir, "ESC-wai", "alpha", projectPath)

	out, errOut, exit := run(t, []string{"whoami", "--session-id=" + id, "--json"}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "whoami --json: %s", errOut)

	assert.Equal(t, id, mustJSONField(t, out, "sessionId"))
	assert.Equal(t, "alpha", mustJSONField(t, out, "teamId"))
	assert.Equal(t, projectPath, mustJSONField(t, out, "projectPath"), "whoami must show the FULL projectPath (F-6)")
	assert.Equal(t, dataDir, mustJSONField(t, out, "dataDir"), "whoami must show the current dataDir (F-5 diagnostic)")

	// Human output must render team and dataDir too.
	out, errOut, exit = run(t, []string{"whoami", "--session-id=" + id}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "whoami human: %s", errOut)
	assert.Contains(t, out, "alpha")
	assert.Contains(t, out, dataDir)
	assert.Contains(t, out, projectPath)
}

// TestScenarioTeam_InvalidTeamRejected: a malformed --team is rejected at
// register time with exit 1 (validation failure), never written to a manifest.
func TestScenarioTeam_InvalidTeamRejected(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	_, errOut, exit := run(t, []string{"register", "--role=esc", "--agent-name=ESC-bad", "--team=Bad Name", "--project-path=" + t.TempDir()},
		dataDirEnv(dataDir))
	assert.Equal(t, 1, exit, "malformed --team must exit 1; stderr: %s", errOut)
	assert.Contains(t, errOut, "invalid team ID", "stderr must explain the rejection")
}

// TestScenarioTeam_BackwardCompat: a session registered without --team has no
// teamId (whoami shows "(none)"), and is excluded from any --team filter — the
// additive field never changes behaviour for team-less sessions.
func TestScenarioTeam_BackwardCompat(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	id := registerTeam(t, dataDir, "ESC-legacy", "", t.TempDir())

	out, errOut, exit := run(t, []string{"whoami", "--session-id=" + id}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "whoami: %s", errOut)
	assert.Contains(t, out, "(none)", "team-less session must show team (none)")

	// Excluded from a team filter.
	out, errOut, exit = run(t, []string{"peers", "--team=anything", "--json"}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "peers --team: %s", errOut)
	assert.NotContains(t, out, id, "team-less session must not match any --team filter")

	// Default register JSON must not carry an empty teamId (omitempty).
	out, errOut, exit = run(t, []string{"inspect", id}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "inspect: %s", errOut)
	assert.NotContains(t, out, "teamId", "team-less manifest JSON must omit teamId (omitempty)")
}
