//go:build !windows

package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// mustSymlink creates a symlink a fixture needs. Every Unix grants it, so a
// failure here is a real failure. The Windows counterpart in
// symlink_windows_test.go is where the interesting case lives.
func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	require.NoError(t, os.Symlink(target, link))
}
