package main

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// captureStderr survived the v0.8 removal of the commands whose tests used to
// define it; several suites still need it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	defer func() { os.Stderr = old }()

	fn()
	require.NoError(t, w.Close())
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(data)
}
