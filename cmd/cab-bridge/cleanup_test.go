package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real incident, as a test. A critic ran
//
//	CAB_RETENTION_DAYS=1 cab-bridge cleanup --scope=global --force
//
// from inside its own sandbox, after a `cd`, and deleted thirteen archived
// sessions from the PRODUCTION data dir. The binary did exactly what it was
// told; what the command let it believe is the defect — the data dir comes from
// $HOME, never from the cwd, and nothing said so.
func TestRunCleanup_GlobalForceMustNameItsTarget(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	t.Setenv("CAB_AUTO_GC_HOURS", "0")

	t.Run("the_command_from_the_incident_is_refused", func(t *testing.T) {
		err := runCleanup([]string{"--scope=global", "--force"})
		require.Error(t, err, "--force removes the question, not the aim")
		assert.Contains(t, err.Error(), "--data-dir")
		assert.Contains(t, err.Error(), dataDir, "and it names the target, absolute")
		assert.Contains(t, err.Error(), "$HOME", "and says where that path comes from")
	})

	t.Run("a_mismatched_target_is_refused_rather_than_guessed", func(t *testing.T) {
		err := runCleanup([]string{"--scope=global", "--force", "--data-dir=/somewhere/else"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not match")
	})

	t.Run("naming_the_right_one_proceeds", func(t *testing.T) {
		_, stderr := captureStdoutStderr(t, func() {
			require.NoError(t, runCleanup([]string{"--scope=global", "--force", "--data-dir=" + dataDir}))
		})
		assert.Contains(t, stderr, "cleanup will act on "+dataDir,
			"the announcement comes BEFORE the work, and cannot be switched off")
	})

	// my-session stays one word long: the requirement is on the blast radius,
	// not on the command.
	t.Run("my_session_is_not_burdened", func(t *testing.T) {
		err := runCleanup([]string{"--scope=my-session", "--session-id=nosuchid"})
		if err != nil {
			assert.NotContains(t, err.Error(), "--data-dir", "the narrow scope needs no target declaration")
		}
	})
}
