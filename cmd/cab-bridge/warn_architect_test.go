package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// The architect is the session that Claude Desktop drives over MCP, and the server
// never waits as a listener: a note about the absence of one is false for the model
// reading it. The branch next to it, any other session that never listened, is warned
// as before.
func TestWarnNotListening_AnArchitectHasNoListenerByDesignAndIsNotWarned(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	mgr := newSessionManager(cfg)
	plantOverviewSession(t, dataDir, "wrnarc001", session.RoleArchitect, "ARC-w", "/repo/w", "", session.StateIdle)
	plantOverviewSession(t, dataDir, "wrnesc001", session.RoleEsc, "ESC-w", "/repo/w", "", "working")

	var arc, esc bytes.Buffer
	warnNotListening(mgr, "wrnarc001", &arc)
	warnNotListening(mgr, "wrnesc001", &esc)

	assert.Empty(t, arc.String(), "an architect never listened and is not told it should")
	assert.Contains(t, esc.String(), "no next is listening", "and the same state in any other role still is")
}
