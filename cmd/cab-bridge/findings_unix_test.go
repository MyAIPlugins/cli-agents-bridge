//go:build !windows

package main

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindingsStale_NotesALooseRegistryAndDoesNotChangeIt(t *testing.T) {
	e := newFindingsEnv(t)
	e.seed(t, 10*24*time.Hour, "old-one", "aperto")
	require.NoError(t, os.Chmod(e.registry(), 0o644))

	_, stderr, err := e.run("", "stale")
	require.NoError(t, err)
	assert.Equal(t, "findings: note: 1 open for more than 7 days\nfindings: note: the registry has mode 0644 (the next add makes it 0600)\n", stderr)
	info, err := os.Stat(e.registry())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "stale does not change the mode")
}

func TestFindingsAdd_SaysWhenItRepublishesALooseRegistryAs0600(t *testing.T) {
	e := newFindingsEnv(t)
	e.seed(t, time.Hour, "first", "aperto")
	require.NoError(t, os.Chmod(e.registry(), 0o644))

	_, stderr, err := e.run(validFindingJSON(t, map[string]string{"chiave": "second"}), "add")
	require.NoError(t, err)
	assert.Equal(t, noNamesLine+"findings: note: the registry had mode 0644, it is 0600 now\n", stderr)
	info, err := os.Stat(e.registry())
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
