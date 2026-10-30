package main

import (
	"errors"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
