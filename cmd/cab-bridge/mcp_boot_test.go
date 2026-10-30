package main

import (
	"errors"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func home(path string, err error) func() (string, error) {
	return func() (string, error) { return path, err }
}

func TestMCPPreflight(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		home    func() (string, error)
		env     map[string]string
		wantErr string // substring; empty means it must pass
	}{
		{"a normal environment", home("/Users/alan", nil), nil, ""},
		{"an absolute data dir is fine", home("/Users/alan", nil), map[string]string{"CAB_DATA_DIR": "/var/bridge"}, ""},
		{"no home (F-145: the case that built a private bridge in the cwd)", home("", errors.New("$HOME is not defined")), nil, "no home directory"},
		{"a relative home", home("relative/home", nil), nil, "not an absolute path"},
		{"a relative CAB_DATA_DIR", home("/Users/alan", nil), map[string]string{"CAB_DATA_DIR": ".claude/bridge"}, "CAB_DATA_DIR"},
		{"a relative CAB_DATA_DIR is refused even with a good home", home("/Users/alan", nil), map[string]string{"CAB_DATA_DIR": "bridge"}, "not an absolute path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := mcpPreflight(tc.home, envOf(tc.env))
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Contains(t, err.Error(), "not starting", "the refusal says it is a refusal")
		})
	}
}

// The real standard output goes to the caller and os.Stdout is pointed at stderr,
// so a stray print from reused code can no longer reach the protocol channel.
// Not parallel: it swaps process-global descriptors.
func TestReserveProtocolStdout_StrayPrintsNeverReachTheProtocolChannel(t *testing.T) {
	protoR, protoW, err := os.Pipe()
	require.NoError(t, err)
	errR, errW, err := os.Pipe()
	require.NoError(t, err)

	origOut, origErr := os.Stdout, os.Stderr
	t.Cleanup(func() { os.Stdout, os.Stderr = origOut, origErr })
	os.Stdout, os.Stderr = protoW, errW

	real := reserveProtocolStdout()
	assert.Same(t, protoW, real, "the protocol writer gets the real stdout")
	assert.Same(t, errW, os.Stdout, "and os.Stdout is stderr from now on")

	_, werr := os.Stdout.WriteString("a stray line from reused code\n")
	require.NoError(t, werr)
	_, werr = real.WriteString(`{"jsonrpc":"2.0"}` + "\n")
	require.NoError(t, werr)

	require.NoError(t, protoW.Close())
	require.NoError(t, errW.Close())
	onProtocol, _ := io.ReadAll(protoR)
	onStderr, _ := io.ReadAll(errR)
	assert.Equal(t, `{"jsonrpc":"2.0"}`+"\n", string(onProtocol), "only the protocol writer reaches the protocol channel")
	assert.Equal(t, "a stray line from reused code\n", string(onStderr), "the stray line went to the diagnostics")
}

func validMCPConfig() config.Config { return config.DefaultConfig() }

func TestValidateMCPConfig_TheDefaultsAreValid(t *testing.T) {
	t.Parallel()
	require.NoError(t, validateMCPConfig(validMCPConfig()))
}

// The numbers that decide whether an answer is ever received are checked before the
// server starts, against what was measured (the relay's 60 s) and not against taste.
func TestValidateMCPConfig_RefusesWhatCannotBeHonoured(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mut  func(*config.Config)
		want string
	}{
		{"a wait that outlasts the relay", func(c *config.Config) { c.MCPNextMaxWaitSeconds = 58; c.MCPNextDefaultWaitSeconds = 25 }, "gives up on a tool call after 60"},
		{"a wait that fits alone but not with the inbox scan", func(c *config.Config) { c.MCPNextMaxWaitSeconds = 48; c.MCPScanBudgetSeconds = 5 }, "could take 53 s"},
		{"a default wait above the maximum", func(c *config.Config) { c.MCPNextDefaultWaitSeconds = 41 }, "larger than mcp_next_max_wait_seconds"},
		{"a child deadline above the relay's patience", func(c *config.Config) { c.MCPChildTimeoutSeconds = 51 }, "mcp_child_timeout_seconds"},
		{"a result budget below the floor", func(c *config.Config) { c.MCPMaxResultBytes = 100 }, "below the floor"},
		{"a zero wait (somebody would read it as no limit)", func(c *config.Config) { c.MCPNextMaxWaitSeconds = 0 }, "must be positive"},
		{"a negative scan budget", func(c *config.Config) { c.MCPScanBudgetSeconds = -1 }, "must be positive"},
		{"a zero grant lifetime", func(c *config.Config) { c.MCPGrantTTLMinutes = 0 }, "must be positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validMCPConfig()
			tc.mut(&cfg)
			err := validateMCPConfig(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), "not starting")
		})
	}
}

func TestValidateMCPConfig_TheBoundaryIsExact(t *testing.T) {
	t.Parallel()
	cfg := validMCPConfig()
	cfg.MCPNextMaxWaitSeconds, cfg.MCPNextDefaultWaitSeconds, cfg.MCPScanBudgetSeconds = 45, 25, 5 // exactly 50
	require.NoError(t, validateMCPConfig(cfg), "50 s is the most a call may take: allowed")
	cfg.MCPNextMaxWaitSeconds = 46
	require.Error(t, validateMCPConfig(cfg), "51 s is not")
}
