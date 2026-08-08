package integration

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// registerPair survived the removal of the auto-ack scenarios that defined it.
func registerPair(t *testing.T, dataDir, suffix string) (valID, escID string) {
	t.Helper()
	out, errOut, exit := run(t, []string{"register", "--role=val", "--agent-name=VAL-" + suffix, "--project-path=" + t.TempDir()}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "register VAL: %s", errOut)
	valID = mustJSONField(t, out, "sessionId")

	out, errOut, exit = run(t, []string{"register", "--role=esc", "--agent-name=ESC-" + suffix, "--project-path=" + t.TempDir()}, dataDirEnv(dataDir))
	require.Equal(t, 0, exit, "register ESC: %s", errOut)
	escID = mustJSONField(t, out, "sessionId")
	return valID, escID
}
