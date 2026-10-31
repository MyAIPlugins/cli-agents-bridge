package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A waiting `next` looks at the inbox as often as the rest of the tool does: one
// setting, poll_interval_ms, and nothing hardcoded beside it.
func TestMCPApp_ThePollIntervalIsTheConfiguredOne(t *testing.T) {
	t.Parallel()
	cfg := nextTestConfig(t.TempDir())
	cfg.PollIntervalMs = 37
	app := newMCPApp(context.Background(), cfg, newSessionManager(cfg), "", t.TempDir(), func(string, ...any) {})
	assert.Equal(t, 37*time.Millisecond, app.pollInterval)
}

func TestValidateMCPConfig_RefusesAPollIntervalThatIsNotPositive(t *testing.T) {
	t.Parallel()
	cfg := nextTestConfig(t.TempDir())
	require.NoError(t, validateMCPConfig(cfg))
	for _, ms := range []int{0, -5} {
		cfg.PollIntervalMs = ms
		err := validateMCPConfig(cfg)
		require.Error(t, err, "poll_interval_ms %d", ms)
		assert.Contains(t, err.Error(), "poll_interval_ms")
	}
}
